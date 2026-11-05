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

package catalog

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	vinylv1 "github.com/bluedynamics/cloud-vinyl/api/v1"
)

const validCatalog = `
varnish:
  default: "9.1.0"
  versions:
    "9.1.0":
      daemon: docker.io/library/varnish:9.1.0
      exporter: ghcr.io/bluedynamics/varnish-exporter:1.8.3
      tracer: ghcr.io/bluedynamics/cloud-vinyl-tracer:1.0.0
    "8.0.0":
      daemon: docker.io/library/varnish:8.0.0
      exporter: ghcr.io/bluedynamics/varnish-exporter:1.8.3
      tracer: ghcr.io/bluedynamics/cloud-vinyl-tracer:1.0.0
vinyl:
  default: "9.1.0"
  versions:
    "9.1.0":
      daemon: ghcr.io/bluedynamics/vinyl-cache:9.1.0-r1
      exporter: ghcr.io/bluedynamics/varnish-exporter:1.8.3-vinyl9.1.0
      tracer: ghcr.io/bluedynamics/cloud-vinyl-tracer:1.0.0-vinyl
`

func mustParse(t *testing.T, doc string) *Catalog {
	t.Helper()
	c, err := Parse([]byte(doc))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return c
}

func TestParse_Valid(t *testing.T) {
	c := mustParse(t, validCatalog)
	rt, err := c.Resolve(vinylv1.FlavorVinyl, "9.1.0")
	if err != nil {
		t.Fatal(err)
	}
	want := Runtime{
		Flavor:  vinylv1.FlavorVinyl,
		Version: "9.1.0",
		Images: Images{
			Daemon:   "ghcr.io/bluedynamics/vinyl-cache:9.1.0-r1",
			Exporter: "ghcr.io/bluedynamics/varnish-exporter:1.8.3-vinyl9.1.0",
			Tracer:   "ghcr.io/bluedynamics/cloud-vinyl-tracer:1.0.0-vinyl",
		},
	}
	if rt != want {
		t.Errorf("got %+v, want %+v", rt, want)
	}
}

func TestParse_UnquotedVersionKey(t *testing.T) {
	// 9.1.0 is not a YAML float, but 9.1 would be; both must stay strings.
	doc := `
varnish:
  default: "9.1"
  versions:
    9.1:
      {daemon: a, exporter: b, tracer: c}
    9.1.0:
      {daemon: a, exporter: b, tracer: c}
`
	c := mustParse(t, doc)
	if got := strings.Join(c.Versions(vinylv1.FlavorVarnish), ","); got != "9.1,9.1.0" {
		t.Errorf("versions = %q", got)
	}
}

func TestResolve_EmptyVersionUsesDefault(t *testing.T) {
	rt, err := mustParse(t, validCatalog).Resolve(vinylv1.FlavorVarnish, "")
	if err != nil {
		t.Fatal(err)
	}
	if rt.Version != "9.1.0" {
		t.Errorf("Version = %q, want 9.1.0", rt.Version)
	}
}

func TestResolve_EmptyFlavorIsVarnish(t *testing.T) {
	c := mustParse(t, validCatalog)
	got, err := c.Resolve("", "")
	if err != nil {
		t.Fatal(err)
	}
	want, _ := c.Resolve(vinylv1.FlavorVarnish, "")
	if got != want || got.Flavor != vinylv1.FlavorVarnish {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestResolve_Unknown(t *testing.T) {
	c := mustParse(t, validCatalog)
	_, err := c.Resolve(vinylv1.FlavorVarnish, "7.0.0")
	if !errors.Is(err, ErrNotInCatalog) {
		t.Fatalf("err = %v, want ErrNotInCatalog", err)
	}
	for _, s := range []string{"varnish", "7.0.0", "8.0.0", "9.1.0"} {
		if !strings.Contains(err.Error(), s) {
			t.Errorf("message %q lacks %q", err, s)
		}
	}

	vinylOnly := mustParse(t, `
vinyl:
  default: "9.1.0"
  versions:
    "9.1.0": {daemon: a, exporter: b, tracer: c}
`)
	_, err = vinylOnly.Resolve(vinylv1.FlavorVarnish, "")
	if !errors.Is(err, ErrNotInCatalog) {
		t.Fatalf("err = %v, want ErrNotInCatalog", err)
	}
	if !strings.Contains(err.Error(), "varnish") {
		t.Errorf("message %q does not name the flavor", err)
	}
	if got := vinylOnly.Versions(vinylv1.FlavorVarnish); len(got) != 0 {
		t.Errorf("Versions of absent flavor = %v", got)
	}
}

func TestVersions_Sorted(t *testing.T) {
	got := mustParse(t, validCatalog).Versions(vinylv1.FlavorVarnish)
	if strings.Join(got, ",") != "8.0.0,9.1.0" {
		t.Errorf("got %v", got)
	}
}

func TestParse_Rejects(t *testing.T) {
	entry := `{daemon: a, exporter: b, tracer: c}`
	tests := []struct {
		name, doc, want string
	}{
		{"unknown flavor", "bogus:\n  default: \"1\"\n  versions:\n    \"1\": " + entry, "bogus"},
		{"unknown field", "varnish:\n  default: \"1\"\n  extra: x\n  versions:\n    \"1\": " + entry, "extra"},
		{"unknown image field", "varnish:\n  default: \"1\"\n  versions:\n    \"1\": {daemon: a, exporter: b, tracer: c, sidecar: d}", "sidecar"},
		{"no versions", "varnish:\n  default: \"1\"\n  versions: {}", "no versions"},
		{"default missing", "varnish:\n  default: \"2\"\n  versions:\n    \"1\": " + entry, "default"},
		{"empty default", "varnish:\n  versions:\n    \"1\": " + entry, "default"},
		{"empty daemon", "varnish:\n  default: \"1\"\n  versions:\n    \"1\": {exporter: b, tracer: c}", "daemon"},
		{"empty exporter", "varnish:\n  default: \"1\"\n  versions:\n    \"1\": {daemon: a, tracer: c}", "exporter"},
		{"empty tracer", "varnish:\n  default: \"1\"\n  versions:\n    \"1\": {daemon: a, exporter: b}", "tracer"},
		{"whitespace in image", "varnish:\n  default: \"1\"\n  versions:\n    \"1\": {daemon: \"a b\", exporter: b, tracer: c}", "whitespace"},
		{"empty document", "", "empty"},
		{"only comments", "# nothing\n", "empty"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.doc))
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q does not contain %q", err, tt.want)
			}
		})
	}
}

func TestLoad(t *testing.T) {
	p := filepath.Join(t.TempDir(), "catalog.yaml")
	if err := os.WriteFile(p, []byte(validCatalog), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err != nil {
		t.Fatal(err)
	}
}

func TestLoad_MissingFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "nope.yaml")
	_, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), p) {
		t.Fatalf("err = %v, want it to name %s", err, p)
	}
}

func TestLoad_InvalidFileNamesPath(t *testing.T) {
	p := filepath.Join(t.TempDir(), "bad.yaml")
	if err := os.WriteFile(p, []byte("bogus: {}"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), p) {
		t.Fatalf("err = %v, want it to name %s", err, p)
	}
}
