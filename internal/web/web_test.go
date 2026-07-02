package web

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

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

// fakeLogStore is an in-memory RequestLogStore for the token-detail tests.
type fakeLogStore struct {
	stats  store.TokenStats
	recent []store.RequestLog
	get    *store.RequestLog
}

func (f *fakeLogStore) Save(*store.RequestLog) error           { return nil }
func (f *fakeLogStore) Recent(int) ([]store.RequestLog, error) { return f.recent, nil }
func (f *fakeLogStore) Get(string) (*store.RequestLog, error)  { return f.get, nil }
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

// newTestServer wires a Server backed by the given token store and an in-memory
// config. logs/bus are nil: the exercised render paths never touch them.
func newTestServer(t *testing.T, tokens store.TokenStore) *Server {
	t.Helper()
	cfg := testConfig(t)
	return New(cfg, manager.New(cfg, nil), tokens, nil, nil)
}

func TestRequestRowShowsSecondsNotMillis(t *testing.T) {
	s := newTestServer(t, newFakeTokenStore())
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
	s := newTestServer(t, newFakeTokenStore())
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
	s := newTestServer(t, newFakeTokenStore())
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

func TestInstancesPageShowsConfigAndLogs(t *testing.T) {
	s := newTestServer(t, newFakeTokenStore())
	req := httptest.NewRequest(http.MethodGet, "/instances", nil)
	rec := httptest.NewRecorder()

	s.instances(rec, req)

	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	// The full llama-server command line from the config must be rendered.
	if !strings.Contains(body, "llama-server --port ${PORT}") {
		t.Errorf("instances page missing model config: %q", body)
	}
	if !strings.Contains(body, "Live instance logs") || !strings.Contains(body, "/instances/logs.txt") {
		t.Errorf("instances page missing live logs section: %q", body)
	}
}

func TestInstanceLogsTextNoInstance(t *testing.T) {
	s := newTestServer(t, newFakeTokenStore())
	req := httptest.NewRequest(http.MethodGet, "/instances/logs.txt", nil)
	rec := httptest.NewRecorder()

	s.instanceLogsText(rec, req)

	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("content-type = %q, want text/plain", ct)
	}
	if !strings.Contains(rec.Body.String(), "No instance running.") {
		t.Errorf("body = %q, want 'No instance running.'", rec.Body.String())
	}
}

func TestTokensPageShowsInOutTokens(t *testing.T) {
	tokens := newFakeTokenStore()
	tokens.Create("ci")
	cfg := testConfig(t)
	logs := &fakeLogStore{stats: store.TokenStats{PromptTokens: 120, PredictedTokens: 45, CacheTokens: 9999}}
	s := New(cfg, manager.New(cfg, nil), tokens, logs, nil)

	req := httptest.NewRequest(http.MethodGet, "/tokens", nil)
	rec := httptest.NewRecorder()
	s.tokensPage(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, ">120<") || !strings.Contains(body, ">45<") {
		t.Errorf("tokens table missing in/out counts:\n%s", body)
	}
	// Cache tokens must not leak into the in/out columns.
	if strings.Contains(body, "9999") {
		t.Errorf("cache tokens should not be shown in the tokens table")
	}
}

func TestRequestDetailPrettyPrintsJSON(t *testing.T) {
	cfg := testConfig(t)
	logs := &fakeLogStore{get: &store.RequestLog{
		ID:           "r1",
		RequestBody:  `{"model":"gemma","stream":false}`,
		ResponseBody: `not json`,
	}}
	s := New(cfg, manager.New(cfg, nil), newFakeTokenStore(), logs, nil)

	req := httptest.NewRequest(http.MethodGet, "/requests/r1", nil)
	req.SetPathValue("id", "r1")
	rec := httptest.NewRecorder()
	s.requestDetail(rec, req)

	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	// Valid JSON is indented (a newline + two-space indent before the key).
	if !strings.Contains(body, "{\n  &#34;model&#34;: &#34;gemma&#34;") {
		t.Errorf("request body not pretty-printed:\n%s", body)
	}
	// Non-JSON is left untouched.
	if !strings.Contains(body, "not json") {
		t.Errorf("non-JSON response body should pass through: %s", body)
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
	s := New(cfg, manager.New(cfg, nil), tokens, logs, nil)

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
	s := New(cfg, manager.New(cfg, nil), tokens, &fakeLogStore{}, nil)

	req := httptest.NewRequest(http.MethodGet, "/tokens/999", nil)
	req.SetPathValue("id", "999")
	rec := httptest.NewRecorder()
	s.tokenDetail(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestPlaygroundPageListsModels(t *testing.T) {
	s := newTestServer(t, newFakeTokenStore())
	req := httptest.NewRequest(http.MethodGet, "/playground", nil)
	rec := httptest.NewRecorder()

	s.playground(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, `value="gemma"`) {
		t.Errorf("page missing model option: %q", body)
	}
	if !strings.Contains(body, ">Test<") {
		t.Errorf("page missing heading")
	}
	if !strings.Contains(body, "/v1/chat/completions") {
		t.Errorf("page missing equivalent curl command")
	}
}
