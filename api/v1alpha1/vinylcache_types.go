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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Phase constants represent the overall state of a VinylCache instance.
const (
	PhasePending  = "Pending"
	PhaseReady    = "Ready"
	PhaseDegraded = "Degraded"
	PhaseError    = "Error"
)

// Condition type constants for VinylCache status conditions.
const (
	ConditionReady             = "Ready"
	ConditionVCLSynced         = "VCLSynced"
	ConditionBackendsAvailable = "BackendsAvailable"
	ConditionProgressing       = "Progressing"
	ConditionVCLConsistent     = "VCLConsistent"
)

// VinylCacheSpec defines the desired state of VinylCache.
type VinylCacheSpec struct {
	// replicas is the desired number of Varnish pods in the cluster.
	// +kubebuilder:validation:Minimum=1
	Replicas int32 `json:"replicas"`

	// image is the container image for the Varnish pods.
	// +kubebuilder:validation:MinLength=1
	Image string `json:"image"`

	// backends defines the upstream services Varnish will proxy to.
	// At least one backend is required.
	// +kubebuilder:validation:MinItems=1
	Backends []BackendSpec `json:"backends"`

	// director configures how Varnish distributes requests across backend endpoints.
	// +optional
	Director DirectorSpec `json:"director,omitempty"`

	// cluster configures Varnish clustering between pods.
	// +optional
	Cluster ClusterSpec `json:"cluster,omitempty"`

	// varnishParameters are runtime parameters passed to varnishd via -p flags.
	// Certain security-sensitive parameters are blocked by the admission webhook.
	// +optional
	VarnishParams map[string]string `json:"varnishParameters,omitempty"`

	// storage configures one or more Varnish storage backends (malloc or file).
	// +optional
	Storage []StorageSpec `json:"storage,omitempty"`

	// vcl configures VCL generation: snippets injected into generated VCL subroutines,
	// or a full VCL override.
	// +optional
	VCL VCLSpec `json:"vcl,omitempty"`

	// invalidation configures cache invalidation methods (PURGE, BAN, xkey).
	// +optional
	Invalidation InvalidationSpec `json:"invalidation,omitempty"`

	// proxyProtocol enables PROXY protocol on a dedicated port for passing client IP.
	// +optional
	ProxyProtocol ProxyProtocolSpec `json:"proxyProtocol,omitempty"`

	// service configures the Kubernetes Service created for traffic ingress.
	// +optional
	Service ServiceSpec `json:"service,omitempty"`

	// debounce configures a grace period before applying endpoint changes to Varnish,
	// preventing thundering-herd on rapid endpoint churn.
	// +optional
	Debounce DebounceSpec `json:"debounce,omitempty"`

	// retry configures retry behaviour when VCL updates fail to apply.
	// +optional
	Retry RetrySpec `json:"retry,omitempty"`

	// pod configures pod-level scheduling and metadata.
	// +optional
	Pod PodSpec `json:"pod,omitempty"`

	// monitoring configures Prometheus metrics and alerting rules.
	// +optional
	Monitoring MonitoringSpec `json:"monitoring,omitempty"`

	// resources sets CPU and memory requests/limits for the Varnish container.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`
}

// BackendSpec describes an upstream Kubernetes service used as a Varnish backend.
type BackendSpec struct {
	// name is the VCL identifier for this backend. Must match ^[a-zA-Z][a-zA-Z0-9_]*$.
	// +kubebuilder:validation:Pattern=`^[a-zA-Z][a-zA-Z0-9_]*$`
	Name string `json:"name"`

	// serviceRef references the Kubernetes Service in the same namespace.
	ServiceRef ServiceRef `json:"serviceRef"`

	// weight is the relative weight for the director. 0 means standby (fallback director).
	// +optional
	// +kubebuilder:validation:Minimum=0
	Weight int32 `json:"weight,omitempty"`

	// port overrides the service port used for this backend.
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	Port int32 `json:"port,omitempty"`

	// probe configures the Varnish backend health probe.
	// +optional
	Probe *BackendProbeSpec `json:"probe,omitempty"`

	// connectionParameters configures backend connection pool timeouts and limits.
	// +optional
	ConnectionParameters *ConnectionParameters `json:"connectionParameters,omitempty"`
}

// ServiceRef references a Kubernetes Service by name in the same namespace.
// Cross-namespace references are not permitted (enforced by the admission webhook).
type ServiceRef struct {
	// name is the Kubernetes Service name.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// BackendProbeSpec configures the Varnish backend health probe.
type BackendProbeSpec struct {
	// url is the URL to probe. If empty, the backend's root path is used.
	// +optional
	URL string `json:"url,omitempty"`

	// interval is how often to probe the backend.
	// +optional
	Interval metav1.Duration `json:"interval,omitempty"`

	// timeout is the maximum time to wait for a probe response.
	// +optional
	Timeout metav1.Duration `json:"timeout,omitempty"`

	// window is the number of most recent probes to consider for threshold evaluation.
	// +optional
	// +kubebuilder:validation:Minimum=1
	Window int32 `json:"window,omitempty"`

	// threshold is the minimum number of successful probes within the window for the backend
	// to be considered healthy.
	// +optional
	// +kubebuilder:validation:Minimum=1
	Threshold int32 `json:"threshold,omitempty"`

	// expectedResponse is the expected HTTP response status code. Defaults to 200.
	// +optional
	ExpectedResponse *int32 `json:"expectedResponse,omitempty"`
}

// ConnectionParameters configures Varnish backend connection pool behaviour.
type ConnectionParameters struct {
	// connectTimeout is the maximum time to establish a TCP connection to the backend.
	// +optional
	ConnectTimeout metav1.Duration `json:"connectTimeout,omitempty"`

	// firstByteTimeout is the maximum time to wait for the first byte of the response.
	// +optional
	FirstByteTimeout metav1.Duration `json:"firstByteTimeout,omitempty"`

	// betweenBytesTimeout is the maximum time to wait between bytes of the response body.
	// +optional
	BetweenBytesTimeout metav1.Duration `json:"betweenBytesTimeout,omitempty"`

	// idleTimeout is the maximum time an idle connection may be reused. Should be less
	// than the backend's keep-alive timeout to avoid 503 races.
	// +optional
	IdleTimeout metav1.Duration `json:"idleTimeout,omitempty"`

	// maxConnections is the maximum number of concurrent connections to the backend.
	// +optional
	// +kubebuilder:validation:Minimum=1
	MaxConnections int32 `json:"maxConnections,omitempty"`
}

// DirectorSpec configures how Varnish distributes requests across backend endpoints.
type DirectorSpec struct {
	// type selects the Varnish director algorithm.
	// "shard" (default) provides consistent hashing; "round_robin", "random", and "hash"
	// are also supported.
	// +optional
	// +kubebuilder:validation:Enum=shard;round_robin;random;hash
	Type string `json:"type,omitempty"`

	// shard configures the shard director (consistent-hash). Only used when type is "shard".
	// +optional
	Shard *ShardSpec `json:"shard,omitempty"`

	// hash configures the hash director. Only used when type is "hash".
	// +optional
	Hash *HashSpec `json:"hash,omitempty"`
}

// ShardSpec configures the Varnish shard director for consistent hashing.
type ShardSpec struct {
	// warmup is the proportion of requests (0.0–1.0) sent to the alternative backend
	// to pre-populate its cache. Default: 0.1. Must be between 0.0 and 1.0.
	// +optional
	Warmup *float64 `json:"warmup,omitempty"`

	// rampup is the time after adding a backend before it receives its full share of traffic,
	// preventing thundering-herd. Default: 30s.
	// +optional
	Rampup metav1.Duration `json:"rampup,omitempty"`

	// replicas is the number of Ketama replicas per backend in the hash ring. Default: 67.
	// +optional
	// +kubebuilder:validation:Minimum=1
	Replicas int32 `json:"replicas,omitempty"`

	// by determines what value is hashed for shard selection. "HASH" uses the Varnish
	// hash (default); "URL" uses the request URL.
	// +optional
	// +kubebuilder:validation:Enum=HASH;URL
	By string `json:"by,omitempty"`

	// healthy controls which backends the director considers when selecting a shard.
	// "CHOSEN" (default) only considers the chosen backend healthy; "ALL" requires all
	// backends to be healthy.
	// +optional
	// +kubebuilder:validation:Enum=CHOSEN;ALL
	Healthy string `json:"healthy,omitempty"`
}

// HashSpec configures the Varnish hash director.
type HashSpec struct {
	// header is the request header name used as the hash key.
	// +optional
	Header string `json:"header,omitempty"`
}

// ClusterSpec configures Varnish clustering between pods.
type ClusterSpec struct {
	// enabled activates inter-pod clustering via a shard director.
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// peerRouting configures how requests are routed between Varnish peers.
	// +optional
	PeerRouting PeerRoutingSpec `json:"peerRouting,omitempty"`
}

// PeerRoutingSpec configures peer-to-peer request routing in a Varnish cluster.
type PeerRoutingSpec struct {
	// type is the routing strategy between Varnish pods. Only "shard" is supported.
	// +optional
	// +kubebuilder:validation:Enum=shard
	Type string `json:"type,omitempty"`
}

// VCLSpec configures VCL generation for the Varnish instance.
type VCLSpec struct {
	// snippets provides VCL code injected into each generated subroutine.
	// +optional
	Snippets VCLSnippets `json:"snippets,omitempty"`

	// fullOverride replaces the entire generated VCL with a custom VCL string.
	// Use with caution — this disables all structured configuration.
	// +optional
	FullOverride string `json:"fullOverride,omitempty"`
}

// VCLSnippets provides inline VCL code inserted into each generated subroutine.
// Each field is limited to 64 KiB (enforced by CEL validation).
type VCLSnippets struct {
	// header is inserted at the top of the VCL file, before any subroutines.
	// Use for import statements and global declarations.
	// +optional
	Header string `json:"header,omitempty"`

	// vclInit is inserted into the vcl_init subroutine after director initialisation.
	// +optional
	VCLInit string `json:"vclInit,omitempty"`

	// vclRecv is inserted into vcl_recv after generated routing logic, before return().
	// +optional
	VCLRecv string `json:"vclRecv,omitempty"`

	// vclHash is inserted into vcl_hash.
	// +optional
	VCLHash string `json:"vclHash,omitempty"`

	// vclHit is inserted into vcl_hit.
	// +optional
	VCLHit string `json:"vclHit,omitempty"`

	// vclMiss is inserted into vcl_miss.
	// +optional
	VCLMiss string `json:"vclMiss,omitempty"`

	// vclPass is inserted into vcl_pass.
	// +optional
	VCLPass string `json:"vclPass,omitempty"`

	// vclPurge is inserted into vcl_purge.
	// +optional
	VCLPurge string `json:"vclPurge,omitempty"`

	// vclPipe is inserted into vcl_pipe.
	// +optional
	VCLPipe string `json:"vclPipe,omitempty"`

	// vclBackendFetch is inserted into vcl_backend_fetch.
	// +optional
	VCLBackendFetch string `json:"vclBackendFetch,omitempty"`

	// vclBackendResponse is inserted into vcl_backend_response.
	// +optional
	VCLBackendResponse string `json:"vclBackendResponse,omitempty"`

	// vclDeliver is inserted into vcl_deliver.
	// +optional
	VCLDeliver string `json:"vclDeliver,omitempty"`

	// vclSynth is inserted into vcl_synth.
	// +optional
	VCLSynth string `json:"vclSynth,omitempty"`

	// vclBackendError is inserted into vcl_backend_error.
	// +optional
	VCLBackendError string `json:"vclBackendError,omitempty"`

	// vclFini is inserted into vcl_fini.
	// +optional
	VCLFini string `json:"vclFini,omitempty"`
}

// InvalidationSpec configures cache invalidation methods.
type InvalidationSpec struct {
	// purge configures HTTP PURGE-based cache invalidation.
	// +optional
	Purge *PurgeSpec `json:"purge,omitempty"`

	// ban configures BAN-based cache invalidation.
	// +optional
	BAN *BANSpec `json:"ban,omitempty"`

	// xkey configures xkey-based surrogate key invalidation (requires vmod_xkey).
	// +optional
	Xkey *XkeySpec `json:"xkey,omitempty"`
}

// PurgeSpec configures HTTP PURGE-based cache invalidation.
type PurgeSpec struct {
	// enabled activates the PURGE invalidation handler.
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// soft enables soft purges, which mark objects as expired rather than removing them.
	// This allows Varnish to serve stale content while revalidating. Default: true.
	// +optional
	Soft bool `json:"soft,omitempty"`

	// allowedSources is a list of CIDR ranges permitted to send PURGE requests.
	// If empty, no source restriction is applied.
	// +optional
	AllowedSources []string `json:"allowedSources,omitempty"`
}

// BANSpec configures BAN-based cache invalidation.
type BANSpec struct {
	// enabled activates the BAN invalidation handler.
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// allowedSources is a list of CIDR ranges permitted to send BAN requests.
	// +optional
	AllowedSources []string `json:"allowedSources,omitempty"`

	// rateLimitPerMinute limits BAN requests per minute. 0 means no limit.
	// +optional
	// +kubebuilder:validation:Minimum=0
	RateLimitPerMinute int32 `json:"rateLimitPerMinute,omitempty"`
}

// XkeySpec configures xkey surrogate-key invalidation (requires vmod_xkey).
type XkeySpec struct {
	// enabled activates xkey-based invalidation.
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// softPurge enables soft purging via xkey. Default: true.
	// +optional
	SoftPurge bool `json:"softPurge,omitempty"`

	// allowedSources is a list of CIDR ranges permitted to send xkey invalidation requests.
	// +optional
	AllowedSources []string `json:"allowedSources,omitempty"`
}

// ProxyProtocolSpec enables PROXY protocol on a dedicated port.
type ProxyProtocolSpec struct {
	// enabled activates PROXY protocol support.
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// port is the port on which PROXY protocol connections are accepted. Default: 8081.
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	Port int32 `json:"port,omitempty"`
}

// StorageSpec configures a Varnish storage backend.
type StorageSpec struct {
	// name is the internal storage identifier used in varnishd -s arguments.
	Name string `json:"name"`

	// type is the storage backend type. Only "malloc" and "file" are permitted.
	// "persistent", "umem", and "default" are rejected by the admission webhook.
	// +kubebuilder:validation:Enum=malloc;file
	Type string `json:"type"`

	// size is the storage allocation as a Kubernetes resource quantity (e.g. "1Gi").
	// The operator converts to bytes for varnishd.
	// +optional
	Size resource.Quantity `json:"size,omitempty"`

	// path is the filesystem path for file-type storage.
	// +optional
	Path string `json:"path,omitempty"`
}

// ServiceSpec configures the Kubernetes Service created for Varnish traffic ingress.
type ServiceSpec struct {
	// annotations are added to the Service object.
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`

	// type sets the Kubernetes Service type (ClusterIP, NodePort, LoadBalancer).
	// +optional
	// +kubebuilder:validation:Enum=ClusterIP;NodePort;LoadBalancer
	Type string `json:"type,omitempty"`
}

// DebounceSpec configures the grace period before applying endpoint changes to Varnish.
type DebounceSpec struct {
	// duration is the time to wait after the last endpoint change before pushing a VCL update.
	// This prevents thundering-herd on rapid endpoint churn. Default: 5s.
	// +optional
	Duration metav1.Duration `json:"duration,omitempty"`
}

// RetrySpec configures retry behaviour for failed VCL update operations.
type RetrySpec struct {
	// maxAttempts is the maximum number of VCL push attempts before the operator
	// transitions to Error phase. Default: 3.
	// +optional
	// +kubebuilder:validation:Minimum=1
	MaxAttempts int32 `json:"maxAttempts,omitempty"`

	// backoffBase is the initial backoff duration between retry attempts. Default: 5s.
	// +optional
	BackoffBase metav1.Duration `json:"backoffBase,omitempty"`

	// backoffMax is the maximum backoff duration (exponential backoff cap). Default: 5m.
	// +optional
	BackoffMax metav1.Duration `json:"backoffMax,omitempty"`
}

// PodSpec configures pod-level scheduling, metadata, and placement constraints.
type PodSpec struct {
	// annotations are added to each Varnish pod.
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`

	// labels are added to each Varnish pod (merged with operator-managed labels).
	// +optional
	Labels map[string]string `json:"labels,omitempty"`

	// nodeSelector constrains pods to nodes matching all specified labels.
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// tolerations allow pods to be scheduled on tainted nodes.
	// +optional
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`

	// affinity configures pod affinity and anti-affinity scheduling rules.
	// +optional
	Affinity *corev1.Affinity `json:"affinity,omitempty"`

	// priorityClassName is the name of the PriorityClass for Varnish pods.
	// +optional
	PriorityClass string `json:"priorityClassName,omitempty"`
}

// MonitoringSpec configures Prometheus monitoring for the Varnish cluster.
type MonitoringSpec struct {
	// enabled activates Prometheus metrics scraping.
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// prometheusRules configures PrometheusRule objects for alerting.
	// +optional
	PrometheusRules *PrometheusRulesSpec `json:"prometheusRules,omitempty"`

	// serviceMonitor configures a ServiceMonitor for Prometheus Operator scraping.
	// +optional
	ServiceMonitor *ServiceMonitorSpec `json:"serviceMonitor,omitempty"`
}

// PrometheusRulesSpec configures PrometheusRule objects for alerting.
type PrometheusRulesSpec struct {
	// enabled creates a PrometheusRule with default alerting rules.
	// +optional
	Enabled bool `json:"enabled,omitempty"`
}

// ServiceMonitorSpec configures a ServiceMonitor for Prometheus Operator.
type ServiceMonitorSpec struct {
	// enabled creates a ServiceMonitor targeting the Varnish metrics endpoint.
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// interval is the scrape interval (e.g. "30s"). Defaults to the Prometheus global interval.
	// +optional
	Interval string `json:"interval,omitempty"`
}

// VinylCacheStatus defines the observed state of VinylCache.
type VinylCacheStatus struct {
	// phase is the high-level lifecycle state derived from conditions.
	// One of: Pending, Ready, Degraded, Error.
	// +optional
	Phase string `json:"phase,omitempty"`

	// message provides a human-readable explanation of the current phase.
	// +optional
	Message string `json:"message,omitempty"`

	// activeVCL describes the VCL version currently loaded in Varnish.
	// +optional
	ActiveVCL *ActiveVCLStatus `json:"activeVCL,omitempty"`

	// replicas is the total number of pods targeted by the StatefulSet.
	// +optional
	Replicas int32 `json:"replicas,omitempty"`

	// readyReplicas is the number of pods that have passed their readiness checks.
	// +optional
	ReadyReplicas int32 `json:"readyReplicas,omitempty"`

	// updatedReplicas is the number of pods running the current StatefulSet revision.
	// +optional
	UpdatedReplicas int32 `json:"updatedReplicas,omitempty"`

	// availableReplicas is the number of pods available for at least minReadySeconds.
	// +optional
	AvailableReplicas int32 `json:"availableReplicas,omitempty"`

	// selector is the label selector string used by the scale subresource.
	// +optional
	Selector string `json:"selector,omitempty"`

	// backends reports the observed health of each configured backend.
	// +optional
	Backends []BackendStatus `json:"backends,omitempty"`

	// clusterPeers reports the observed state of each Varnish pod in the cluster.
	// +optional
	ClusterPeers []ClusterPeerStatus `json:"clusterPeers,omitempty"`

	// readyPeers is the number of cluster peers in Ready state.
	// +optional
	ReadyPeers int32 `json:"readyPeers,omitempty"`

	// totalPeers is the total number of cluster peers known to the operator.
	// +optional
	TotalPeers int32 `json:"totalPeers,omitempty"`

	// conditions represent the current detailed state of the VinylCache resource.
	// Standard types: Ready, VCLSynced, BackendsAvailable, Progressing, VCLConsistent.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// ActiveVCLStatus describes the VCL version currently loaded in Varnish.
type ActiveVCLStatus struct {
	// name is the VCL label (e.g. "boot" or a generated name).
	Name string `json:"name"`

	// hash is the SHA-256 digest of the VCL source, used to detect drift.
	Hash string `json:"hash"`

	// pushedAt is the time when this VCL was successfully applied.
	// +optional
	PushedAt *metav1.Time `json:"pushedAt,omitempty"`
}

// BackendStatus reports the observed health of a configured backend.
type BackendStatus struct {
	// name matches the BackendSpec.Name.
	Name string `json:"name"`

	// healthy is true if Varnish considers the backend available.
	Healthy bool `json:"healthy"`

	// address is the resolved endpoint address (host:port).
	// +optional
	Address string `json:"address,omitempty"`
}

// ClusterPeerStatus reports the observed state of a single Varnish pod in the cluster.
type ClusterPeerStatus struct {
	// podName is the name of the Varnish pod.
	PodName string `json:"podName"`

	// ready is true if the pod has passed its readiness check.
	Ready bool `json:"ready"`

	// activeVCLHash is the hash of the VCL currently loaded in this pod.
	// Used to detect VCL consistency across the cluster.
	// +optional
	ActiveVCLHash string `json:"activeVCLHash,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:subresource:scale:specpath=.spec.replicas,statuspath=.status.replicas,selectorpath=.status.selector
// +kubebuilder:resource:shortName=vc,categories=vinyl
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="Replicas",type="string",JSONPath=".status.readyReplicas"
// +kubebuilder:printcolumn:name="VCL",type="string",JSONPath=".status.conditions[?(@.type=='VCLSynced')].status"
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase",priority=1
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// VinylCache is the Schema for the vinylcaches API.
// It describes a complete Varnish cache cluster: replicas, backends, VCL configuration,
// clustering, and cache invalidation strategy.
type VinylCache struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is standard Kubernetes object metadata.
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// spec defines the desired state of the VinylCache cluster.
	// +optional
	Spec VinylCacheSpec `json:"spec,omitempty"`

	// status defines the observed state of the VinylCache cluster.
	// +optional
	Status VinylCacheStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// VinylCacheList contains a list of VinylCache.
type VinylCacheList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []VinylCache `json:"items"`
}

func init() {
	SchemeBuilder.Register(&VinylCache{}, &VinylCacheList{})
}
