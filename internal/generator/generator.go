package generator

import (
	"bytes"
	"embed"
	"fmt"
	"strconv"
	"strings"
	"text/template"
	"time"

	vinylv1 "github.com/bluedynamics/cloud-vinyl/api/v1"
)

//go:embed templates
var templateFS embed.FS

// Generator produces Varnish VCL from a VinylCacheSpec and endpoint data.
type Generator interface {
	Generate(input Input) (*Result, error)
}

// Input contains everything the generator needs to produce VCL.
type Input struct {
	Spec      *vinylv1.VinylCacheSpec
	Peers     []PeerBackend         // cluster peers (other pods in the StatefulSet)
	Endpoints map[string][]Endpoint // backend name -> list of endpoints
	Namespace string                // VinylCache namespace (for VCL name)
	Name      string                // VinylCache name (for VCL name)

	// OperatorIP is the pod IP of the cloud-vinyl operator instance that
	// is reconciling this VinylCache. When non-empty it is added to the
	// vinyl_purge_allowed ACL so the operator's invalidation proxy can
	// forward PURGE requests into Varnish. Empty in tests / pre-GA startup.
	OperatorIP string
}

// PeerBackend is a cluster peer (another Varnish pod).
type PeerBackend struct {
	Name string // e.g. "my_cache_0"
	IP   string // Pod IP
	Port int    // typically 8080
}

// Endpoint is a resolved backend endpoint.
type Endpoint struct {
	IP   string
	Port int
}

// Result is the output of the generator.
type Result struct {
	VCL  string
	Hash string // SHA-256 of VCL
}

// TemplateData is passed to all templates.
type TemplateData struct {
	Input
	// Derived fields for convenience
	HasCluster       bool
	HasESI           bool
	HasXkey          bool
	HasSoftPurge     bool
	HasBAN           bool
	HasProxyProtocol bool
	HasFullOverride  bool
	// HasTracing gates the tracing VCL: uuid import, traceparent
	// validation in vcl_recv, span-id minting in vcl_backend_fetch.
	HasTracing       bool
	VCLName          string         // sanitized name for vcl declaration
	BackendGroups    []BackendGroup // Replaces flat BackendDefs: grouped per spec.backends[i] for directors.
	PeerDefs         []BackendDef
	UseShardDirector bool
	DirectorName     string

	// ClusterShardBy is the hash-key strategy for the cluster-peer shard
	// director's request-time .backend() call. Always "URL": it is the only
	// value the CRD enum allows (#92 — by=HASH hashes req.hash, which is
	// never populated on this call site, so it cannot work here and was
	// removed from the enum). Kept as a template field, rather than a
	// hardcoded literal, so buildTemplateData has somewhere to coerce a
	// legacy stored "HASH" value to (see the migration comment there).
	ClusterShardBy string
	// ClusterShardHealthy is the health policy the cluster-peer director
	// evaluates at request time. Empty means "Varnish default" — template
	// omits the healthy= argument. Overridable via spec.director.shard.healthy.
	ClusterShardHealthy string

	// HasFetchRetry gates the retry VCL in vcl_backend_error and
	// vcl_backend_fetch (#117): true if any backend group or the peer hop
	// retries.
	HasFetchRetry bool
	// PeerRetries is spec.cluster.retry.maxRetries resolved to its default;
	// 0 when clustering is off.
	PeerRetries int32
	// PeerRetryMatch is a VCL condition that is true when bereq.backend is
	// one of the peer backends.
	PeerRetryMatch string
	// PeerRetryReselect is the VCL expression that picks another peer.
	PeerRetryReselect string
}

// BackendDef is a single backend definition for VCL.
type BackendDef struct {
	Name                  string
	IP                    string
	Port                  int
	ProbeURL              string
	ProbeInterval         string // e.g. "5s"; empty means use built-in default
	ProbeTimeout          string // e.g. "2s"; empty means use built-in default
	ProbeWindow           int32  // 0 means use built-in default
	ProbeThreshold        int32  // 0 means use built-in default
	ProbeExpectedResponse int32  // 0 means use built-in default (200)
	ConnectTimeout        string
	FirstByteTimeout      string
	BetweenBytesTimeout   string
	IdleTimeout           string
	MaxConnections        int32
}

// BackendGroup is one CRD backend (spec.backends[i]) expanded to its per-pod backends,
// with the director algorithm that groups them in vcl_init.
type BackendGroup struct {
	Name     string       // VCL identifier: sanitizeName(BackendSpec.Name).
	Director DirectorInfo // Director algorithm + params for this group.
	Backends []BackendDef // One per resolved Endpoint; Name is "<Group.Name>_<idx>".

	// Retries is spec.backends[i].retry.maxRetries resolved to its default,
	// 0 for directors that cannot pick a different backend (hash, fallback)
	// and for groups without endpoints.
	Retries int32
	// RetryMatch is a VCL condition that is true when bereq.backend belongs
	// to this group.
	RetryMatch string
	// RetryReselect is the VCL expression that picks another backend of
	// this group on a retry.
	RetryReselect string
	// RetryFirstByteTimeout is the group's configured first_byte_timeout as
	// an exact VCL duration, empty if unset. bereq.first_byte_timeout does
	// not reflect a backend definition's .first_byte_timeout, so the retry
	// guard needs it separately.
	RetryFirstByteTimeout string
}

// DirectorInfo captures the resolved director config for a backend group.
// It reflects the vinylv1.DirectorSpec but is template-friendly.
//
// Intentionally scoped: only fields the VCL templates consume today are captured
// (Type, shard.Warmup, shard.Rampup). Extend this struct (and resolveDirectorInfo)
// when new template bindings are needed — e.g. shard.Replicas or shard.Healthy
// for advanced sharding, or hash.Header for the hash director.
type DirectorInfo struct {
	Type   string  // CRD enum: "shard" (default), "round_robin", "random", "hash", "fallback".
	Warmup float64 // 0.0 if unset; only for shard.
	Rampup string  // empty if unset; formatted via fmtDuration; only for shard.
}

type templateGenerator struct {
	templates *template.Template
}

// New creates a new VCL Generator using the embedded template files.
func New() Generator {
	tmpl := template.Must(template.New("vcl").Funcs(templateFuncMap()).ParseFS(templateFS, "templates/*.vcl.tmpl"))
	return &templateGenerator{templates: tmpl}
}

// NewWithTemplateDir creates a generator with templates from a specific dir.
func NewWithTemplateDir(dir string) (Generator, error) {
	pattern := dir + "/*.vcl.tmpl"
	tmpl, err := template.New("vcl").Funcs(templateFuncMap()).ParseGlob(pattern)
	if err != nil {
		return nil, fmt.Errorf("parse templates from %s: %w", dir, err)
	}
	return &templateGenerator{templates: tmpl}, nil
}

// templateFuncMap returns the shared template function map.
func templateFuncMap() template.FuncMap {
	return template.FuncMap{
		"join":      strings.Join,
		"hasPrefix": strings.HasPrefix,
		"deref": func(f *float64) float64 {
			if f == nil {
				return 0.0
			}
			return *f
		},
		"fmtDuration": func(d time.Duration) string {
			if d.Hours() >= 1 {
				return fmt.Sprintf("%.0fh", d.Hours())
			}
			if d.Minutes() >= 1 {
				return fmt.Sprintf("%.0fm", d.Minutes())
			}
			return fmt.Sprintf("%.0fs", d.Seconds())
		},
	}
}

func (g *templateGenerator) Generate(input Input) (*Result, error) {
	data := buildTemplateData(input)

	// If fullOverride is set, return it directly (with a comment header).
	// Full-override bypasses the empty-backend guard — users supplying their
	// own VCL are not subject to the director constraint.
	if input.Spec.VCL.FullOverride != "" {
		vcl := fmt.Sprintf("# cloud-vinyl managed VCL (full override mode)\n# Name: %s/%s\n\n%s",
			input.Namespace, input.Name, input.Spec.VCL.FullOverride)
		return &Result{VCL: vcl, Hash: HashVCL(vcl)}, nil
	}

	// Defensive guard: refuse to emit VCL with an empty director. A shard
	// director with zero add_backend calls is a runtime error in Varnish at
	// VCL load time. The controller already skips the push in this case;
	// this guard ensures invalid VCL can never be produced.
	for _, g := range data.BackendGroups {
		if len(g.Backends) == 0 {
			return nil, fmt.Errorf("backend %q has no resolved endpoints; refusing to emit VCL with empty director", g.Name)
		}
	}

	var buf bytes.Buffer
	if err := g.templates.ExecuteTemplate(&buf, "main.vcl.tmpl", data); err != nil {
		return nil, fmt.Errorf("execute template: %w", err)
	}
	vcl := buf.String()
	return &Result{VCL: vcl, Hash: HashVCL(vcl)}, nil
}

func buildTemplateData(input Input) TemplateData {
	data := TemplateData{
		Input:            input,
		HasCluster:       input.Spec.Cluster.Enabled && len(input.Peers) > 0,
		HasProxyProtocol: input.Spec.ProxyProtocol.Enabled,
		HasFullOverride:  input.Spec.VCL.FullOverride != "",
		VCLName:          sanitizeName(input.Name),
		DirectorName:     "director_" + sanitizeName(input.Name),
	}

	// Feature flags from invalidation spec.
	if input.Spec.Invalidation.Xkey != nil {
		data.HasXkey = input.Spec.Invalidation.Xkey.Enabled
	}
	if input.Spec.Invalidation.Purge != nil {
		// A nil Soft means "unset": treat it as true, matching the CRD's
		// +kubebuilder:default=true. Callers that construct the struct
		// directly (e.g. unit tests) bypass the API server's defaulting, so
		// this must be nil-safe rather than relying on admission having run.
		data.HasSoftPurge = input.Spec.Invalidation.Purge.Soft == nil || *input.Spec.Invalidation.Purge.Soft
	}
	if input.Spec.Invalidation.BAN != nil {
		data.HasBAN = input.Spec.Invalidation.BAN.Enabled
	}

	// ESI is a first-class field, not a varnishParameters convention: the
	// previous approach gated this on VarnishParams["feature"] containing
	// "+esi", but that string is rendered verbatim into varnishd's real
	// -p feature=... argument and real varnishd has no "esi" feature bit —
	// passing it crashes varnishd outright ("Unknown feature bit (+esi)").
	// spec.esi.enabled carries no such risk: it never reaches varnishd argv.
	data.HasESI = input.Spec.ESI.Enabled

	data.HasTracing = input.Spec.Tracing.Enabled

	data.UseShardDirector = input.Spec.Director.Type == directorShard || input.Spec.Director.Type == ""

	// Cluster-peer shard director parameters (request-time).
	// Fall back to "URL" for by (preserves pre-#36 behaviour) and to empty
	// for healthy (so the template omits the arg and Varnish uses its default).
	//
	// "URL" is the only value the CRD enum allows going forward (#92), but
	// stored objects written before that enum change may still carry the old
	// "HASH" value: the API server only validates on write, so an existing
	// object keeps reconciling with its stale stored spec indefinitely. Only
	// pass through an explicit "URL"; anything else (empty, the legacy
	// "HASH", or any value validation didn't yet catch) falls back to "URL"
	// rather than reproducing #92's silent non-sharding for those objects.
	data.ClusterShardBy = "URL"
	data.ClusterShardHealthy = ""
	if input.Spec.Director.Shard != nil {
		if input.Spec.Director.Shard.By == "URL" {
			data.ClusterShardBy = input.Spec.Director.Shard.By
		}
		if input.Spec.Director.Shard.Healthy != "" {
			data.ClusterShardHealthy = input.Spec.Director.Shard.Healthy
		}
	}

	// Build backend groups from spec backends + resolved endpoints.
	for _, b := range input.Spec.Backends {
		group := BackendGroup{
			Name:     sanitizeName(b.Name),
			Director: resolveDirectorInfo(b.Director),
		}
		for i, ep := range input.Endpoints[b.Name] {
			def := BackendDef{
				Name: fmt.Sprintf("%s_%d", group.Name, i),
				IP:   ep.IP,
				Port: ep.Port,
			}
			if b.Probe != nil && b.Probe.URL != "" {
				def.ProbeURL = b.Probe.URL
				if b.Probe.Interval.Duration > 0 {
					def.ProbeInterval = fmtDuration(b.Probe.Interval.Duration)
				} else {
					def.ProbeInterval = "5s"
				}
				if b.Probe.Timeout.Duration > 0 {
					def.ProbeTimeout = fmtDuration(b.Probe.Timeout.Duration)
				} else {
					def.ProbeTimeout = "2s"
				}
				if b.Probe.Window > 0 {
					def.ProbeWindow = b.Probe.Window
				} else {
					def.ProbeWindow = 5
				}
				if b.Probe.Threshold > 0 {
					def.ProbeThreshold = b.Probe.Threshold
				} else {
					def.ProbeThreshold = 3
				}
				if b.Probe.ExpectedResponse != nil {
					def.ProbeExpectedResponse = *b.Probe.ExpectedResponse
				}
			}
			if b.ConnectionParameters != nil {
				cp := b.ConnectionParameters
				if cp.ConnectTimeout.Duration > 0 {
					def.ConnectTimeout = fmtDuration(cp.ConnectTimeout.Duration)
				}
				if cp.FirstByteTimeout.Duration > 0 {
					def.FirstByteTimeout = fmtDuration(cp.FirstByteTimeout.Duration)
				}
				if cp.BetweenBytesTimeout.Duration > 0 {
					def.BetweenBytesTimeout = fmtDuration(cp.BetweenBytesTimeout.Duration)
				}
				if cp.IdleTimeout.Duration > 0 {
					def.IdleTimeout = fmtDuration(cp.IdleTimeout.Duration)
				}
				def.MaxConnections = cp.MaxConnections
			}
			group.Backends = append(group.Backends, def)
		}
		setGroupRetry(&group, b)
		data.HasFetchRetry = data.HasFetchRetry || group.Retries > 0
		data.BackendGroups = append(data.BackendGroups, group)
	}

	// Build peer defs.
	for _, p := range input.Peers {
		data.PeerDefs = append(data.PeerDefs, BackendDef{
			Name: p.Name,
			IP:   p.IP,
			Port: p.Port,
		})
	}
	if data.HasCluster {
		data.PeerRetries = resolveMaxRetries(input.Spec.Cluster.Retry)
		if data.PeerRetries > 0 {
			names := make([]string, 0, len(data.PeerDefs))
			for _, p := range data.PeerDefs {
				names = append(names, p.Name)
			}
			data.PeerRetryMatch = backendMatch(names)
			data.PeerRetryReselect = fmt.Sprintf("%s.backend(by=%s, alt=bereq.retries, healthy=ALL)",
				data.DirectorName, data.ClusterShardBy)
			data.HasFetchRetry = true
		}
	}

	return data
}

// directorShard is the shard director type, also the default.
const directorShard = "shard"

// defaultFetchRetries is the retry count when spec.backends[].retry or
// spec.cluster.retry leaves maxRetries unset.
const defaultFetchRetries = 1

func resolveMaxRetries(r *vinylv1.FetchRetrySpec) int32 {
	if r == nil || r.MaxRetries == nil {
		return defaultFetchRetries
	}
	return *r.MaxRetries
}

// setGroupRetry fills the retry fields of a backend group (#117).
//
// A retry only helps if it lands on a different backend:
//   - shard: the client side resolves .backend() immediately (resolve=NOW is
//     the default outside vcl_init), so bereq.backend holds the concrete
//     backend and a bare retry would reuse it. The re-selection asks for the
//     alt-th alternative of the same key: by=URL hashes bereq.url, which is
//     what the default by=HASH hashes on the client side (req.url).
//     healthy=ALL counts only healthy backends, so an already-sick primary
//     does not make alt=1 return the backend that just failed.
//   - round_robin, random: .backend() returns the director itself, which
//     resolves anew on every attempt and usually picks another backend.
//   - hash, fallback: deterministic, they would pick the same backend, so
//     they never retry.
func setGroupRetry(g *BackendGroup, b vinylv1.BackendSpec) {
	// Generate already refuses groups without endpoints; nothing to match.
	if len(g.Backends) == 0 {
		return
	}
	names := make([]string, 0, len(g.Backends)+1)
	for _, def := range g.Backends {
		names = append(names, def.Name)
	}
	switch g.Director.Type {
	case directorShard:
		g.RetryReselect = g.Name + ".backend(by=URL, alt=bereq.retries, healthy=ALL)"
	case "round_robin", "random":
		names = append(names, g.Name+".backend()")
		g.RetryReselect = g.Name + ".backend()"
	default:
		return
	}
	g.Retries = resolveMaxRetries(b.Retry)
	if g.Retries == 0 {
		return
	}
	g.RetryMatch = backendMatch(names)
	if cp := b.ConnectionParameters; cp != nil && cp.FirstByteTimeout.Duration > 0 {
		g.RetryFirstByteTimeout = strconv.FormatFloat(cp.FirstByteTimeout.Seconds(), 'f', -1, 64) + "s"
	}
}

// backendMatch renders a VCL condition that is true when bereq.backend is
// one of the given backends.
func backendMatch(backends []string) string {
	parts := make([]string, len(backends))
	for i, b := range backends {
		parts[i] = "bereq.backend == " + b
	}
	return strings.Join(parts, " || ")
}

// fmtDuration formats a time.Duration into a Varnish-compatible duration string.
func fmtDuration(d time.Duration) string {
	if d.Hours() >= 1 {
		return fmt.Sprintf("%.0fh", d.Hours())
	}
	if d.Minutes() >= 1 {
		return fmt.Sprintf("%.0fm", d.Minutes())
	}
	return fmt.Sprintf("%.0fs", d.Seconds())
}

// resolveDirectorInfo collapses a nullable per-backend DirectorSpec into a template-ready
// DirectorInfo with defaults (shard / HASH / empty warmup/rampup).
func resolveDirectorInfo(ds *vinylv1.DirectorSpec) DirectorInfo {
	out := DirectorInfo{Type: directorShard}
	if ds == nil {
		return out
	}
	if ds.Type != "" {
		out.Type = ds.Type
	}
	if ds.Shard != nil {
		if ds.Shard.Warmup != nil {
			out.Warmup = *ds.Shard.Warmup
		}
		if ds.Shard.Rampup.Duration > 0 {
			out.Rampup = fmtDuration(ds.Shard.Rampup.Duration)
		}
	}
	return out
}

// sanitizeName replaces hyphens with underscores for VCL identifier compatibility.
func sanitizeName(name string) string {
	return strings.ReplaceAll(name, "-", "_")
}
