package webhook

import (
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	vinylv1 "github.com/bluedynamics/cloud-vinyl/api/v1"
	"github.com/bluedynamics/cloud-vinyl/internal/catalog"
)

const testRuntimeCatalog = `
varnish:
  default: "9.1.0"
  versions:
    "9.1.0":
      daemon: docker.io/library/varnish:9.1.0
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

func testRuntimeCat(t *testing.T) *catalog.Catalog {
	t.Helper()
	c, err := catalog.Parse([]byte(testRuntimeCatalog))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return c
}

func rtVC(f vinylv1.Flavor, version, image string) *vinylv1.VinylCache {
	return &vinylv1.VinylCache{Spec: vinylv1.VinylCacheSpec{Flavor: f, Version: version, Image: image}}
}

func TestValidateRuntime_UnknownVersionRejectedOnCreate(t *testing.T) {
	_, errs := ValidateRuntime(nil, rtVC(vinylv1.FlavorVinyl, "9.0.0", ""), testRuntimeCat(t))
	got := strings.Join(errs, ";")
	for _, want := range []string{`flavor "vinyl" version "9.0.0" is not in the runtime catalog`, "9.1.0"} {
		if !strings.Contains(got, want) {
			t.Errorf("error %q lacks %q", got, want)
		}
	}
}

func TestValidateRuntime_UnchangedRuntimeAllowedOnUpdate(t *testing.T) {
	cat := testRuntimeCat(t)
	old := rtVC(vinylv1.FlavorVinyl, "8.0.0", "")
	nw := old.DeepCopy()
	nw.Labels = map[string]string{"a": "b"}
	if _, errs := ValidateRuntime(old, nw, cat); len(errs) != 0 {
		t.Errorf("label change rejected: %v", errs)
	}
	// Finalizer removal during deletion (Review Focus 1).
	old.Finalizers = []string{"vinyl.bluedynamics.eu/finalizer"}
	now := metav1.Now()
	old.DeletionTimestamp = &now
	nw = old.DeepCopy()
	nw.Finalizers = nil
	if _, errs := ValidateRuntime(old, nw, cat); len(errs) != 0 {
		t.Errorf("finalizer removal rejected: %v", errs)
	}
	// Empty flavor equals varnish.
	o2 := rtVC("", "8.0.0", "")
	n2 := rtVC(vinylv1.FlavorVarnish, "8.0.0", "")
	if _, errs := ValidateRuntime(o2, n2, cat); len(errs) != 0 {
		t.Errorf("empty vs varnish treated as change: %v", errs)
	}
}

func TestValidateRuntime_ChangedVersionRejectedOnUpdate(t *testing.T) {
	cat := testRuntimeCat(t)
	old := rtVC(vinylv1.FlavorVinyl, "8.0.0", "")
	nw := rtVC(vinylv1.FlavorVinyl, "9.0.0", "")
	if _, errs := ValidateRuntime(old, nw, cat); len(errs) == 0 {
		t.Error("changed unknown version admitted")
	}
	nw = rtVC(vinylv1.FlavorVinyl, "9.1.0", "")
	if _, errs := ValidateRuntime(old, nw, cat); len(errs) != 0 {
		t.Errorf("move to a catalog version rejected: %v", errs)
	}
}

func TestValidateRuntime_ImageWithoutVersionWarns(t *testing.T) {
	cat := testRuntimeCat(t)
	w, errs := ValidateRuntime(nil, rtVC(vinylv1.FlavorVarnish, "", "docker.io/library/varnish:9.1.0"), cat)
	if len(errs) != 0 || len(w) != 1 {
		t.Fatalf("errs=%v warnings=%v", errs, w)
	}
	for _, want := range []string{"spec.version", "9.1.0", "catalog entry", "chart values"} {
		if !strings.Contains(w[0], want) {
			t.Errorf("warning %q lacks %q", w[0], want)
		}
	}
	if w, _ := ValidateRuntime(nil, rtVC(vinylv1.FlavorVarnish, "9.1.0", "docker.io/library/varnish:9.1.0"), cat); len(w) != 0 {
		t.Errorf("version set still warns: %v", w)
	}
	if w, _ := ValidateRuntime(nil, rtVC(vinylv1.FlavorVarnish, "", ""), cat); len(w) != 0 {
		t.Errorf("no image warns: %v", w)
	}
}

func TestValidateRuntime_LineageMismatchWarns(t *testing.T) {
	cat := testRuntimeCat(t)
	tests := []struct {
		name   string
		flavor vinylv1.Flavor
		image  string
		warn   bool
	}{
		{"vinyl with varnish image", vinylv1.FlavorVinyl, "varnish:9.1.0", true},
		{"varnish with vinyl image", vinylv1.FlavorVarnish, "ghcr.io/bluedynamics/vinyl-cache:9.1.0-r1", true},
		{"vinyl with vinyl image", vinylv1.FlavorVinyl, "ghcr.io/bluedynamics/vinyl-cache:9.1.0-r1", false},
		{"mirror host and org ignored", vinylv1.FlavorVinyl, "varnish-mirror.example.com/team/vinyl-cache:1", false},
		{"digest-only varnish with vinyl", vinylv1.FlavorVinyl, "ghcr.io/x/varnish@sha256:" + strings.Repeat("a", 64), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// version set so only the lineage warning can appear
			w, errs := ValidateRuntime(nil, rtVC(tt.flavor, "9.1.0", tt.image), cat)
			if len(errs) != 0 {
				t.Fatalf("errs: %v", errs)
			}
			if (len(w) == 1) != tt.warn || len(w) > 1 {
				t.Errorf("warnings=%v want warn=%v", w, tt.warn)
			}
		})
	}
}

func TestValidateRuntime_LegacyCacheAdmitted(t *testing.T) {
	w, errs := ValidateRuntime(nil, rtVC("", "", "docker.io/library/varnish:9.1.0"), testRuntimeCat(t))
	if len(errs) != 0 {
		t.Fatalf("errs: %v", errs)
	}
	if len(w) != 1 || !strings.Contains(w[0], "spec.version") {
		t.Errorf("warnings=%v", w)
	}
}

func TestValidateRuntime_ExporterLineageWarns(t *testing.T) {
	cat := testRuntimeCat(t)
	tests := []struct {
		name   string
		flavor vinylv1.Flavor
		exp    *vinylv1.ExporterSpec
		warn   bool
	}{
		{"vinyl with varnish tag", vinylv1.FlavorVinyl, expSpec("ghcr.io/bluedynamics/varnish-exporter", "1.8.3-varnish9.1.0"), true},
		{"vinyl with vinyl tag", vinylv1.FlavorVinyl, expSpec("ghcr.io/bluedynamics/varnish-exporter", "1.8.3-vinyl9.1.0"), false},
		{"varnish with varnish tag", vinylv1.FlavorVarnish, expSpec("ghcr.io/bluedynamics/varnish-exporter", "1.8.3-varnish9.1.0"), false},
		{"varnish with vinyl tag", vinylv1.FlavorVarnish, expSpec("ghcr.io/bluedynamics/varnish-exporter", "1.8.3-vinyl9.1.0"), true},
		{"empty flavor is varnish", "", expSpec("ghcr.io/bluedynamics/varnish-exporter", "1.8.3-vinyl9.1.0"), true},
		{"vinyl with vinyl repository", vinylv1.FlavorVinyl, expSpec("example.org/vinyl-exporter", "1.8.3"), false},
		{"vinyl without tag defaults to latest", vinylv1.FlavorVinyl, expSpec("example.org/varnish-exporter", ""), true},
		{"org called vinyl is ignored", vinylv1.FlavorVarnish, expSpec("example.org/vinyl/varnish-exporter", "1.8.3"), false},
		{"no override", vinylv1.FlavorVinyl, &vinylv1.ExporterSpec{Enabled: true}, false},
		{"no exporter", vinylv1.FlavorVinyl, nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vc := rtVC(tt.flavor, "9.1.0", "")
			vc.Spec.Monitoring.Exporter = tt.exp
			w, errs := ValidateRuntime(nil, vc, cat)
			if len(errs) != 0 {
				t.Fatalf("errs: %v", errs)
			}
			if (len(w) == 1) != tt.warn || len(w) > 1 {
				t.Fatalf("warnings=%v want warn=%v", w, tt.warn)
			}
			if tt.warn && !strings.Contains(w[0], "spec.monitoring.exporter.image") {
				t.Errorf("warning %q", w[0])
			}
		})
	}
}

func expSpec(repo, tag string) *vinylv1.ExporterSpec {
	return &vinylv1.ExporterSpec{Enabled: true, Image: vinylv1.ExporterImageSpec{Repository: repo, Tag: tag}}
}
