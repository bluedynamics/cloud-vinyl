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
	VCLName          string // sanitized name for vcl declaration
	BackendDefs      []BackendDef
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

	// Build backend defs from spec backends + resolved endpoints.
	for _, b := range input.Spec.Backends {
		endpoints := input.Endpoints[b.Name]
		for i, ep := range endpoints {
			name := fmt.Sprintf("%s_%d", b.Name, i)
			def := BackendDef{
				Name: name,
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
			data.BackendDefs = append(data.BackendDefs, def)
		}
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

// sanitizeName replaces hyphens with underscores for VCL identifier compatibility.
func sanitizeName(name string) string {
	return strings.ReplaceAll(name, "-", "_")
}
