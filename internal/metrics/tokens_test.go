package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nilsherzig/local-inference-manager/internal/store"
)

// fakeSource is an in-memory TokenStatsSource for the collector test.
type fakeSource struct {
	tokens []store.APIToken
	stats  map[string]store.TokenStats
	err    error
}

func (f *fakeSource) List() ([]store.APIToken, error) { return f.tokens, f.err }
func (f *fakeSource) StatsByToken(id string) (store.TokenStats, error) {
	return f.stats[id], nil
}

// scrape renders the metrics endpoint into a string.
func scrape(t *testing.T, m *Metrics) string {
	t.Helper()
	h := m.Handler(func() (string, string, bool) { return "", "", false })
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	return rec.Body.String()
}

// TestTokenStatsExposed checks each per-token figure appears with the token's
// name and id as labels.
func TestTokenStatsExposed(t *testing.T) {
	m := New()
	m.RegisterTokenStats(&fakeSource{
		tokens: []store.APIToken{{ID: "id-1", Name: "prod"}},
		stats: map[string]store.TokenStats{
			"id-1": {Requests: 4, PromptTokens: 30965, PredictedTokens: 1249, CacheTokens: 0},
		},
	})

	body := scrape(t, m)
	for _, want := range []string{
		`lim_token_requests_total{token="prod",token_id="id-1"} 4`,
		`lim_token_prompt_tokens_total{token="prod",token_id="id-1"} 30965`,
		`lim_token_generated_tokens_total{token="prod",token_id="id-1"} 1249`,
		`lim_token_cache_tokens_total{token="prod",token_id="id-1"} 0`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing metric line %q in:\n%s", want, body)
		}
	}
}

// TestTokenStatsSkipsRevoked keeps revoked tokens out of the metrics endpoint.
func TestTokenStatsSkipsRevoked(t *testing.T) {
	m := New()
	m.RegisterTokenStats(&fakeSource{
		tokens: []store.APIToken{
			{ID: "id-1", Name: "prod"},
			{ID: "id-2", Name: "old", Revoked: true},
		},
		stats: map[string]store.TokenStats{
			"id-1": {Requests: 4},
			"id-2": {Requests: 9},
		},
	})

	body := scrape(t, m)
	if !strings.Contains(body, `token="prod"`) {
		t.Errorf("active token missing:\n%s", body)
	}
	if strings.Contains(body, `token="old"`) {
		t.Errorf("revoked token exposed:\n%s", body)
	}
}

// TestTokenStatsListError emits no token metrics when the source fails, and does
// not break the rest of the endpoint.
func TestTokenStatsListError(t *testing.T) {
	m := New()
	m.RegisterTokenStats(&fakeSource{err: http.ErrHandlerTimeout})

	body := scrape(t, m)
	if strings.Contains(body, "lim_token_requests_total") {
		t.Errorf("token metrics present despite list error:\n%s", body)
	}
	if !strings.Contains(body, "lim_queue_depth") {
		t.Errorf("own metrics missing after token source error:\n%s", body)
	}
}
