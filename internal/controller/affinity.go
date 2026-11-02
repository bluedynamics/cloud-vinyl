package controller

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	vinylv1 "github.com/bluedynamics/cloud-vinyl/api/v1"
)

// AntiAffinityDefaults is the operator-wide pod anti-affinity applied to every
// VinylCache that does not set affinity.podAntiAffinity itself (#147). Zero
// fields fall back to BuiltinAntiAffinityDefaults.
type AntiAffinityDefaults struct {
	Type        string
	TopologyKey string
	Weight      int32
}

// BuiltinAntiAffinityDefaults spreads the pods of a VinylCache across nodes
// without ever leaving one Pending: on a small cluster with a node down, a
// degraded cache on the remaining node beats no cache.
var BuiltinAntiAffinityDefaults = AntiAffinityDefaults{
	Type:        vinylv1.PodAntiAffinityPreferred,
	TopologyKey: corev1.LabelHostname,
	Weight:      100,
}

// Validate rejects values the CRD would reject on a VinylCache, so a bad
// operator flag fails at startup instead of on every reconcile.
func (d AntiAffinityDefaults) Validate() error {
	switch d.Type {
	case "", vinylv1.PodAntiAffinityPreferred, vinylv1.PodAntiAffinityRequired, vinylv1.PodAntiAffinityNone:
	default:
		return fmt.Errorf("pod anti-affinity type %q must be preferred, required or none", d.Type)
	}
	if d.Weight < 0 || d.Weight > 100 {
		return fmt.Errorf("pod anti-affinity weight %d must be between 1 and 100", d.Weight)
	}
	return nil
}

// resolve layers the operator default and the VinylCache's own settings over
// the built-in defaults, field by field.
func (d AntiAffinityDefaults) resolve(spec *vinylv1.PodAntiAffinitySpec) AntiAffinityDefaults {
	out := BuiltinAntiAffinityDefaults
	for _, layer := range []AntiAffinityDefaults{d, fromSpec(spec)} {
		if layer.Type != "" {
			out.Type = layer.Type
		}
		if layer.TopologyKey != "" {
			out.TopologyKey = layer.TopologyKey
		}
		if layer.Weight != 0 {
			out.Weight = layer.Weight
		}
	}
	return out
}

func fromSpec(spec *vinylv1.PodAntiAffinitySpec) AntiAffinityDefaults {
	if spec == nil {
		return AntiAffinityDefaults{}
	}
	return AntiAffinityDefaults{Type: spec.Type, TopologyKey: spec.TopologyKey, Weight: spec.Weight}
}

// podAffinity returns the pod's affinity: spec.pod.affinity as given, with
// podAntiAffinity filled in when the user left it unset. The rule is set for
// every replica count, so scaling from one replica to two does not change the
// pod template and restart pod-0.
func podAffinity(vc *vinylv1.VinylCache, defaults AntiAffinityDefaults) *corev1.Affinity {
	user := vc.Spec.Pod.Affinity
	if user != nil && user.PodAntiAffinity != nil {
		return user
	}
	anti := podAntiAffinity(vc.Name, defaults.resolve(vc.Spec.Pod.PodAntiAffinity))
	if anti == nil {
		return user
	}
	out := &corev1.Affinity{}
	if user != nil {
		out = user.DeepCopy()
	}
	out.PodAntiAffinity = anti
	return out
}

// podAntiAffinity builds the rule for one VinylCache. It selects on the
// cache-name label rather than app, which other workloads in the namespace
// may carry too.
func podAntiAffinity(name string, cfg AntiAffinityDefaults) *corev1.PodAntiAffinity {
	term := corev1.PodAffinityTerm{
		TopologyKey: cfg.TopologyKey,
		LabelSelector: &metav1.LabelSelector{
			MatchLabels: map[string]string{labelVinylCacheName: name},
		},
	}
	switch cfg.Type {
	case vinylv1.PodAntiAffinityRequired:
		return &corev1.PodAntiAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{term},
		}
	case vinylv1.PodAntiAffinityPreferred:
		return &corev1.PodAntiAffinity{
			PreferredDuringSchedulingIgnoredDuringExecution: []corev1.WeightedPodAffinityTerm{
				{Weight: cfg.Weight, PodAffinityTerm: term},
			},
		}
	default:
		return nil
	}
}
