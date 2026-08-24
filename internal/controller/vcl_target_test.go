package controller

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/bluedynamics/cloud-vinyl/internal/generator"
)

func ipsOf(peers []generator.PeerBackend) []string {
	out := make([]string, 0, len(peers))
	for _, p := range peers {
		out = append(out, p.IP)
	}
	return out
}

func TestPodsNeedingVCL_SkipsPodsAlreadyCarryingIt(t *testing.T) {
	reachable := []generator.PeerBackend{peer("c_0", "10.0.0.1"), peer("c_1", "10.0.0.2")}
	observed := map[string]string{"10.0.0.1": "ns-cache-want1111", "10.0.0.2": "ns-cache-want1111"}

	assert.Empty(t, podsNeedingVCL(reachable, observed, "ns-cache-want1111"),
		"a converged cache must not push at all")
}

func TestPodsNeedingVCL_IncludesDivergingPod(t *testing.T) {
	reachable := []generator.PeerBackend{peer("c_0", "10.0.0.1"), peer("c_1", "10.0.0.2")}
	observed := map[string]string{"10.0.0.1": "ns-cache-want1111", "10.0.0.2": "ns-cache-old00000"}

	assert.Equal(t, []string{"10.0.0.2"}, ipsOf(podsNeedingVCL(reachable, observed, "ns-cache-want1111")))
}

// This is the failure that held PR #77 for a round: two further replicas come
// up after the first one converged. The desired VCL has not changed, and the
// old aggregate condition (hash equal, peer counts equal) therefore skipped the
// push entirely, leaving the new pods without VCL forever.
func TestPodsNeedingVCL_NewReplicasWhenDesiredVCLUnchanged(t *testing.T) {
	reachable := []generator.PeerBackend{
		peer("c_0", "10.0.0.1"), peer("c_1", "10.0.0.2"), peer("c_2", "10.0.0.3"),
	}
	observed := map[string]string{"10.0.0.1": "ns-cache-want1111"} // c_1 and c_2 never queried successfully

	assert.Equal(t, []string{"10.0.0.2", "10.0.0.3"},
		ipsOf(podsNeedingVCL(reachable, observed, "ns-cache-want1111")))
}

// A pod whose varnishd restarted is back on the bootstrap VCL. Its hash no
// longer matches, so it gets pushed again without any special handling.
func TestPodsNeedingVCL_RestartedPodIsPushedAgain(t *testing.T) {
	reachable := []generator.PeerBackend{peer("c_0", "10.0.0.1")}
	observed := map[string]string{"10.0.0.1": "boot"}

	assert.Equal(t, []string{"10.0.0.1"}, ipsOf(podsNeedingVCL(reachable, observed, "ns-cache-want1111")))
}
