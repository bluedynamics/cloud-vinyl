package agent

import (
	"bufio"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

// AdminClient is the interface to the Varnish admin protocol.
type AdminClient interface {
	// PushVCL loads and activates a VCL. Discards old VCL after delay.
	PushVCL(ctx context.Context, name, vcl string) error

	// ValidateVCL checks VCL syntax without activating it.
	ValidateVCL(ctx context.Context, name, vcl string) (ValidationResult, error)

	// ActiveVCL returns the name of the currently active VCL.
	ActiveVCL(ctx context.Context) (string, error)

	// Ban pushes a ban expression via the admin protocol.
	Ban(ctx context.Context, expression string) error

	// DiscardVCL discards a loaded but inactive VCL.
	DiscardVCL(ctx context.Context, name string) error
}

// ValidationResult holds the result of a VCL syntax check.
type ValidationResult struct {
	Valid   bool
	Message string
	Line    int
}

type varnishAdminClient struct {
	addr   string
	secret string
}

// NewAdminClient creates a new Varnish admin protocol client.
func NewAdminClient(addr, secret string) AdminClient {
	return &varnishAdminClient{addr: addr, secret: secret}
}

func (c *varnishAdminClient) connect(ctx context.Context) (net.Conn, *bufio.ReadWriter, error) {
	dialer := net.Dialer{Timeout: 10 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", c.addr)
	if err != nil {
		return nil, nil, fmt.Errorf("connect to varnish admin %s: %w", c.addr, err)
	}
	rw := bufio.NewReadWriter(bufio.NewReader(conn), bufio.NewWriter(conn))

	// Read banner (challenge)
	code, challenge, err := readResponse(rw)
	if err != nil {
		_ = conn.Close()
		return nil, nil, fmt.Errorf("read banner: %w", err)
	}
	if code != 107 { // 107 = authentication required
		_ = conn.Close()
		return nil, nil, fmt.Errorf("expected auth challenge (107), got %d", code)
	}

	// Compute auth response per Varnish CLI protocol (cli_auth.c):
	// SHA256(challenge + "\n" + secret_file_content + challenge + "\n")
	// Note: NO newline between secret and second challenge occurrence.
	// The banner body contains the 32-char challenge followed by
	// additional text ("Authentication required.") — extract only
	// the first 32 characters.
	lines := strings.SplitN(strings.TrimSpace(challenge), "\n", 2)
	challenge = strings.TrimSpace(lines[0])
	hash := sha256.Sum256([]byte(challenge + "\n" + c.secret + challenge + "\n"))
	hexHash := fmt.Sprintf("%x", hash)

	// Authenticate
	if _, err = fmt.Fprintf(rw, "auth %s\n", hexHash); err != nil {
		_ = conn.Close()
		return nil, nil, fmt.Errorf("write auth: %w", err)
	}
	if err = rw.Flush(); err != nil {
		_ = conn.Close()
		return nil, nil, fmt.Errorf("flush auth: %w", err)
	}

	code, _, err = readResponse(rw)
	if err != nil {
		_ = conn.Close()
		return nil, nil, fmt.Errorf("auth response: %w", err)
	}
	if code != 200 {
		_ = conn.Close()
		return nil, nil, fmt.Errorf("authentication failed (code %d)", code)
	}

	return conn, rw, nil
}

func readResponse(rw *bufio.ReadWriter) (int, string, error) {
	// Response format: "<code> <len>\n<body>\n"
	header, err := rw.ReadString('\n')
	if err != nil {
		return 0, "", fmt.Errorf("read response header: %w", err)
	}
	parts := strings.Fields(header)
	if len(parts) < 2 {
		return 0, "", fmt.Errorf("malformed response header: %q", header)
	}
	code, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, "", fmt.Errorf("invalid response code: %w", err)
	}
	length, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, "", fmt.Errorf("invalid response length: %w", err)
	}
	body := make([]byte, length+1) // +1 for trailing newline
	_, err = io.ReadFull(rw, body)
	if err != nil {
		return 0, "", fmt.Errorf("read response body: %w", err)
	}
	return code, string(body[:length]), nil
}

func (c *varnishAdminClient) sendCommand(ctx context.Context, cmd string) (int, string, error) {
	conn, rw, err := c.connect(ctx)
	if err != nil {
		return 0, "", err
	}
	defer conn.Close()

	if _, err = fmt.Fprintf(rw, "%s\n", cmd); err != nil {
		return 0, "", fmt.Errorf("write command: %w", err)
	}
	if err = rw.Flush(); err != nil {
		return 0, "", fmt.Errorf("flush command: %w", err)
	}
	return readResponse(rw)
}

func (c *varnishAdminClient) sendInlineVCL(ctx context.Context, cmd, name, vcl string) (int, string, error) {
	conn, rw, err := c.connect(ctx)
	if err != nil {
		return 0, "", err
	}
	defer conn.Close()

	// vcl.inline uses heredoc syntax: "vcl.inline <name> << EOF\n<vcl>\nEOF\n"
	payload := fmt.Sprintf("%s %s << EOF\n%s\nEOF\n", cmd, name, vcl)
	if _, err = fmt.Fprint(rw, payload); err != nil {
		return 0, "", fmt.Errorf("write inline vcl: %w", err)
	}
	if err = rw.Flush(); err != nil {
		return 0, "", fmt.Errorf("flush inline vcl: %w", err)
	}
	return readResponse(rw)
}

// PushVCL loads and activates a VCL, then asynchronously discards the old one.
func (c *varnishAdminClient) PushVCL(ctx context.Context, name, vcl string) error {
	// 1. Get current active VCL name for later discard
	oldName, _ := c.ActiveVCL(ctx) // ignore error — might be empty on first push

	// 2. Load new VCL
	code, msg, err := c.sendInlineVCL(ctx, "vcl.inline", name, vcl)
	if err != nil {
		return fmt.Errorf("vcl.inline: %w", err)
	}
	if code != 200 {
		return fmt.Errorf("vcl.inline failed (code %d): %s", code, msg)
	}

	// 3. Activate new VCL
	code, msg, err = c.sendCommand(ctx, fmt.Sprintf("vcl.use %s", name))
	if err != nil {
		return fmt.Errorf("vcl.use: %w", err)
	}
	if code != 200 {
		return fmt.Errorf("vcl.use failed (code %d): %s", code, msg)
	}

	// 4. Discard old VCL asynchronously after grace period (§3.4)
	if oldName != "" && oldName != name {
		go func() {
			time.Sleep(5 * time.Second)
			discardCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = c.DiscardVCL(discardCtx, oldName)
		}()
	}

	return nil
}

// ValidateVCL checks VCL syntax without activating it.
func (c *varnishAdminClient) ValidateVCL(ctx context.Context, name, vcl string) (ValidationResult, error) {
	// Use vcl.inline with a temporary name, then check the response
	tempName := fmt.Sprintf("validate_%d", time.Now().UnixNano())
	code, msg, err := c.sendInlineVCL(ctx, "vcl.inline", tempName, vcl)
	if err != nil {
		return ValidationResult{}, err
	}
	if code == 200 {
		// Discard the loaded VCL immediately (it was just for validation)
		go func() {
			discardCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = c.DiscardVCL(discardCtx, tempName)
		}()
		return ValidationResult{Valid: true}, nil
	}
	// Parse line number from error message if present
	result := ValidationResult{Valid: false, Message: msg}
	// Error messages often contain "Line N:" prefix
	if _, after, found := strings.Cut(msg, "Line "); found {
		var line int
		_, _ = fmt.Sscanf(after, "%d", &line)
		result.Line = line
	}
	return result, nil
}

// ActiveVCL returns the name of the currently active VCL.
func (c *varnishAdminClient) ActiveVCL(ctx context.Context) (string, error) {
	code, body, err := c.sendCommand(ctx, "vcl.list")
	if err != nil {
		return "", err
	}
	if code != 200 {
		return "", fmt.Errorf("vcl.list failed (code %d): %s", code, body)
	}
	// Output format: "active       0 <name>"
	for line := range strings.SplitSeq(body, "\n") {
		if strings.HasPrefix(line, "active") {
			parts := strings.Fields(line)
			if len(parts) >= 3 {
				return parts[2], nil
			}
		}
	}
	return "", nil
}

// Ban pushes a ban expression via the admin protocol.
func (c *varnishAdminClient) Ban(ctx context.Context, expression string) error {
	code, msg, err := c.sendCommand(ctx, fmt.Sprintf("ban %s", expression))
	if err != nil {
		return err
	}
	if code != 200 {
		return fmt.Errorf("ban failed (code %d): %s", code, msg)
	}
	return nil
}

// DiscardVCL discards a loaded but inactive VCL.
func (c *varnishAdminClient) DiscardVCL(ctx context.Context, name string) error {
	code, msg, err := c.sendCommand(ctx, fmt.Sprintf("vcl.discard %s", name))
	if err != nil {
		return err
	}
	if code != 200 {
		return fmt.Errorf("vcl.discard failed (code %d): %s", code, msg)
	}
	return nil
}
