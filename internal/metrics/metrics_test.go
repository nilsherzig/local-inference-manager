package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// llamaMetrics is a real llama-server /metrics dump.
const llamaMetrics = `# HELP llamacpp:prompt_tokens_total Number of prompt tokens processed.
# TYPE llamacpp:prompt_tokens_total counter
llamacpp:prompt_tokens_total 18292
# HELP llamacpp:prompt_seconds_total Prompt process time
# TYPE llamacpp:prompt_seconds_total counter
llamacpp:prompt_seconds_total 35.585
# HELP llamacpp:tokens_predicted_total Number of generation tokens processed.
# TYPE llamacpp:tokens_predicted_total counter
llamacpp:tokens_predicted_total 1406
# HELP llamacpp:tokens_predicted_seconds_total Predict process time
# TYPE llamacpp:tokens_predicted_seconds_total counter
llamacpp:tokens_predicted_seconds_total 41.827
# HELP llamacpp:n_decode_total Total number of llama_decode() calls
# TYPE llamacpp:n_decode_total counter
llamacpp:n_decode_total 739
# HELP llamacpp:n_tokens_max Largest observed n_tokens.
# TYPE llamacpp:n_tokens_max counter
llamacpp:n_tokens_max 8156
# HELP llamacpp:prompt_tokens_seconds Average prompt throughput in tokens/s.
# TYPE llamacpp:prompt_tokens_seconds gauge
llamacpp:prompt_tokens_seconds 514.037
# HELP llamacpp:predicted_tokens_seconds Average generation throughput in tokens/s.
# TYPE llamacpp:predicted_tokens_seconds gauge
llamacpp:predicted_tokens_seconds 33.6147
# HELP llamacpp:requests_processing Number of requests processing.
# TYPE llamacpp:requests_processing gauge
llamacpp:requests_processing 0
# HELP llamacpp:requests_deferred Number of requests deferred.
# TYPE llamacpp:requests_deferred gauge
llamacpp:requests_deferred 0
# HELP llamacpp:n_busy_slots_per_decode Average number of busy slots per llama_decode() call
# TYPE llamacpp:n_busy_slots_per_decode gauge
llamacpp:n_busy_slots_per_decode 1
`

// allLlamaMetrics lists every metric name in llamaMetrics.
var allLlamaMetrics = []string{
	"llamacpp:prompt_tokens_total",
	"llamacpp:prompt_seconds_total",
	"llamacpp:tokens_predicted_total",
	"llamacpp:tokens_predicted_seconds_total",
	"llamacpp:n_decode_total",
	"llamacpp:n_tokens_max",
	"llamacpp:prompt_tokens_seconds",
	"llamacpp:predicted_tokens_seconds",
	"llamacpp:requests_processing",
	"llamacpp:requests_deferred",
	"llamacpp:n_busy_slots_per_decode",
}

func TestRelabelInjectsModel(t *testing.T) {
	in := "# HELP foo help text\n" +
		"# TYPE foo counter\n" +
		"foo 42\n" +
		"bar{le=\"0.5\"} 7\n"
	out := relabel(in, "gemma")

	// Comments untouched.
	if !strings.Contains(out, "# HELP foo help text") {
		t.Errorf("comment altered: %s", out)
	}
	// Label added to a plain sample.
	if !strings.Contains(out, `foo{model="gemma"} 42`) {
		t.Errorf("plain sample not labelled: %s", out)
	}
	// Label merged into an existing label set.
	if !strings.Contains(out, `bar{le="0.5",model="gemma"} 7`) {
		t.Errorf("labelled sample not merged: %s", out)
	}
}

func TestRelabelEmpty(t *testing.T) {
	if out := relabel("", "m"); strings.TrimSpace(out) != "" {
		t.Errorf("empty input should stay empty, got %q", out)
	}
}

// TestRelabelLlamaMetrics checks every real llama-server metric (including the
// colon in the name) is labelled, and HELP/TYPE comments stay intact.
func TestRelabelLlamaMetrics(t *testing.T) {
	out := relabel(llamaMetrics, "gemma")
	for _, name := range allLlamaMetrics {
		if !strings.Contains(out, name+`{model="gemma"}`) {
			t.Errorf("metric %q not labelled: sample missing from output", name)
		}
		if !strings.Contains(out, "# TYPE "+name+" ") {
			t.Errorf("metric %q TYPE comment missing", name)
		}
	}
}

// TestHandlerBundlesAllMetrics is the end-to-end guard: a gzip-requesting scrape
// must return an uncompressed body containing the manager's own metrics plus
// every bundled llama-server metric, labelled with the model.
func TestHandlerBundlesAllMetrics(t *testing.T) {
	instance := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/metrics" {
			t.Errorf("unexpected instance path %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(llamaMetrics))
	}))
	defer instance.Close()

	m := New()
	m.RecordRequest("gemma", 200, 10, 5, 250*time.Millisecond) // seed an own metric
	h := m.Handler(func() (string, string, bool) {
		return "gemma", instance.URL, true
	})

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if enc := rec.Header().Get("Content-Encoding"); enc != "" {
		t.Fatalf("response is compressed (%q); bundled metrics would be lost", enc)
	}
	body := rec.Body.String()

	if !strings.Contains(body, "lim_requests_total") {
		t.Errorf("manager's own metrics missing from output")
	}
	for _, name := range allLlamaMetrics {
		if !strings.Contains(body, name+`{model="gemma"}`) {
			t.Errorf("bundled metric %q missing from /metrics output", name)
		}
	}
}

// TestHandlerNoInstance emits only the manager's own metrics when nothing runs.
func TestHandlerNoInstance(t *testing.T) {
	m := New()
	h := m.Handler(func() (string, string, bool) { return "", "", false })

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, "lim_queue_depth") {
		t.Errorf("own metrics missing: %s", body)
	}
	if strings.Contains(body, "llamacpp:") {
		t.Errorf("no instance running but llama metrics present: %s", body)
	}
}
