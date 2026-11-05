package controller

import (
	vinylv1 "github.com/bluedynamics/cloud-vinyl/api/v1"
	"github.com/bluedynamics/cloud-vinyl/internal/catalog"
)

// flavorTraits are the per-flavor differences in how the pod is built. The
// binaries are renamed in Vinyl Cache; everything else (paths, workdir,
// container names) is identical across flavors.
type flavorTraits struct {
	// daemonCommand is the daemon binary the operator runs.
	daemonCommand string
	// varnishstatPath is passed to the exporter as -varnishstat-path.
	// Empty means the exporter's default.
	varnishstatPath string
	// varnishlogPath is the tracer's VARNISHLOG_PATH.
	varnishlogPath string
}

var flavorTraitsByFlavor = map[vinylv1.Flavor]flavorTraits{
	vinylv1.FlavorVarnish: {
		daemonCommand:  "varnishd",
		varnishlogPath: "varnishlog",
	},
	vinylv1.FlavorVinyl: {
		daemonCommand:   "vinyld",
		varnishstatPath: "/usr/bin/vinylstat",
		varnishlogPath:  "vinyllog",
	},
}

// traitsFor returns the traits of a flavor. An empty flavor means varnish, so
// objects that predate the flavor field behave as before.
func traitsFor(f vinylv1.Flavor) flavorTraits {
	if f == "" {
		f = vinylv1.FlavorVarnish
	}
	return flavorTraitsByFlavor[f]
}

// effectiveRuntime applies the per-cache image overrides on top of the
// catalog runtime: spec.image replaces the daemon image and
// spec.monitoring.exporter.image replaces the exporter image.
func effectiveRuntime(vc *vinylv1.VinylCache, rt catalog.Runtime) catalog.Runtime {
	if vc.Spec.Image != "" {
		rt.Daemon = vc.Spec.Image
	}
	if exp := vc.Spec.Monitoring.Exporter; exp != nil && exp.Image.Repository != "" {
		tag := exp.Image.Tag
		if tag == "" {
			tag = "latest"
		}
		rt.Exporter = exp.Image.Repository + ":" + tag
	}
	return rt
}
