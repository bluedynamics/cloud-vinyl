package agent

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeVarnishCLI serves the Varnish admin protocol well enough to answer a
// single command, so the real client code path (banner, auth, response
// framing) is exercised instead of being mocked away.
//
// It replies to every command with cmdBody, which is what the tests vary.
func fakeVarnishCLI(t *testing.T, cmdBody string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				rw := bufio.NewReadWriter(bufio.NewReader(conn), bufio.NewWriter(conn))

				// Banner: 107 = authentication required. The client reads the
				// first line of the body as the challenge.
				banner := "0123456789abcdef0123456789abcdef\nAuthentication required.\n"
				fmt.Fprintf(rw, "107 %d\n%s\n", len(banner), banner)
				_ = rw.Flush()

				// auth <hash>
				if _, err := rw.ReadString('\n'); err != nil {
					return
				}
				fmt.Fprintf(rw, "200 %d\n%s\n", 0, "")
				_ = rw.Flush()

				// the actual command
				if _, err := rw.ReadString('\n'); err != nil {
					return
				}
				fmt.Fprintf(rw, "200 %d\n%s\n", len(cmdBody), cmdBody)
				_ = rw.Flush()
			}(conn)
		}
	}()
	return ln.Addr().String()
}

// TestActiveVCLReturnsName pins the one thing the readiness probe depends on:
// ActiveVCL must return the VCL *name*, not some other column of vcl.list.
//
// The fixtures are verbatim varnishadm output captured from running varnishd
// 7.6.5 and 8.0.2 containers.
func TestActiveVCLReturnsName(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "fresh varnishd still on the bootstrap VCL",
			body: "active   auto   warm   0   boot\n",
			want: "boot",
		},
		{
			name: "after the operator pushed a named VCL",
			body: "available   auto   warm   0   boot\n" +
				"active      auto   warm   0   livepush\n",
			want: "livepush",
		},
		{
			name: "several loaded VCLs, active one last",
			body: "available   auto   cold   0   boot\n" +
				"available   auto   warm   0   vcl-1a2b3c\n" +
				"active      auto   warm   0   vcl-4d5e6f\n",
			want: "vcl-4d5e6f",
		},
		{
			// A VCL with labels attached grows *extra* columns after the
			// name, so the name is not the last field on the line.
			name: "active VCL that has a label attached",
			body: "active      auto    warm   0   boot      <-   (1 label)\n" +
				"available   label   warm   0   mylabel   ->   boot\n",
			want: "boot",
		},
		{
			name: "a label is the active VCL",
			body: "available   auto    warm   0   boot      <-   (1 label)\n" +
				"active      label   warm   0   mylabel   ->   boot\n",
			want: "mylabel",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			addr := fakeVarnishCLI(t, tc.body)
			c := NewAdminClient(addr, "secret")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			got, err := c.ActiveVCL(ctx)
			if err != nil {
				t.Fatalf("ActiveVCL: %v", err)
			}
			if got != tc.want {
				t.Errorf("ActiveVCL() = %q, want %q\nvcl.list was:\n%s",
					got, tc.want, strings.TrimRight(tc.body, "\n"))
			}
		})
	}
}

// TestHealthGatesOnBootstrapVCL closes the gap that let #73 ship: the existing
// Health tests drive the handler through a mock AdminClient that returns "boot",
// a value the real client never produced. This wires the real AdminClient to a
// varnishd speaking the actual CLI protocol, so the readiness contract is
// tested across the seam where it actually broke.
func TestHealthGatesOnBootstrapVCL(t *testing.T) {
	tests := []struct {
		name     string
		vclList  string
		wantCode int
	}{
		{
			name:     "bootstrap VCL still active means not ready",
			vclList:  "active   auto   warm   0   boot\n",
			wantCode: http.StatusServiceUnavailable,
		},
		{
			name: "operator VCL pushed means ready",
			vclList: "available   auto   warm   0   boot\n" +
				"active      auto   warm   0   vcl-4d5e6f\n",
			wantCode: http.StatusOK,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			addr := fakeVarnishCLI(t, tc.vclList)
			h := NewHandler(NewAdminClient(addr, "secret"), NewXkeyPurger("http://127.0.0.1:8080"))

			rr := httptest.NewRecorder()
			h.Health(rr, httptest.NewRequest(http.MethodGet, "/health", nil))

			if rr.Code != tc.wantCode {
				t.Errorf("Health() = %d, want %d\nbody: %s\nvcl.list was:\n%s",
					rr.Code, tc.wantCode, rr.Body.String(),
					strings.TrimRight(tc.vclList, "\n"))
			}
		})
	}
}

// TestActiveVCLRejectsUnrecognisedShape covers what happens on a varnishd whose
// vcl.list does not have the layout we parse. Varnish 6.0 LTS is the live
// example: it collapses state and temperature into one column, so a row has
// four fields where 7.6 and later have five.
//
//	varnish 6.0.18:  active      auto/warm          0 boot
//	varnish 8.0.2:   active   auto   warm   0   boot
//
// Returning "" there is the worst possible answer. Health compares the name
// against "boot", so an empty string reads as "operator VCL is live" and the
// pod goes Ready while still serving the bootstrap 503 — the exact failure #73
// was about. It also makes every pod look out of date forever, so the operator
// re-pushes on every reconcile and never converges.
//
// An error is the honest answer: Health turns it into a 503, the pod stays
// NotReady, and the operator logs why.
func TestActiveVCLRejectsUnrecognisedShape(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			name: "varnish 6.0 collapses state and temperature into one column",
			body: "active      auto/warm          0 boot\n",
		},
		{
			name: "no active row at all",
			body: "available   auto   warm   0   boot\n",
		},
		{
			name: "empty response",
			body: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			addr := fakeVarnishCLI(t, tc.body)
			c := NewAdminClient(addr, "secret")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			got, err := c.ActiveVCL(ctx)

			if err == nil {
				t.Fatalf("ActiveVCL() = %q with nil error; an unparseable vcl.list must "+
					"surface, not silently look like a pushed VCL", got)
			}
			if got != "" {
				t.Errorf("ActiveVCL() = %q, want empty alongside the error", got)
			}
		})
	}
}

// TestHealthFailsClosedOnUnrecognisedShape is the consequence that matters:
// a varnishd we cannot read must keep the pod out of the Service endpoints.
func TestHealthFailsClosedOnUnrecognisedShape(t *testing.T) {
	addr := fakeVarnishCLI(t, "active      auto/warm          0 boot\n")
	h := NewHandler(NewAdminClient(addr, "secret"), NewXkeyPurger("http://127.0.0.1:8080"))

	rr := httptest.NewRecorder()
	h.Health(rr, httptest.NewRequest(http.MethodGet, "/health", nil))

	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("Health() = %d, want 503 when vcl.list cannot be parsed\nbody: %s",
			rr.Code, rr.Body.String())
	}
}
