package proxy

import "testing"

func TestExtractStatsNonStream(t *testing.T) {
	body := `{"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5},
	          "timings":{"prompt_n":10,"predicted_n":5,"prompt_per_second":100.5,"predicted_per_second":42.0}}`
	s := extractStats([]byte(body), "application/json")
	if s.PromptN != 10 || s.PredictedN != 5 {
		t.Errorf("tokens = %+v", s)
	}
	if s.PredictedPerSec != 42.0 || s.PromptPerSec != 100.5 {
		t.Errorf("speeds = %+v", s)
	}
}

func TestExtractStatsCacheAndDraft(t *testing.T) {
	// A high-cache-hit response with speculative-decoding draft stats.
	body := `{"timings":{"cache_n":2708,"prompt_n":5,"prompt_ms":50.416,"prompt_per_second":99.17,` +
		`"predicted_n":279,"predicted_ms":7395.986,"predicted_per_second":37.72,` +
		`"draft_n":552,"draft_n_accepted":140}}`
	s := extractStats([]byte(body), "application/json")
	if s.CacheN != 2708 {
		t.Errorf("cache_n = %d, want 2708", s.CacheN)
	}
	if s.PromptN != 5 || s.PredictedN != 279 {
		t.Errorf("tokens = %+v", s)
	}
	if s.DraftN != 552 || s.DraftNAccepted != 140 {
		t.Errorf("draft = %d/%d, want 140/552", s.DraftNAccepted, s.DraftN)
	}
}

func TestExtractStatsUsageFallback(t *testing.T) {
	// No timings, but usage present.
	body := `{"usage":{"prompt_tokens":7,"completion_tokens":3}}`
	s := extractStats([]byte(body), "application/json")
	if s.PromptN != 7 || s.PredictedN != 3 {
		t.Errorf("usage fallback failed: %+v", s)
	}
}

func TestExtractStatsStream(t *testing.T) {
	sse := "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n" +
		"data: {\"choices\":[{\"finish_reason\":\"stop\"}],\"timings\":{\"prompt_n\":4,\"predicted_n\":2,\"predicted_per_second\":50.0}}\n\n" +
		"data: [DONE]\n\n"
	s := extractStats([]byte(sse), "text/event-stream")
	if s.PromptN != 4 || s.PredictedN != 2 || s.PredictedPerSec != 50.0 {
		t.Errorf("stream stats = %+v", s)
	}
}

func TestExtractStatsNoTimings(t *testing.T) {
	// Must not panic and returns zero stats.
	s := extractStats([]byte(`{"choices":[]}`), "application/json")
	if s.PromptN != 0 || s.PredictedN != 0 {
		t.Errorf("expected zero stats, got %+v", s)
	}
	if got := extractStats([]byte("not json"), "application/json"); got != (stats{}) {
		t.Errorf("garbage input should yield zero stats, got %+v", got)
	}
}
