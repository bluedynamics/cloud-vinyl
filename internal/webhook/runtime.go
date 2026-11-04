package webhook

import (
	"fmt"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	vinylv1 "github.com/bluedynamics/cloud-vinyl/api/v1"
	"github.com/bluedynamics/cloud-vinyl/internal/catalog"
)

// ValidateRuntime checks spec.flavor and spec.version against the runtime
// catalog and warns about image overrides that look wrong. old is nil on
// create.
//
// The catalog check only runs on create or when flavor or version changed.
// Removing the finalizer on delete is an update through this webhook; had it
// been rejected for an unchanged runtime whose catalog entry vanished, the
// cache could never be deleted. A nil catalog disables the check.
func ValidateRuntime(old, vc *vinylv1.VinylCache, cat *catalog.Catalog) (admission.Warnings, []string) {
	if cat == nil {
		return nil, nil
	}
	var warnings admission.Warnings
	var errs []string

	flavor := effectiveFlavor(vc.Spec.Flavor)
	changed := old == nil ||
		effectiveFlavor(old.Spec.Flavor) != flavor ||
		old.Spec.Version != vc.Spec.Version
	if changed {
		if _, err := cat.Resolve(flavor, vc.Spec.Version); err != nil {
			errs = append(errs, err.Error())
		}
	}

	if vc.Spec.Image != "" && vc.Spec.Version == "" {
		if rt, err := cat.Resolve(flavor, ""); err == nil {
			warnings = append(warnings, fmt.Sprintf(
				"spec.image is set without spec.version: the exporter and tracing sidecars follow the "+
					"catalog default version %s, which may not match the image you run. "+
					"Set spec.version to one of the versions the catalog has for flavor %s (%s) so the sidecars match; "+
					"a version the catalog does not list is rejected and needs a catalog entry in the chart values (runtimes)",
				rt.Version, flavor, strings.Join(cat.Versions(flavor), ", ")))
		}
	}

	if vc.Spec.Image != "" {
		repo := imageRepoName(vc.Spec.Image)
		switch {
		case flavor == vinylv1.FlavorVinyl && strings.Contains(repo, "varnish"):
			warnings = append(warnings, fmt.Sprintf(
				"spec.image %q looks like a Varnish image but spec.flavor is vinyl: the pod will not "+
					"start because the vinyl sidecars expect vinyld. Use a Vinyl Cache image or set spec.flavor to varnish",
				vc.Spec.Image))
		case flavor == vinylv1.FlavorVarnish && strings.Contains(repo, "vinyl"):
			warnings = append(warnings, fmt.Sprintf(
				"spec.image %q looks like a Vinyl Cache image but spec.flavor is varnish: the pod will not "+
					"start because the varnish sidecars expect varnishd. Use a Varnish image or set spec.flavor to vinyl",
				vc.Spec.Image))
		}
	}
	if w := exporterLineageWarning(flavor, vc.Spec.Monitoring.Exporter); w != "" {
		warnings = append(warnings, w)
	}
	return warnings, errs
}

// exporterLineageWarning warns when spec.monitoring.exporter.image pins an
// exporter of the other lineage. The reference is built as the controller
// does (repository:tag, tag defaulting to latest). Both lineages publish
// under the repository name varnish-exporter, so the lineage lives in the tag
// (1.8.3-varnish9.1.0 or 1.8.3-vinyl9.1.0). The rule looks at the last
// repository segment plus the tag, lowercased; host and organisation are
// ignored. Flavor vinyl needs "vinyl" in that part, flavor varnish must not
// have it. A vinyl exporter in an image without vinylstat fails at start,
// because the operator passes -varnishstat-path /usr/bin/vinylstat.
func exporterLineageWarning(flavor vinylv1.Flavor, exp *vinylv1.ExporterSpec) string {
	if exp == nil || exp.Image.Repository == "" {
		return ""
	}
	tag := exp.Image.Tag
	if tag == "" {
		tag = "latest"
	}
	ref := exp.Image.Repository + ":" + tag
	hasVinyl := strings.Contains(imageRepoName(exp.Image.Repository)+":"+strings.ToLower(tag), "vinyl")
	switch {
	case flavor == vinylv1.FlavorVinyl && !hasVinyl:
		return fmt.Sprintf(
			"spec.monitoring.exporter.image %q looks like a Varnish exporter (neither its repository name nor its tag "+
				"mentions vinyl) but spec.flavor is vinyl: the exporter will not start because the vinyl pod has "+
				"vinylstat, not varnishstat. Use a vinyl exporter tag such as 1.8.3-vinyl9.1.0, or remove the override",
			ref)
	case flavor == vinylv1.FlavorVarnish && hasVinyl:
		return fmt.Sprintf(
			"spec.monitoring.exporter.image %q looks like a Vinyl Cache exporter (its repository name or tag "+
				"mentions vinyl) but spec.flavor is varnish: the exporter will not start because the varnish pod has "+
				"varnishstat, not vinylstat. Use a varnish exporter tag such as 1.8.3-varnish9.1.0, or remove the override",
			ref)
	}
	return ""
}

func effectiveFlavor(f vinylv1.Flavor) vinylv1.Flavor {
	if f == "" {
		return vinylv1.FlavorVarnish
	}
	return f
}

// imageRepoName returns the lowercased last path segment of the repository,
// without digest and tag. Host and organisation are ignored on purpose, so a
// mirror called varnish-mirror.example.com does not count as Varnish.
func imageRepoName(image string) string {
	if i := strings.Index(image, "@"); i >= 0 {
		image = image[:i]
	}
	seg := image[strings.LastIndex(image, "/")+1:]
	if i := strings.Index(seg, ":"); i >= 0 {
		seg = seg[:i]
	}
	return strings.ToLower(seg)
}
