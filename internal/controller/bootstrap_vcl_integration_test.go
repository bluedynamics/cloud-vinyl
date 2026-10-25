//go:build integration

/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestBootstrapVCLCompiles compiles bootstrapVCL with a real varnishd.
// varnishd loads it with -f at startup and exits when it does not compile, so
// a construct the cache image no longer knows leaves every pod crash-looping
// before the agent can push anything. Varnish 9.1 dropping synthetic() did
// exactly that.
//
// The default image follows internal/agent/admin_test.go: keep both on the
// varnish version the operator is documented against.
func TestBootstrapVCLCompiles(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	image := os.Getenv("VINYL_TEST_VARNISH_IMAGE")
	if image == "" {
		image = "varnish:9.1.0"
	}

	dir := t.TempDir()
	vclPath := filepath.Join(dir, "default.vcl")
	if err := os.WriteFile(vclPath, []byte(bootstrapVCL), 0o644); err != nil {
		t.Fatalf("write vcl: %v", err)
	}
	// The container runs as its own uid, so the file must be world readable.
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatalf("chmod tempdir: %v", err)
	}

	// -C compiles the VCL to C and exits, which is the same VCC pass varnishd
	// runs on -f at startup.
	out, err := exec.Command("docker", "--context", "default", "run", "--rm",
		"--entrypoint", "varnishd",
		"-v", vclPath+":/bootstrap.vcl:ro",
		image, "-C", "-f", "/bootstrap.vcl",
	).CombinedOutput()
	if err != nil {
		t.Fatalf("%s does not compile the bootstrap VCL: %v\n%s", image, err, out)
	}
}
