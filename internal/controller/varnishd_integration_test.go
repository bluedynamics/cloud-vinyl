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
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/resource"

	vinylv1 "github.com/bluedynamics/cloud-vinyl/api/v1"
)

// testVarnishImage is the image these tests run varnishd from.
func testVarnishImage(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	if image := os.Getenv("VINYL_TEST_VARNISH_IMAGE"); image != "" {
		return image
	}
	return "varnish:9.1.0"
}

// docker runs the docker CLI on the local default context: the machine's
// active context may point at a remote host.
func docker(args ...string) *exec.Cmd {
	return exec.Command("docker", append([]string{"--context", "default"}, args...)...)
}

// TestBootstrapVCLCompiles compiles bootstrapVCL with a real varnishd.
// varnishd loads it with -f at startup and exits when it does not compile, so
// a construct the cache image no longer knows leaves every pod crash-looping
// before the agent can push anything. Varnish 9.1 dropping synthetic() did
// exactly that.
//
// The default image follows internal/agent/admin_test.go: keep both on the
// varnish version the operator is documented against.
func TestBootstrapVCLCompiles(t *testing.T) {
	image := testVarnishImage(t)

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
	out, err := docker("run", "--rm",
		"--entrypoint", "varnishd",
		"-v", vclPath+":/bootstrap.vcl:ro",
		image, "-C", "-f", "/bootstrap.vcl",
	).CombinedOutput()
	if err != nil {
		t.Fatalf("%s does not compile the bootstrap VCL: %v\n%s", image, err, out)
	}
}

// TestVarnishdStartsWithOperatorCommandLine boots varnishd the way the pod
// does: the container command is varnishd (not the image entrypoint), the
// arguments come from varnishdArgs, and it runs as UID 65532 on a read-only
// root filesystem with the workdir and /tmp on writable volumes.
func TestVarnishdStartsWithOperatorCommandLine(t *testing.T) {
	image := testVarnishImage(t)

	t.Run("spec.storage and PROXY enabled", func(t *testing.T) {
		vc := &vinylv1.VinylCache{}
		vc.Spec.Storage = []vinylv1.StorageSpec{
			{Name: "cache", Type: "malloc", Size: resource.MustParse("64Mi")},
		}
		vc.Spec.ProxyProtocol = vinylv1.ProxyProtocolSpec{Enabled: true, Port: 8081}
		id := startOperatorVarnishd(t, image, vc)

		storages := varnishadm(t, id, "storage.list")
		assert.Contains(t, storages, "storage.cache")
		assert.NotContains(t, storages, "storage.s0",
			"spec.storage must be the only cache storage (#113)")

		listeners := varnishadm(t, id, "debug.listen_address")
		assert.Contains(t, listeners, "http 0.0.0.0 8080")
		assert.Contains(t, listeners, "proxy 0.0.0.0 8081")
		assert.NotContains(t, listeners, "8443")

		out, err := docker("exec", id, "curl", "-s", "-o", "/dev/null", "-w", "%{http_code}",
			"http://127.0.0.1:8080/").Output()
		require.NoError(t, err)
		assert.Equal(t, "503", string(out), "the bootstrap VCL answers until the agent pushes")
	})

	t.Run("defaults", func(t *testing.T) {
		id := startOperatorVarnishd(t, image, &vinylv1.VinylCache{})

		assert.Contains(t, varnishadm(t, id, "storage.list"), "storage.s0",
			"without spec.storage, varnishd creates its default storage")

		listeners := varnishadm(t, id, "debug.listen_address")
		assert.Contains(t, listeners, "http 0.0.0.0 8080")
		assert.NotContains(t, listeners, "proxy",
			"no PROXY listener unless spec.proxyProtocol is enabled")
	})
}

// startOperatorVarnishd runs varnishd with varnishdArgs(vc) and the pod's
// mounts, waits until its admin CLI answers and returns the container id.
func startOperatorVarnishd(t *testing.T, image string, vc *vinylv1.VinylCache) string {
	t.Helper()
	dir := t.TempDir()
	vclPath := filepath.Join(dir, "default.vcl")
	secretPath := filepath.Join(dir, "secret")
	require.NoError(t, os.WriteFile(vclPath, []byte(bootstrapVCL), 0o644))
	require.NoError(t, os.WriteFile(secretPath, []byte("integration-test-secret\n"), 0o644))
	// The container runs as UID 65532, so the files must be world readable.
	require.NoError(t, os.Chmod(dir, 0o755))

	// The emptyDirs of the pod are exec-able; docker tmpfs is noexec unless told.
	runArgs := []string{"run", "-d", "--rm",
		"--user", "65532:65532", "--read-only",
		"--tmpfs", varnishWorkdirMountPath + ":exec,uid=65532,gid=65532",
		"--tmpfs", "/tmp:exec,uid=65532,gid=65532",
		"-v", vclPath + ":" + bootstrapVCLPath + ":ro",
		"-v", secretPath + ":" + varnishSecretPath + ":ro",
		"--entrypoint", "varnishd",
		image,
	}
	out, err := docker(append(runArgs, varnishdArgs(vc)...)...).Output()
	require.NoError(t, err, "docker run: %s", stderrText(err))
	id := strings.TrimSpace(string(out))
	t.Cleanup(func() { _ = docker("rm", "-f", id).Run() })

	deadline := time.Now().Add(15 * time.Second)
	for {
		if docker("exec", id, "varnishadm", "-T", varnishAdminAddr, "-S", varnishSecretPath, "ping").Run() == nil {
			return id
		}
		if time.Now().After(deadline) {
			logs, _ := docker("logs", id).CombinedOutput()
			t.Fatalf("varnishd did not come up with args %q:\n%s", varnishdArgs(vc), logs)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func varnishadm(t *testing.T, id string, command ...string) string {
	t.Helper()
	args := append([]string{"exec", id, "varnishadm", "-T", varnishAdminAddr, "-S", varnishSecretPath}, command...)
	out, err := docker(args...).Output()
	require.NoError(t, err, "varnishadm %v: %s", command, stderrText(err))
	return string(out)
}

func stderrText(err error) string {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return string(ee.Stderr)
	}
	return ""
}
