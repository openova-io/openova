// Package metrics is a dependency-free Prometheus text-exposition registry
// with counters and gauges, enough for the collector and API counters this
// service exposes at GET /metrics.
package metrics

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
)

type sample struct {
	labels string // rendered {k="v",...} or ""
	value  float64
}

type family struct {
	name, help, kind string
	samples          map[string]*sample
}

// Registry holds metric families.
type Registry struct {
	mu       sync.Mutex
	families map[string]*family
	order    []string
}

// New returns an empty registry.
func New() *Registry {
	return &Registry{families: map[string]*family{}}
}

// Default is the process-wide registry.
var Default = New()

func (r *Registry) fam(name, help, kind string) *family {
	f, ok := r.families[name]
	if !ok {
		f = &family{name: name, help: help, kind: kind, samples: map[string]*sample{}}
		r.families[name] = f
		r.order = append(r.order, name)
	}
	return f
}

func renderLabels(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		v := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(labels[k])
		parts = append(parts, fmt.Sprintf(`%s="%s"`, k, v))
	}
	return "{" + strings.Join(parts, ",") + "}"
}

// Inc adds delta to a counter.
func (r *Registry) Inc(name, help string, labels map[string]string, delta float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	f := r.fam(name, help, "counter")
	l := renderLabels(labels)
	s, ok := f.samples[l]
	if !ok {
		s = &sample{labels: l}
		f.samples[l] = s
	}
	s.value += delta
}

// Set sets a gauge.
func (r *Registry) Set(name, help string, labels map[string]string, value float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	f := r.fam(name, help, "gauge")
	l := renderLabels(labels)
	f.samples[l] = &sample{labels: l, value: value}
}

// Get returns the current value of one sample (0 when absent). Used by tests.
func (r *Registry) Get(name string, labels map[string]string) float64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	f, ok := r.families[name]
	if !ok {
		return 0
	}
	s, ok := f.samples[renderLabels(labels)]
	if !ok {
		return 0
	}
	return s.value
}

// Write renders the registry in Prometheus text format.
func (r *Registry) Write(w io.Writer) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, name := range r.order {
		f := r.families[name]
		if _, err := fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n", f.name, f.help, f.name, f.kind); err != nil {
			return err
		}
		keys := make([]string, 0, len(f.samples))
		for k := range f.samples {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			s := f.samples[k]
			if _, err := fmt.Fprintf(w, "%s%s %g\n", f.name, s.labels, s.value); err != nil {
				return err
			}
		}
	}
	return nil
}
