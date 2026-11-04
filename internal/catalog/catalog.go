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

// Package catalog holds the runtime catalog: the mapping from cache flavor
// and version to the daemon, exporter and tracer images. The chart ships it
// as a YAML file, the operator parses it once at startup, and the controller
// and the webhook resolve every VinylCache against it, so both always agree
// on what a (flavor, version) pair means.
package catalog

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"unicode"

	"sigs.k8s.io/yaml"

	vinylv1 "github.com/bluedynamics/cloud-vinyl/api/v1"
)

// ErrNotInCatalog is wrapped by Resolve when the requested flavor or version
// has no catalog entry. Callers use errors.Is to tell it from a broken catalog.
var ErrNotInCatalog = errors.New("not in the runtime catalog")

// Images are the container images that make up one runtime.
type Images struct {
	Daemon   string `json:"daemon"`
	Exporter string `json:"exporter"`
	Tracer   string `json:"tracer"`
}

// Runtime is a fully resolved catalog entry. Version is never empty, even
// when the caller asked for the flavor's default.
type Runtime struct {
	Flavor  vinylv1.Flavor
	Version string
	Images
}

// flavorEntry is one flavor's section of the catalog file.
type flavorEntry struct {
	Default  string            `json:"default"`
	Versions map[string]Images `json:"versions"`
}

// Catalog is a parsed and validated runtime catalog. It is immutable after
// Parse and therefore safe for concurrent use.
type Catalog struct {
	flavors map[vinylv1.Flavor]flavorEntry
}

// Parse decodes and validates a catalog document. Decoding is strict so a
// typo in the chart template fails the operator at startup instead of
// silently dropping an image.
func Parse(data []byte) (*Catalog, error) {
	var raw map[string]flavorEntry
	// Version keys such as 9.1 are floats to YAML; sigs.k8s.io/yaml converts
	// via JSON, which makes every map key a string again.
	if err := yaml.UnmarshalStrict(data, &raw); err != nil {
		return nil, fmt.Errorf("parsing runtime catalog: %w", err)
	}
	if len(raw) == 0 {
		return nil, errors.New("runtime catalog is empty: define at least one flavor")
	}

	c := &Catalog{flavors: make(map[vinylv1.Flavor]flavorEntry, len(raw))}
	for key, entry := range raw {
		f := vinylv1.Flavor(key)
		if f != vinylv1.FlavorVarnish && f != vinylv1.FlavorVinyl {
			return nil, fmt.Errorf("runtime catalog: unknown flavor %q (want %q or %q)",
				key, vinylv1.FlavorVarnish, vinylv1.FlavorVinyl)
		}
		if err := validateEntry(key, entry); err != nil {
			return nil, err
		}
		c.flavors[f] = entry
	}
	return c, nil
}

func validateEntry(flavor string, e flavorEntry) error {
	if len(e.Versions) == 0 {
		return fmt.Errorf("runtime catalog: flavor %q has no versions", flavor)
	}
	if e.Default == "" {
		return fmt.Errorf("runtime catalog: flavor %q has no default version", flavor)
	}
	if _, ok := e.Versions[e.Default]; !ok {
		return fmt.Errorf("runtime catalog: flavor %q: default %q is not among its versions %v",
			flavor, e.Default, sortedKeys(e.Versions))
	}
	for version, img := range e.Versions {
		for _, f := range []struct{ name, value string }{
			{"daemon", img.Daemon}, {"exporter", img.Exporter}, {"tracer", img.Tracer},
		} {
			if f.value == "" {
				return fmt.Errorf("runtime catalog: flavor %q version %q: %s image is empty",
					flavor, version, f.name)
			}
			if strings.IndexFunc(f.value, unicode.IsSpace) >= 0 {
				return fmt.Errorf("runtime catalog: flavor %q version %q: %s image %q contains whitespace",
					flavor, version, f.name, f.value)
			}
		}
	}
	return nil
}

// Load reads and parses the catalog file at path.
func Load(path string) (*Catalog, error) {
	data, err := os.ReadFile(path) //nolint:gosec // path is operator configuration, not user input
	if err != nil {
		return nil, fmt.Errorf("reading runtime catalog %s: %w", path, err)
	}
	c, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("runtime catalog %s: %w", path, err)
	}
	return c, nil
}

// Resolve looks up the runtime for a flavor and version. An empty flavor means
// varnish, so objects that predate the flavor field behave as before; an empty
// version means the flavor's default. Errors wrap ErrNotInCatalog and list
// what is available, because they end up in pod logs and webhook rejections.
func (c *Catalog) Resolve(f vinylv1.Flavor, version string) (Runtime, error) {
	if f == "" {
		f = vinylv1.FlavorVarnish
	}
	entry, ok := c.flavors[f]
	if !ok {
		return Runtime{}, fmt.Errorf("flavor %q is %w (flavors available: %v)",
			f, ErrNotInCatalog, c.flavorNames())
	}
	if version == "" {
		version = entry.Default
	}
	img, ok := entry.Versions[version]
	if !ok {
		return Runtime{}, fmt.Errorf("flavor %q version %q is %w (versions available: %v)",
			f, version, ErrNotInCatalog, sortedKeys(entry.Versions))
	}
	return Runtime{Flavor: f, Version: version, Images: img}, nil
}

// Versions returns the versions available for a flavor, sorted. An empty
// flavor means varnish; a flavor absent from the catalog yields nil.
func (c *Catalog) Versions(f vinylv1.Flavor) []string {
	if f == "" {
		f = vinylv1.FlavorVarnish
	}
	entry, ok := c.flavors[f]
	if !ok {
		return nil
	}
	return sortedKeys(entry.Versions)
}

func (c *Catalog) flavorNames() []string {
	names := make([]string, 0, len(c.flavors))
	for f := range c.flavors {
		names = append(names, string(f))
	}
	sort.Strings(names)
	return names
}

func sortedKeys(m map[string]Images) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
