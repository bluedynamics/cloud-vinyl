package controller

import (
	"testing"

	"github.com/stretchr/testify/assert"

	vinylv1 "github.com/bluedynamics/cloud-vinyl/api/v1"
	"github.com/bluedynamics/cloud-vinyl/internal/catalog"
)

func TestTraitsFor(t *testing.T) {
	assert.Equal(t, flavorTraits{daemonCommand: "varnishd", varnishlogPath: "varnishlog"},
		traitsFor(vinylv1.FlavorVarnish))
	assert.Equal(t, flavorTraits{daemonCommand: "vinyld", varnishstatPath: "/usr/bin/vinylstat", varnishlogPath: "vinyllog"},
		traitsFor(vinylv1.FlavorVinyl))
	assert.Equal(t, traitsFor(vinylv1.FlavorVarnish), traitsFor(""), "empty flavor means varnish")
}

func TestEffectiveRuntime(t *testing.T) {
	base := catalog.Runtime{
		Flavor: vinylv1.FlavorVinyl, Version: "9.1.0",
		Images: catalog.Images{Daemon: "d:1", Exporter: "e:1", Tracer: "t:1"},
	}
	tests := []struct {
		name string
		spec vinylv1.VinylCacheSpec
		want catalog.Images
	}{
		{"no overrides", vinylv1.VinylCacheSpec{}, base.Images},
		{"image only", vinylv1.VinylCacheSpec{Image: "my/d:2"},
			catalog.Images{Daemon: "my/d:2", Exporter: "e:1", Tracer: "t:1"}},
		{"exporter only", vinylv1.VinylCacheSpec{Monitoring: vinylv1.MonitoringSpec{
			Exporter: &vinylv1.ExporterSpec{Image: vinylv1.ExporterImageSpec{Repository: "my/e", Tag: "3"}}}},
			catalog.Images{Daemon: "d:1", Exporter: "my/e:3", Tracer: "t:1"}},
		{"exporter tag defaults to latest", vinylv1.VinylCacheSpec{Monitoring: vinylv1.MonitoringSpec{
			Exporter: &vinylv1.ExporterSpec{Image: vinylv1.ExporterImageSpec{Repository: "my/e"}}}},
			catalog.Images{Daemon: "d:1", Exporter: "my/e:latest", Tracer: "t:1"}},
		{"both", vinylv1.VinylCacheSpec{Image: "my/d:2", Monitoring: vinylv1.MonitoringSpec{
			Exporter: &vinylv1.ExporterSpec{Image: vinylv1.ExporterImageSpec{Repository: "my/e", Tag: "3"}}}},
			catalog.Images{Daemon: "my/d:2", Exporter: "my/e:3", Tracer: "t:1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := effectiveRuntime(&vinylv1.VinylCache{Spec: tt.spec}, base)
			assert.Equal(t, tt.want, got.Images)
			assert.Equal(t, base.Flavor, got.Flavor)
			assert.Equal(t, base.Version, got.Version)
		})
	}
}
