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

package v1alpha1

import (
	"context"

	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	vinylv1alpha1 "github.com/bluedynamics/cloud-vinyl/api/v1alpha1"
	internalwebhook "github.com/bluedynamics/cloud-vinyl/internal/webhook"
)

// nolint:unused
// log is for logging in this package.
var vinylcachelog = logf.Log.WithName("vinylcache-resource")

// SetupVinylCacheWebhookWithManager registers the webhook for VinylCache in the manager.
func SetupVinylCacheWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &vinylv1alpha1.VinylCache{}).
		WithValidator(&VinylCacheCustomValidator{}).
		WithDefaulter(&VinylCacheCustomDefaulter{}).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-vinyl-bluedynamics-eu-v1alpha1-vinylcache,mutating=true,failurePolicy=fail,sideEffects=None,groups=vinyl.bluedynamics.eu,resources=vinylcaches,verbs=create;update,versions=v1alpha1,name=mvinylcache-v1alpha1.kb.io,admissionReviewVersions=v1

// VinylCacheCustomDefaulter struct is responsible for setting default values on the custom resource of the
// Kind VinylCache when those are created or updated.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as it is used only for temporary operations and does not need to be deeply copied.
type VinylCacheCustomDefaulter struct{}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the Kind VinylCache.
func (d *VinylCacheCustomDefaulter) Default(_ context.Context, obj *vinylv1alpha1.VinylCache) error {
	vinylcachelog.Info("Defaulting for VinylCache", "name", obj.GetName())
	internalwebhook.DefaultVinylCache(obj)
	return nil
}

// TODO(user): change verbs to "verbs=create;update;delete" if you want to enable deletion validation.
// NOTE: If you want to customise the 'path', use the flags '--defaulting-path' or '--validation-path'.
// +kubebuilder:webhook:path=/validate-vinyl-bluedynamics-eu-v1alpha1-vinylcache,mutating=false,failurePolicy=fail,sideEffects=None,groups=vinyl.bluedynamics.eu,resources=vinylcaches,verbs=create;update,versions=v1alpha1,name=vvinylcache-v1alpha1.kb.io,admissionReviewVersions=v1

// VinylCacheCustomValidator struct is responsible for validating the VinylCache resource
// when it is created, updated, or deleted.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as this struct is used only for temporary operations and does not need to be deeply copied.
type VinylCacheCustomValidator struct{}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type VinylCache.
func (v *VinylCacheCustomValidator) ValidateCreate(_ context.Context, obj *vinylv1alpha1.VinylCache) (admission.Warnings, error) {
	vinylcachelog.Info("Validation for VinylCache upon creation", "name", obj.GetName())
	return internalwebhook.ValidateVinylCache(obj)
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type VinylCache.
func (v *VinylCacheCustomValidator) ValidateUpdate(_ context.Context, _, newObj *vinylv1alpha1.VinylCache) (admission.Warnings, error) {
	vinylcachelog.Info("Validation for VinylCache upon update", "name", newObj.GetName())
	return internalwebhook.ValidateVinylCache(newObj)
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type VinylCache.
func (v *VinylCacheCustomValidator) ValidateDelete(_ context.Context, obj *vinylv1alpha1.VinylCache) (admission.Warnings, error) {
	vinylcachelog.Info("Validation for VinylCache upon deletion", "name", obj.GetName())
	return nil, nil
}
