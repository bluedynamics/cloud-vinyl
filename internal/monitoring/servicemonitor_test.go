package monitoring_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/bluedynamics/cloud-vinyl/internal/monitoring"
)

func TestGenerateServiceMonitor_BasicFields(t *testing.T) {
	sm := monitoring.GenerateServiceMonitor("cloud-vinyl", "cloud-vinyl-system")
	assert.Equal(t, "cloud-vinyl-system", sm.Namespace)
	assert.Equal(t, "cloud-vinyl-metrics", sm.Name)
	assert.NotEmpty(t, sm.Spec.Endpoints)
	assert.Equal(t, "30s", sm.Spec.Endpoints[0].Interval)
}
