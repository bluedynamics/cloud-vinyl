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

package v1

import (
	"context"
	"fmt"
	"strings"

	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	vinylv1 "github.com/bluedynamics/cloud-vinyl/api/v1"
	"github.com/bluedynamics/cloud-vinyl/internal/catalog"
	internalwebhook "github.com/bluedynamics/cloud-vinyl/internal/webhook"
)

// nolint:unused
// log is for logging in this package.
var vinylcachelog = logf.Log.WithName("vinylcache-resource")

// SetupVinylCacheWebhookWithManager registers the webhook for VinylCache in the manager.
// cat is the runtime catalog flavor and version are validated against.
func SetupVinylCacheWebhookWithManager(mgr ctrl.Manager, cat *catalog.Catalog) error {
	return ctrl.NewWebhookManagedBy(mgr, &vinylv1.VinylCache{}).
		WithValidator(&VinylCacheCustomValidator{Catalog: cat}).
		WithDefaulter(&VinylCacheCustomDefaulter{}).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-vinyl-bluedynamics-eu-v1-vinylcache,mutating=true,failurePolicy=fail,sideEffects=None,groups=vinyl.bluedynamics.eu,resources=vinylcaches,verbs=create;update,versions=v1,name=mvinylcache-v1.kb.io,admissionReviewVersions=v1,matchPolicy=Equivalent

// VinylCacheCustomDefaulter struct is responsible for setting default values on the custom resource of the
// Kind VinylCache when those are created or updated.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as it is used only for temporary operations and does not need to be deeply copied.
type VinylCacheCustomDefaulter struct{}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the Kind VinylCache.
func (d *VinylCacheCustomDefaulter) Default(_ context.Context, obj *vinylv1.VinylCache) error {
	vinylcachelog.Info("Defaulting for VinylCache", "name", obj.GetName())
	internalwebhook.DefaultVinylCache(obj)
	return nil
}

// TODO(user): change verbs to "verbs=create;update;delete" if you want to enable deletion validation.
// NOTE: If you want to customise the 'path', use the flags '--defaulting-path' or '--validation-path'.
// +kubebuilder:webhook:path=/validate-vinyl-bluedynamics-eu-v1-vinylcache,mutating=false,failurePolicy=fail,sideEffects=None,groups=vinyl.bluedynamics.eu,resources=vinylcaches,verbs=create;update,versions=v1,name=vvinylcache-v1.kb.io,admissionReviewVersions=v1,matchPolicy=Equivalent

// VinylCacheCustomValidator struct is responsible for validating the VinylCache resource
// when it is created, updated, or deleted.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as this struct is used only for temporary operations and does not need to be deeply copied.
type VinylCacheCustomValidator struct {
	// Catalog is the runtime catalog; nil skips the flavor/version check.
	Catalog *catalog.Catalog
}

// withRuntime merges the runtime catalog rules into the results of the
// general validation.
func (v *VinylCacheCustomValidator) withRuntime(old, vc *vinylv1.VinylCache, warnings admission.Warnings, err error) (admission.Warnings, error) {
	rtWarnings, rtErrs := internalwebhook.ValidateRuntime(old, vc, v.Catalog)
	warnings = append(warnings, rtWarnings...)
	if len(rtErrs) == 0 {
		return warnings, err
	}
	if err != nil {
		rtErrs = append([]string{err.Error()}, rtErrs...)
	}
	return warnings, fmt.Errorf("%s", strings.Join(rtErrs, "; "))
}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type VinylCache.
func (v *VinylCacheCustomValidator) ValidateCreate(_ context.Context, obj *vinylv1.VinylCache) (admission.Warnings, error) {
	vinylcachelog.Info("Validation for VinylCache upon creation", "name", obj.GetName())
	warnings, err := internalwebhook.ValidateVinylCache(obj)
	return v.withRuntime(nil, obj, warnings, err)
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type VinylCache.
func (v *VinylCacheCustomValidator) ValidateUpdate(_ context.Context, oldObj, newObj *vinylv1.VinylCache) (admission.Warnings, error) {
	vinylcachelog.Info("Validation for VinylCache upon update", "name", newObj.GetName())
	warnings, err := internalwebhook.ValidateVinylCacheUpdate(oldObj, newObj)
	return v.withRuntime(oldObj, newObj, warnings, err)
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type VinylCache.
func (v *VinylCacheCustomValidator) ValidateDelete(_ context.Context, obj *vinylv1.VinylCache) (admission.Warnings, error) {
	vinylcachelog.Info("Validation for VinylCache upon deletion", "name", obj.GetName())
	return nil, nil
}
