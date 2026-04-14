package generator

import (
	"bytes"
	"embed"
	"fmt"
	"strings"
	"text/template"
	"time"

	vinylv1alpha1 "github.com/bluedynamics/cloud-vinyl/api/v1alpha1"
)

//go:embed templates
var templateFS embed.FS

// Generator produces Varnish VCL from a VinylCacheSpec and endpoint data.
type Generator interface {
	Generate(input Input) (*Result, error)
}

// Input contains everything the generator needs to produce VCL.
type Input struct {
	Spec      *vinylv1alpha1.VinylCacheSpec
	Peers     []PeerBackend         // cluster peers (other pods in the StatefulSet)
	Endpoints map[string][]Endpoint // backend name -> list of endpoints
	Namespace string                // VinylCache namespace (for VCL name)
	Name      string                // VinylCache name (for VCL name)
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
	HasProxyProtocol bool
	HasFullOverride  bool
	VCLName          string         // sanitized name for vcl declaration
	BackendGroups    []BackendGroup // Replaces flat BackendDefs: grouped per spec.backends[i] for directors.
	PeerDefs         []BackendDef
	UseShardDirector bool
	DirectorName     string
}

// BackendDef is a single backend definition for VCL.
type BackendDef struct {
	Name                string
	IP                  string
	Port                int
	ProbeURL            string
	ConnectTimeout      string
	FirstByteTimeout    string
	BetweenBytesTimeout string
	IdleTimeout         string
	MaxConnections      int32
}

// BackendGroup is one CRD backend (spec.backends[i]) expanded to its per-pod backends,
// with the director algorithm that groups them in vcl_init.
type BackendGroup struct {
	Name     string       // VCL identifier; matches BackendSpec.Name (sanitized).
	Director DirectorInfo // Director algorithm + params for this group.
	Backends []BackendDef // One per resolved Endpoint; Name is "<Group.Name>_<idx>".
}

// DirectorInfo captures the resolved director config for a backend group.
// It reflects the v1alpha1.DirectorSpec but is template-friendly.
type DirectorInfo struct {
	Type   string  // "shard" (default), "round_robin", "random", "fallback".
	Warmup float64 // 0.0 if unset; only for shard.
	Rampup string  // empty if unset; formatted via fmtDuration; only for shard.
	By     string  // "HASH" (default) or "URL"; only for shard.
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
	if input.Spec.VCL.FullOverride != "" {
		vcl := fmt.Sprintf("# cloud-vinyl managed VCL (full override mode)\n# Name: %s/%s\n\n%s",
			input.Namespace, input.Name, input.Spec.VCL.FullOverride)
		return &Result{VCL: vcl, Hash: HashVCL(vcl)}, nil
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
		data.HasSoftPurge = input.Spec.Invalidation.Purge.Soft
	}

	// ESI: check VarnishParams for explicit feature flag.
	_, hasESI := input.Spec.VarnishParams["feature +esi"]
	data.HasESI = hasESI

	data.UseShardDirector = input.Spec.Director.Type == "shard" || input.Spec.Director.Type == ""

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

	return data
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
func resolveDirectorInfo(ds *vinylv1alpha1.DirectorSpec) DirectorInfo {
	out := DirectorInfo{Type: "shard", By: "HASH"}
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
		if ds.Shard.By != "" {
			out.By = ds.Shard.By
		}
	}
	return out
}

// sanitizeName replaces hyphens with underscores for VCL identifier compatibility.
func sanitizeName(name string) string {
	return strings.ReplaceAll(name, "-", "_")
}
