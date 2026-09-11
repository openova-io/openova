// Package metrics is a dependency-free Prometheus text-exposition registry
// with counters, gauges and histograms — the same shape the chargeback
// application's own registry has (products/chargeback/internal/metrics),
// extended with the histogram this service needs to report render latency.
//
// Keeping it in-module holds the renderer at one external dependency (the PDF
// library) and keeps the image a single static binary.
package metrics

import (
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// DefaultBuckets are render-duration buckets in seconds: a small document is
// a few milliseconds, the 10 s render deadline is the last finite bound.
var DefaultBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

// SizeBuckets are response-size buckets in bytes, 4 KiB to 16 MiB.
var SizeBuckets = []float64{4096, 16384, 65536, 262144, 1048576, 4194304, 16777216}

type sample struct {
	labels string
	value  float64
}

type histogram struct {
	labels  string
	buckets []float64
	counts  []uint64
	sum     float64
	count   uint64
}

type family struct {
	name, help, kind string
	samples          map[string]*sample
	hists            map[string]*histogram
	buckets          []float64
}

// Registry holds metric families.
type Registry struct {
	mu       sync.Mutex
	families map[string]*family
	order    []string
}

// New returns an empty registry.
func New() *Registry { return &Registry{families: map[string]*family{}} }

func (r *Registry) fam(name, help, kind string) *family {
	f, ok := r.families[name]
	if !ok {
		f = &family{name: name, help: help, kind: kind, samples: map[string]*sample{}, hists: map[string]*histogram{}}
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
	rep := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf(`%s="%s"`, k, rep.Replace(labels[k])))
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
	f.samples[renderLabels(labels)] = &sample{labels: renderLabels(labels), value: value}
}

// Observe records one value into a histogram.
func (r *Registry) Observe(name, help string, buckets []float64, labels map[string]string, value float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	f := r.fam(name, help, "histogram")
	if f.buckets == nil {
		f.buckets = buckets
	}
	l := renderLabels(labels)
	h, ok := f.hists[l]
	if !ok {
		h = &histogram{labels: l, buckets: f.buckets, counts: make([]uint64, len(f.buckets))}
		f.hists[l] = h
	}
	for i, b := range h.buckets {
		if value <= b {
			h.counts[i]++
		}
	}
	h.sum += value
	h.count++
}

// Get returns a counter or gauge sample (0 when absent). Used by tests.
func (r *Registry) Get(name string, labels map[string]string) float64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	f, ok := r.families[name]
	if !ok {
		return 0
	}
	if s, ok := f.samples[renderLabels(labels)]; ok {
		return s.value
	}
	return 0
}

// Count returns a histogram's observation count (0 when absent).
func (r *Registry) Count(name string, labels map[string]string) uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	f, ok := r.families[name]
	if !ok {
		return 0
	}
	if h, ok := f.hists[renderLabels(labels)]; ok {
		return h.count
	}
	return 0
}

// Write renders the registry in Prometheus text-exposition format.
func (r *Registry) Write(w io.Writer) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, name := range r.order {
		f := r.families[name]
		if _, err := fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n", f.name, f.help, f.name, f.kind); err != nil {
			return err
		}
		if f.kind == "histogram" {
			keys := make([]string, 0, len(f.hists))
			for k := range f.hists {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				if err := writeHistogram(w, f.name, f.hists[k]); err != nil {
					return err
				}
			}
			continue
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

func writeHistogram(w io.Writer, name string, h *histogram) error {
	base := strings.TrimSuffix(strings.TrimPrefix(h.labels, "{"), "}")
	with := func(extra string) string {
		if base == "" {
			return "{" + extra + "}"
		}
		return "{" + base + "," + extra + "}"
	}
	for i, b := range h.buckets {
		le := strconv.FormatFloat(b, 'g', -1, 64)
		if _, err := fmt.Fprintf(w, "%s_bucket%s %d\n", name, with(`le="`+le+`"`), h.counts[i]); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(w, "%s_bucket%s %d\n", name, with(`le="+Inf"`), h.count); err != nil {
		return err
	}
	sum := h.sum
	if math.IsNaN(sum) || math.IsInf(sum, 0) {
		sum = 0
	}
	if _, err := fmt.Fprintf(w, "%s_sum%s %g\n", name, h.labels, sum); err != nil {
		return err
	}
	_, err := fmt.Fprintf(w, "%s_count%s %d\n", name, h.labels, h.count)
	return err
}
