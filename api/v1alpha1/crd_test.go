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

package v1alpha1_test

import (
	"os"
	"reflect"
	"testing"

	"sigs.k8s.io/yaml"
)

// TestCRDVersionsShareOneSchema guards the conversion strategy None. The API
// server converts between v1alpha1 and v1 by rewriting apiVersion only, which
// is lossless only while both versions have the same schema: a field that one
// version lacked would be pruned when read or written through it. v1alpha1's
// VinylCache wraps the v1 spec and status types, so the schemas match by
// construction; this test fails if that ever changes.
func TestCRDVersionsShareOneSchema(t *testing.T) {
	raw, err := os.ReadFile("../../config/crd/bases/vinyl.bluedynamics.eu_vinylcaches.yaml")
	if err != nil {
		t.Fatalf("read CRD: %v", err)
	}
	var crd struct {
		Spec struct {
			Conversion *struct {
				Strategy string `json:"strategy"`
			} `json:"conversion"`
			Versions []map[string]any `json:"versions"`
		} `json:"spec"`
	}
	if err := yaml.Unmarshal(raw, &crd); err != nil {
		t.Fatalf("parse CRD: %v", err)
	}
	if c := crd.Spec.Conversion; c != nil && c.Strategy != "None" {
		t.Fatalf("conversion strategy %q, want None", c.Strategy)
	}

	versions := map[string]map[string]any{}
	for _, v := range crd.Spec.Versions {
		versions[v["name"].(string)] = v
	}
	v1, alpha := versions["v1"], versions["v1alpha1"]
	if v1 == nil || alpha == nil {
		t.Fatalf("want versions v1 and v1alpha1, got %v", reflect.ValueOf(versions).MapKeys())
	}
	if v1["storage"] != true || alpha["storage"] != false {
		t.Errorf("v1 must be the storage version (v1 storage=%v, v1alpha1 storage=%v)", v1["storage"], alpha["storage"])
	}
	if alpha["served"] != true || alpha["deprecated"] != true {
		t.Errorf("v1alpha1 must stay served and be marked deprecated (served=%v, deprecated=%v)", alpha["served"], alpha["deprecated"])
	}
	for _, key := range []string{"schema", "subresources", "additionalPrinterColumns"} {
		if !reflect.DeepEqual(v1[key], alpha[key]) {
			t.Errorf("%s differs between v1 and v1alpha1", key)
		}
	}
}

// TestCRDFlavorDefaultsToVarnish pins the flavor field contract: it defaults
// to varnish, admits only varnish and vinyl, and spec.image is no longer
// required because the runtime catalog supplies it.
func TestCRDFlavorDefaultsToVarnish(t *testing.T) {
	raw, err := os.ReadFile("../../config/crd/bases/vinyl.bluedynamics.eu_vinylcaches.yaml")
	if err != nil {
		t.Fatalf("read CRD: %v", err)
	}
	var crd struct {
		Spec struct {
			Versions []struct {
				Name   string `json:"name"`
				Schema struct {
					OpenAPIV3Schema struct {
						Properties struct {
							Spec struct {
								Required   []string `json:"required"`
								Properties struct {
									Flavor struct {
										Default string   `json:"default"`
										Enum    []string `json:"enum"`
									} `json:"flavor"`
								} `json:"properties"`
							} `json:"spec"`
						} `json:"properties"`
					} `json:"openAPIV3Schema"`
				} `json:"schema"`
			} `json:"versions"`
		} `json:"spec"`
	}
	if err := yaml.Unmarshal(raw, &crd); err != nil {
		t.Fatalf("parse CRD: %v", err)
	}
	found := false
	for _, v := range crd.Spec.Versions {
		if v.Name != "v1" {
			continue
		}
		found = true
		spec := v.Schema.OpenAPIV3Schema.Properties.Spec
		if got := spec.Properties.Flavor.Default; got != "varnish" {
			t.Errorf("flavor default %q, want varnish", got)
		}
		if got := spec.Properties.Flavor.Enum; !reflect.DeepEqual(got, []string{"varnish", "vinyl"}) {
			t.Errorf("flavor enum %v, want [varnish vinyl]", got)
		}
		for _, r := range spec.Required {
			if r == "image" {
				t.Errorf("image must not be required, got required=%v", spec.Required)
			}
		}
	}
	if !found {
		t.Fatal("v1 version not found in CRD")
	}
}
