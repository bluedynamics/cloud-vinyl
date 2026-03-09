package monitoring

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// ServiceMonitor is a minimal representation of monitoring.coreos.com/v1 ServiceMonitor.
// We define our own struct to avoid the heavy prometheus-operator dependency.
// In production, convert this to the actual CRD type when applying to Kubernetes.
type ServiceMonitor struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              ServiceMonitorSpec `json:"spec"`
}

// ServiceMonitorSpec defines what the ServiceMonitor scrapes.
type ServiceMonitorSpec struct {
	Selector  metav1.LabelSelector `json:"selector"`
	Endpoints []Endpoint           `json:"endpoints"`
}

// Endpoint describes a single scrape endpoint.
type Endpoint struct {
	Port          string `json:"port"`
	Path          string `json:"path,omitempty"`
	Interval      string `json:"interval,omitempty"`
	ScrapeTimeout string `json:"scrapeTimeout,omitempty"`
}

// GenerateServiceMonitor creates a ServiceMonitor for the cloud-vinyl operator.
// name is the app name (used in label selectors and object name),
// namespace is the Kubernetes namespace for the object.
func GenerateServiceMonitor(name, namespace string) *ServiceMonitor {
	return &ServiceMonitor{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "monitoring.coreos.com/v1",
			Kind:       "ServiceMonitor",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name + "-metrics",
			Namespace: namespace,
			Labels: map[string]string{
				"app.kubernetes.io/name": name,
			},
		},
		Spec: ServiceMonitorSpec{
			Selector: metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app.kubernetes.io/name": name,
				},
			},
			Endpoints: []Endpoint{
				{
					Port:          "metrics",
					Path:          "/metrics",
					Interval:      "30s",
					ScrapeTimeout: "10s",
				},
			},
		},
	}
}
