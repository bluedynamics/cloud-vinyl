package controller

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/bluedynamics/cloud-vinyl/internal/generator"
)

// perPodAgent answers with a different active VCL name per pod IP.
type perPodAgent struct {
	names   map[string]string
	failFor map[string]bool
	queried []string
}

func (a *perPodAgent) PushVCL(_ context.Context, _, _, _, _ string) error { return nil }

func (a *perPodAgent) ActiveVCLName(_ context.Context, _, podIP string) (string, error) {
	a.queried = append(a.queried, podIP)
	if a.failFor[podIP] {
		return "", errors.New("connection refused")
	}
	return a.names[podIP], nil
}

func peer(name, ip string) generator.PeerBackend {
	return generator.PeerBackend{Name: name, IP: ip, Port: 80}
}

func TestObserveVCLNames_ReportsWhatEachAgentSays(t *testing.T) {
	agent := &perPodAgent{names: map[string]string{
		"10.0.0.1": "ns-cache-aaaa1111",
		"10.0.0.2": "ns-cache-bbbb2222",
	}}
	r := makeReconcilerWithMock(agent)

	got := r.observeVCLNames(context.Background(), makeVC(),
		[]generator.PeerBackend{peer("c_0", "10.0.0.1"), peer("c_1", "10.0.0.2")})

	assert.Equal(t, map[string]string{
		"10.0.0.1": "ns-cache-aaaa1111",
		"10.0.0.2": "ns-cache-bbbb2222",
	}, got)
}

// An unreachable agent must not be reported as "has the desired VCL". Mapping it
// to the empty string makes it a push target, and the push is idempotent, so a
// redundant push costs nothing while a skipped one stalls convergence.
func TestObserveVCLNames_UnreachableAgentYieldsEmptyName(t *testing.T) {
	agent := &perPodAgent{
		names:   map[string]string{"10.0.0.1": "ns-cache-aaaa1111"},
		failFor: map[string]bool{"10.0.0.2": true},
	}
	r := makeReconcilerWithMock(agent)

	got := r.observeVCLNames(context.Background(), makeVC(),
		[]generator.PeerBackend{peer("c_0", "10.0.0.1"), peer("c_1", "10.0.0.2")})

	assert.Equal(t, "ns-cache-aaaa1111", got["10.0.0.1"])
	assert.Equal(t, "", got["10.0.0.2"], "an unreachable pod must not look up to date")
}

func TestObserveVCLNames_NoPods_NoQueries(t *testing.T) {
	agent := &perPodAgent{}
	r := makeReconcilerWithMock(agent)

	got := r.observeVCLNames(context.Background(), makeVC(), nil)

	assert.Empty(t, got)
	assert.Empty(t, agent.queried)
}
