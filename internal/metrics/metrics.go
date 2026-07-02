// Package metrics exposes the manager's own Prometheus metrics and bundles the
// currently running llama-server's /metrics into the same endpoint.
package metrics

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics holds the manager's own collectors.
type Metrics struct {
	reg        *prometheus.Registry
	requests   *prometheus.CounterVec
	duration   *prometheus.HistogramVec
	tokensGen  *prometheus.CounterVec
	promptTok  *prometheus.CounterVec
	instanceUp *prometheus.GaugeVec
	queueDepth prometheus.Gauge
	client     *http.Client
}

// New builds and registers the collectors.
func New() *Metrics {
	m := &Metrics{
		reg: prometheus.NewRegistry(),
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "lim_requests_total", Help: "Proxied requests by model and status.",
		}, []string{"model", "status"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "lim_request_duration_seconds", Help: "Wall time per proxied request.",
			Buckets: prometheus.DefBuckets,
		}, []string{"model"}),
		tokensGen: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "lim_tokens_generated_total", Help: "Generated (predicted) tokens by model.",
		}, []string{"model"}),
		promptTok: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "lim_prompt_tokens_total", Help: "Prompt tokens by model.",
		}, []string{"model"}),
		instanceUp: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "lim_instance_up", Help: "1 when a model instance is running.",
		}, []string{"model"}),
		queueDepth: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "lim_queue_depth", Help: "Requests waiting for an instance to become ready.",
		}),
		client: &http.Client{Timeout: 3 * time.Second},
	}
	m.reg.MustRegister(m.requests, m.duration, m.tokensGen, m.promptTok, m.instanceUp, m.queueDepth)
	return m
}

// RecordRequest records one proxied request.
func (m *Metrics) RecordRequest(model string, status, promptN, predictedN int, dur time.Duration) {
	m.requests.WithLabelValues(model, fmt.Sprintf("%d", status)).Inc()
	m.duration.WithLabelValues(model).Observe(dur.Seconds())
	if predictedN > 0 {
		m.tokensGen.WithLabelValues(model).Add(float64(predictedN))
	}
	if promptN > 0 {
		m.promptTok.WithLabelValues(model).Add(float64(promptN))
	}
}

// SetInstanceUp toggles the up gauge for a model.
func (m *Metrics) SetInstanceUp(model string, up bool) {
	v := 0.0
	if up {
		v = 1
	}
	m.instanceUp.WithLabelValues(model).Set(v)
}

// SetQueueDepth reports the current cold-start queue depth.
func (m *Metrics) SetQueueDepth(n int64) {
	m.queueDepth.Set(float64(n))
}

// CurrentInstance returns the running model name and its base URL, or ok=false.
type CurrentInstance func() (model, baseURL string, ok bool)

// Handler serves the manager metrics followed by the bundled llama-server
// metrics of the active instance (relabelled with model="...").
func (m *Metrics) Handler(current CurrentInstance) http.Handler {
	own := promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		own.ServeHTTP(w, r)
		model, baseURL, ok := current()
		if !ok {
			return
		}
		bundled, err := m.fetchInstance(baseURL, model)
		if err != nil {
			fmt.Fprintf(w, "# bundling %s failed: %v\n", model, err)
			return
		}
		fmt.Fprintf(w, "\n# bundled from model %q\n%s", model, bundled)
	})
}

// fetchInstance retrieves the instance /metrics and injects a model label into
// every metric line.
func (m *Metrics) fetchInstance(baseURL, model string) (string, error) {
	resp, err := m.client.Get(baseURL + "/metrics")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return relabel(string(body), model), nil
}

// relabel injects model="..." into each metric sample line, leaving HELP/TYPE
// comments untouched.
func relabel(text, model string) string {
	label := fmt.Sprintf("model=%q", model)
	var b strings.Builder
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			b.WriteString(line)
			b.WriteByte('\n')
			continue
		}
		b.WriteString(injectLabel(line, label))
		b.WriteByte('\n')
	}
	return b.String()
}

// injectLabel adds label to a single Prometheus sample line.
func injectLabel(line, label string) string {
	name := line
	if i := strings.IndexAny(line, " {"); i >= 0 {
		name = line[:i]
	}
	rest := strings.TrimPrefix(line, name)
	switch {
	case strings.HasPrefix(rest, "{"):
		// existing labels: insert before the closing brace
		end := strings.Index(rest, "}")
		if end < 0 {
			return line
		}
		inner := rest[1:end]
		joined := label
		if inner != "" {
			joined = inner + "," + label
		}
		return name + "{" + joined + "}" + rest[end+1:]
	default:
		return name + "{" + label + "}" + rest
	}
}
