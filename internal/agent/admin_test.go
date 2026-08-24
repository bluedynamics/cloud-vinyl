//go:build integration

package agent

// Integration tests for the Varnish admin protocol against a real varnishd.
//
// Run with: go test -tags=integration ./internal/agent/...
//
// These drive the same code path the vinyl-agent uses in production: SHA256
// challenge-response auth, vcl.inline + vcl.use, vcl.list, ban and vcl.discard.
// They exist because #73 shipped a vcl.list parser matching a column layout
// Varnish stopped using years ago. Unit tests could not catch that: the handler
// tests mock AdminClient, and a mock returns whatever we tell it to.
//
// varnishd runs via the docker CLI rather than testcontainers-go. Starting one
// container and speaking TCP to it does not need a lifecycle library, and this
// keeps the dependency out of go.mod. Tests skip when docker is unavailable.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// varnishImage is the image under test. Keep the default in sync with the
// varnish version the operator is documented against, so a base image bump
// exercises the protocol before it reaches users.
func varnishImage() string {
	if img := os.Getenv("VINYL_TEST_VARNISH_IMAGE"); img != "" {
		return img
	}
	return "varnish:8.0.2"
}

const testSecret = "cloud-vinyl-integration-test-secret"

// startVarnishd boots varnishd with the exact arguments the operator generates
// and returns the host address of its admin CLI.
func startVarnishd(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}

	dir := t.TempDir()
	// The container runs as its own uid, so both files must be world readable.
	secretPath := filepath.Join(dir, "secret")
	if err := os.WriteFile(secretPath, []byte(testSecret), 0o644); err != nil {
		t.Fatalf("write secret: %v", err)
	}
	vclPath := filepath.Join(dir, "default.vcl")
	bootstrap := "vcl 4.1;\n" +
		"backend bootstrap_placeholder { .host = \"127.0.0.1\"; .port = \"1\"; }\n" +
		"sub vcl_recv { return (synth(503, \"initializing\")); }\n"
	if err := os.WriteFile(vclPath, []byte(bootstrap), 0o644); err != nil {
		t.Fatalf("write vcl: %v", err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatalf("chmod tempdir: %v", err)
	}

	// --context default: the machine's active docker context may point at a
	// remote host, where a published port would not be reachable from here.
	run := exec.Command("docker", "--context", "default", "run", "--rm", "-d",
		"-p", "127.0.0.1::6082",
		"-v", secretPath+":/etc/varnish/secret:ro",
		"-v", vclPath+":/etc/varnish/default.vcl:ro",
		"-e", "VARNISH_HTTP_PORT=8080",
		varnishImage(),
		"-n", "vinyl", "-j", "none",
		"-T", "0.0.0.0:6082", "-S", "/etc/varnish/secret",
		"-s", "default=malloc,104857600",
	)
	out, err := run.CombinedOutput()
	if err != nil {
		t.Skipf("cannot start %s (%v): %s", varnishImage(), err, out)
	}
	id := strings.TrimSpace(string(out))
	t.Cleanup(func() {
		_ = exec.Command("docker", "--context", "default", "rm", "-f", id).Run()
	})

	portOut, err := exec.Command("docker", "--context", "default", "port", id, "6082/tcp").Output()
	if err != nil {
		t.Fatalf("docker port: %v", err)
	}
	addr := strings.TrimSpace(strings.Split(string(portOut), "\n")[0])

	// varnishd needs a moment before the CLI accepts connections.
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, err := NewAdminClient(addr, testSecret).ActiveVCL(ctx)
		cancel()
		if err == nil {
			return addr
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("varnishd admin CLI at %s never became ready", addr)
	return ""
}

func newLiveClient(t *testing.T) (AdminClient, context.Context) {
	t.Helper()
	addr := startVarnishd(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return NewAdminClient(addr, testSecret), ctx
}

const validVCL = `vcl 4.1;
backend b { .host = "127.0.0.1"; .port = "1"; }
sub vcl_recv { return (synth(200, "ok")); }
`

// TestIntegrationActiveVCLReportsBootOnFreshStart is the direct regression guard
// for #73. A freshly started varnishd is on the VCL named "boot", and the
// readiness probe keys off exactly that string. The pre-#73 parser returned
// "warm" here, so pods reported Ready while still serving the bootstrap 503.
func TestIntegrationActiveVCLReportsBootOnFreshStart(t *testing.T) {
	c, ctx := newLiveClient(t)
	name, err := c.ActiveVCL(ctx)
	if err != nil {
		t.Fatalf("ActiveVCL: %v", err)
	}
	if name != "boot" {
		t.Errorf("ActiveVCL() = %q on a fresh varnishd, want %q", name, "boot")
	}
}

func TestIntegrationPushVCLBecomesActive(t *testing.T) {
	c, ctx := newLiveClient(t)
	if err := c.PushVCL(ctx, "pushed_vcl", validVCL); err != nil {
		t.Fatalf("PushVCL: %v", err)
	}
	name, err := c.ActiveVCL(ctx)
	if err != nil {
		t.Fatalf("ActiveVCL: %v", err)
	}
	if name != "pushed_vcl" {
		t.Errorf("ActiveVCL() = %q after push, want %q", name, "pushed_vcl")
	}
}

func TestIntegrationValidateVCLAcceptsValid(t *testing.T) {
	c, ctx := newLiveClient(t)
	res, err := c.ValidateVCL(ctx, "check_ok", validVCL)
	if err != nil {
		t.Fatalf("ValidateVCL: %v", err)
	}
	if !res.Valid {
		t.Errorf("ValidateVCL() = %+v, want Valid", res)
	}
}

func TestIntegrationValidateVCLRejectsInvalidWithLineNumber(t *testing.T) {
	c, ctx := newLiveClient(t)
	res, err := c.ValidateVCL(ctx, "check_bad", "vcl 4.1;\nthis is not valid vcl\n")
	if err != nil {
		t.Fatalf("ValidateVCL: %v", err)
	}
	if res.Valid {
		t.Fatal("ValidateVCL() reported valid for broken VCL")
	}
	if res.Line != 2 {
		t.Errorf("ValidateVCL() Line = %d, want 2 (message: %q)", res.Line, res.Message)
	}
}

func TestIntegrationBan(t *testing.T) {
	c, ctx := newLiveClient(t)
	if err := c.Ban(ctx, "req.url ~ /"); err != nil {
		t.Errorf("Ban: %v", err)
	}
}

func TestIntegrationDiscardVCL(t *testing.T) {
	c, ctx := newLiveClient(t)
	if err := c.PushVCL(ctx, "first_vcl", validVCL); err != nil {
		t.Fatalf("PushVCL first: %v", err)
	}
	if err := c.PushVCL(ctx, "second_vcl", validVCL); err != nil {
		t.Fatalf("PushVCL second: %v", err)
	}
	// first_vcl is loaded but no longer active, so it can be discarded.
	if err := c.DiscardVCL(ctx, "first_vcl"); err != nil {
		t.Errorf("DiscardVCL: %v", err)
	}
	name, err := c.ActiveVCL(ctx)
	if err != nil {
		t.Fatalf("ActiveVCL: %v", err)
	}
	if name != "second_vcl" {
		t.Errorf("ActiveVCL() = %q after discard, want %q", name, "second_vcl")
	}
}
