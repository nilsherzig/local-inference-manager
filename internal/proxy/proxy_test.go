package proxy

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/nilsherzig/local-inference-manager/internal/config"
	"github.com/nilsherzig/local-inference-manager/internal/events"
	"github.com/nilsherzig/local-inference-manager/internal/manager"
	"github.com/nilsherzig/local-inference-manager/internal/metrics"
	"github.com/nilsherzig/local-inference-manager/internal/store"
)

// TestHelperProcess is re-executed as a fake llama-server serving /health and a
// chat-completions response carrying timings.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("LIM_HELPER") != "1" {
		return
	}
	port := ""
	for i, a := range os.Args {
		if a == "--port" && i+1 < len(os.Args) {
			port = os.Args[i+1]
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"hi"}}],"timings":{"prompt_n":8,"predicted_n":3,"predicted_per_second":40.0}}`)
	})
	srv := &http.Server{Addr: "127.0.0.1:" + port, Handler: mux}
	go func() {
		c := make(chan os.Signal, 1)
		signal.Notify(c, syscall.SIGTERM)
		<-c
		_ = srv.Close()
	}()
	_ = srv.ListenAndServe()
	os.Exit(0)
}

// memLogStore is an in-memory RequestLogStore for tests.
type memLogStore struct {
	mu   sync.Mutex
	logs []store.RequestLog
}

func (m *memLogStore) Save(l *store.RequestLog) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	l.ID = fmt.Sprintf("req-%d", len(m.logs)+1)
	m.logs = append(m.logs, *l)
	return nil
}
func (m *memLogStore) Recent(int) ([]store.RequestLog, error)   { return m.logs, nil }
func (m *memLogStore) Get(id string) (*store.RequestLog, error) { return nil, nil }
func (m *memLogStore) StatsByToken(string) (store.TokenStats, error) {
	return store.TokenStats{}, nil
}
func (m *memLogStore) RecentByToken(string, int) ([]store.RequestLog, error) { return nil, nil }
func (m *memLogStore) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.logs)
}

func newProxy(t *testing.T, models ...string) (*Proxy, *memLogStore) {
	t.Helper()
	os.Setenv("LIM_HELPER", "1")
	bin := os.Args[0]
	var sb strings.Builder
	sb.WriteString("manager:\n  health_timeout: 10\n  log_requests_body: true\nmodels:\n")
	for _, name := range models {
		fmt.Fprintf(&sb, "  %s:\n    cmd: \"%s -test.run=TestHelperProcess -- --port ${PORT}\"\n", name, bin)
	}
	path := filepath.Join(t.TempDir(), "c.yaml")
	if err := os.WriteFile(path, []byte(sb.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	logs := &memLogStore{}
	mgr := manager.New(cfg, nil)
	t.Cleanup(mgr.Stop)
	return New(cfg, mgr, logs, events.New(), metrics.New()), logs
}

func TestOpenAIHappyPathCapturesStats(t *testing.T) {
	p, logs := newProxy(t, "gemma")

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"gemma","messages":[{"role":"user","content":"hi"}]}`))
	rec := httptest.NewRecorder()
	p.OpenAI(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "hi") {
		t.Errorf("unexpected body: %s", rec.Body.String())
	}
	if logs.count() != 1 {
		t.Fatalf("expected 1 log, got %d", logs.count())
	}
	got := logs.logs[0]
	if got.Model != "gemma" || got.PredictedN != 3 || got.PredictedPerSec != 40.0 {
		t.Errorf("captured stats wrong: %+v", got)
	}
}

func TestOpenAIUnknownModel(t *testing.T) {
	p, _ := newProxy(t, "gemma")
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"nope"}`))
	rec := httptest.NewRecorder()
	p.OpenAI(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestOpenAIMissingModel(t *testing.T) {
	p, _ := newProxy(t, "gemma")
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"messages":[]}`))
	rec := httptest.NewRecorder()
	p.OpenAI(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestExtractModel(t *testing.T) {
	if got := extractModel([]byte(`{"model":"x","foo":1}`)); got != "x" {
		t.Errorf("extractModel = %q", got)
	}
	if got := extractModel([]byte(`not json`)); got != "" {
		t.Errorf("extractModel garbage = %q", got)
	}
}
