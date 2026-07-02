package web

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nilsherzig/local-inference-manager/internal/auth"
	"github.com/nilsherzig/local-inference-manager/internal/config"
	"github.com/nilsherzig/local-inference-manager/internal/manager"
	"github.com/nilsherzig/local-inference-manager/internal/store"
)

const testYAML = `
models:
  gemma:
    cmd: "llama-server --port ${PORT}"
    aliases: [g]
`

// fakeTokenStore is an in-memory TokenStore keyed by plaintext.
type fakeTokenStore struct {
	mu    sync.Mutex
	list  []store.APIToken
	plain map[string]string // plaintext -> id
	seq   int
}

func newFakeTokenStore() *fakeTokenStore {
	return &fakeTokenStore{plain: map[string]string{}}
}

func (f *fakeTokenStore) Create(name string) (string, *store.APIToken, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	id := fmt.Sprintf("uuid-%d", f.seq)
	pt := fmt.Sprintf("plain-%d", f.seq)
	tok := store.APIToken{ID: id, Name: name}
	f.list = append(f.list, tok)
	f.plain[pt] = id
	return pt, &tok, nil
}

func (f *fakeTokenStore) List() ([]store.APIToken, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]store.APIToken, len(f.list))
	copy(out, f.list)
	return out, nil
}

func (f *fakeTokenStore) Lookup(pt string) (*store.APIToken, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id, ok := f.plain[pt]
	if !ok {
		return nil, nil
	}
	for i := range f.list {
		if f.list[i].ID == id {
			if f.list[i].Revoked {
				return nil, nil
			}
			t := f.list[i]
			return &t, nil
		}
	}
	return nil, nil
}

func (f *fakeTokenStore) Token(id string) (*store.APIToken, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.list {
		if f.list[i].ID == id {
			t := f.list[i]
			return &t, nil
		}
	}
	return nil, nil
}

func (f *fakeTokenStore) Revoke(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.list {
		if f.list[i].ID == id {
			f.list[i].Revoked = true
		}
	}
	return nil
}

func (f *fakeTokenStore) activePlaygroundTokens() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, t := range f.list {
		if t.Name == playgroundTokenName && !t.Revoked {
			n++
		}
	}
	return n
}

// fakeLogStore is an in-memory RequestLogStore for the token-detail tests.
type fakeLogStore struct {
	stats  store.TokenStats
	recent []store.RequestLog
}

func (f *fakeLogStore) Save(*store.RequestLog) error           { return nil }
func (f *fakeLogStore) Recent(int) ([]store.RequestLog, error) { return f.recent, nil }
func (f *fakeLogStore) Get(string) (*store.RequestLog, error)  { return nil, nil }
func (f *fakeLogStore) StatsByToken(string) (store.TokenStats, error) {
	return f.stats, nil
}
func (f *fakeLogStore) RecentByToken(string, int) ([]store.RequestLog, error) {
	return f.recent, nil
}

func testConfig(t *testing.T) *config.Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(testYAML), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	return cfg
}

// newTestServer wires a Server whose chat handler is the *real* auth middleware
// wrapping final, so tests exercise the complete authenticated path.
func newTestServer(t *testing.T, tokens store.TokenStore, final http.HandlerFunc) *Server {
	t.Helper()
	cfg := testConfig(t)
	chat := auth.Middleware(tokens)(final)
	// logs/bus are nil: the exercised paths never touch them.
	return New(cfg, manager.New(cfg, nil), tokens, nil, nil, chat)
}

func TestPlaygroundMintsTokenAndAuthenticates(t *testing.T) {
	tokens := newFakeTokenStore()
	var gotTokenID *string
	var gotPath string
	final := func(w http.ResponseWriter, r *http.Request) {
		gotTokenID = auth.TokenID(r.Context())
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"hi"}}],"timings":{"predicted_n":3,"predicted_per_second":42.0}}`))
	}
	s := newTestServer(t, tokens, final)

	res := s.queryViaProxy("gemma", "hello")

	if res.Err != "" {
		t.Fatalf("unexpected error: %s", res.Err)
	}
	if gotPath != "/v1/chat/completions" {
		t.Errorf("path = %q, want /v1/chat/completions", gotPath)
	}
	// The request must have passed auth with a real token id.
	if gotTokenID == nil {
		t.Error("handler saw no token id: request did not authenticate")
	}
	if tokens.activePlaygroundTokens() != 1 {
		t.Errorf("active playground tokens = %d, want 1", tokens.activePlaygroundTokens())
	}
	if res.Answer != "hi" || res.PredictedN != 3 {
		t.Errorf("unexpected result: %+v", res)
	}
}

func TestPlaygroundReusesToken(t *testing.T) {
	tokens := newFakeTokenStore()
	final := func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}
	s := newTestServer(t, tokens, final)

	s.queryViaProxy("gemma", "one")
	s.queryViaProxy("gemma", "two")

	if got := len(tokens.list); got != 1 {
		t.Errorf("created %d tokens, want 1 (should be reused)", got)
	}
}

func TestPlaygroundRemintsAfterRevoke(t *testing.T) {
	tokens := newFakeTokenStore()
	authed := 0
	final := func(w http.ResponseWriter, r *http.Request) {
		if auth.TokenID(r.Context()) != nil {
			authed++
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}
	s := newTestServer(t, tokens, final)

	if res := s.queryViaProxy("gemma", "one"); res.Err != "" {
		t.Fatalf("first query failed: %s", res.Err)
	}
	// Simulate the user revoking the token in the UI.
	for _, tok := range tokens.list {
		_ = tokens.Revoke(tok.ID)
	}

	if res := s.queryViaProxy("gemma", "two"); res.Err != "" {
		t.Fatalf("second query failed: %s", res.Err)
	}

	if authed != 2 {
		t.Errorf("authenticated requests = %d, want 2", authed)
	}
	if got := len(tokens.list); got != 2 {
		t.Errorf("total tokens = %d, want 2 (one revoked, one fresh)", got)
	}
	if tokens.activePlaygroundTokens() != 1 {
		t.Errorf("active playground tokens = %d, want 1", tokens.activePlaygroundTokens())
	}
}

func TestQueryViaProxyUpstreamError(t *testing.T) {
	tokens := newFakeTokenStore()
	final := func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"boom"}}`, http.StatusServiceUnavailable)
	}
	s := newTestServer(t, tokens, final)

	res := s.queryViaProxy("gemma", "hi")

	if !strings.Contains(res.Err, "status 503") {
		t.Errorf("err = %q, want status 503", res.Err)
	}
	if res.Answer != "" {
		t.Errorf("answer should be empty, got %q", res.Answer)
	}
}

func TestQueryViaProxyBadJSON(t *testing.T) {
	tokens := newFakeTokenStore()
	final := func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`not json`))
	}
	s := newTestServer(t, tokens, final)

	res := s.queryViaProxy("gemma", "hi")

	if !strings.Contains(res.Err, "decode") {
		t.Errorf("err = %q, want decode error", res.Err)
	}
}

func TestRunPlaygroundEmptyPrompt(t *testing.T) {
	tokens := newFakeTokenStore()
	final := func(w http.ResponseWriter, r *http.Request) {
		t.Error("chat handler should not be called for an empty prompt")
	}
	s := newTestServer(t, tokens, final)

	form := url.Values{"model": {"gemma"}, "prompt": {"   "}}
	req := httptest.NewRequest(http.MethodPost, "/playground", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	s.runPlayground(rec, req)

	if !strings.Contains(rec.Body.String(), "prompt is empty") {
		t.Errorf("body = %q, want 'prompt is empty'", rec.Body.String())
	}
	if len(tokens.list) != 0 {
		t.Errorf("minted %d tokens for empty prompt, want 0", len(tokens.list))
	}
}

func TestRunPlaygroundRendersAnswer(t *testing.T) {
	tokens := newFakeTokenStore()
	final := func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"rendered answer"}}]}`))
	}
	s := newTestServer(t, tokens, final)

	form := url.Values{"model": {"gemma"}, "prompt": {"hi"}}
	req := httptest.NewRequest(http.MethodPost, "/playground", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	s.runPlayground(rec, req)

	if !strings.Contains(rec.Body.String(), "rendered answer") {
		t.Errorf("body = %q, want rendered answer", rec.Body.String())
	}
}

func TestRequestRowShowsSecondsNotMillis(t *testing.T) {
	s := newTestServer(t, newFakeTokenStore(), func(http.ResponseWriter, *http.Request) {})
	out := s.fragment("requestRow", &store.RequestLog{Model: "m", Status: 200, WallMs: 1500})

	if !strings.Contains(out, "1.5s") {
		t.Errorf("row missing seconds: %q", out)
	}
	if strings.Contains(out, "1500ms") {
		t.Errorf("row still shows milliseconds: %q", out)
	}
}

// TestDashboardEmptyRequestLogShowsPlaceholder verifies the empty-state row is
// rendered (as the only child of the tbody) when there are no requests, so the
// .empty-row:not(:only-child) CSS can hide it once a live row is prepended.
func TestDashboardEmptyRequestLogShowsPlaceholder(t *testing.T) {
	s := newTestServer(t, newFakeTokenStore(), func(http.ResponseWriter, *http.Request) {})
	rec := httptest.NewRecorder()

	s.render(rec, "dashboard", map[string]any{
		"Active":     "dashboard",
		"Snapshot":   manager.Snapshot{},
		"QueueDepth": int64(0),
		"Recent":     []store.RequestLog{},
	})
	body := rec.Body.String()

	if !strings.Contains(body, `class="empty-row"`) {
		t.Error("empty request log should render the empty-row placeholder")
	}
	if !strings.Contains(body, `<tbody id="request-log"`) {
		t.Error("live log should render as a table body")
	}
}

// TestDashboardRequestLogRendersRowsAsTable checks existing requests render as
// table rows and no placeholder is emitted.
func TestDashboardRequestLogRendersRowsAsTable(t *testing.T) {
	s := newTestServer(t, newFakeTokenStore(), func(http.ResponseWriter, *http.Request) {})
	rec := httptest.NewRecorder()

	s.render(rec, "dashboard", map[string]any{
		"Active":     "dashboard",
		"Snapshot":   manager.Snapshot{},
		"QueueDepth": int64(0),
		"Recent":     []store.RequestLog{{ID: "req-1", Model: "gemma", Status: 200, WallMs: 1500}},
	})
	body := rec.Body.String()

	if !strings.Contains(body, "<tr") || !strings.Contains(body, "location.href='/requests/req-1'") {
		t.Errorf("request not rendered as a clickable row: %q", body)
	}
	if strings.Contains(body, `class="empty-row"`) {
		t.Error("placeholder should not render when requests exist")
	}
}

func TestTokenDetailShowsStats(t *testing.T) {
	tokens := newFakeTokenStore()
	_, tok, _ := tokens.Create("ci")
	used := time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC)
	logs := &fakeLogStore{
		stats: store.TokenStats{Requests: 3, PromptTokens: 120, PredictedTokens: 45, CacheTokens: 2708, LastUsed: &used},
		recent: []store.RequestLog{
			{ID: "req-9", Model: "gemma", Status: 200, WallMs: 1500, PredictedN: 45},
		},
	}
	cfg := testConfig(t)
	s := New(cfg, manager.New(cfg, nil), tokens, logs, nil, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	req := httptest.NewRequest(http.MethodGet, "/tokens/"+tok.ID, nil)
	req.SetPathValue("id", tok.ID)
	rec := httptest.NewRecorder()
	s.tokenDetail(rec, req)

	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	for _, want := range []string{"ci", "120", "45", "2708", "location.href='/requests/req-9'"} {
		if !strings.Contains(body, want) {
			t.Errorf("token detail missing %q:\n%s", want, body)
		}
	}
}

func TestTokenDetailUnknownToken(t *testing.T) {
	tokens := newFakeTokenStore() // empty
	cfg := testConfig(t)
	s := New(cfg, manager.New(cfg, nil), tokens, &fakeLogStore{}, nil, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	req := httptest.NewRequest(http.MethodGet, "/tokens/999", nil)
	req.SetPathValue("id", "999")
	rec := httptest.NewRecorder()
	s.tokenDetail(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestPlaygroundPageListsModels(t *testing.T) {
	s := newTestServer(t, newFakeTokenStore(), func(http.ResponseWriter, *http.Request) {})
	req := httptest.NewRequest(http.MethodGet, "/playground", nil)
	rec := httptest.NewRecorder()

	s.playground(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, `value="gemma"`) {
		t.Errorf("page missing model option: %q", body)
	}
	if !strings.Contains(body, "Playground") {
		t.Errorf("page missing heading")
	}
}
