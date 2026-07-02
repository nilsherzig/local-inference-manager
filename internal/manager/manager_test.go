package manager

import (
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/nilsherzig/local-inference-manager/internal/config"
)

// TestHelperProcess is re-executed by the manager as a fake llama-server. In a
// normal test run (LIM_HELPER unset) it is a no-op.
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
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
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

// fakeConfig builds a config whose models re-exec this test binary as a fake
// healthy server.
func fakeConfig(t *testing.T, healthTimeout, ttl int, models ...string) *config.Config {
	t.Helper()
	os.Setenv("LIM_HELPER", "1") // children inherit → they run TestHelperProcess

	bin := os.Args[0]
	var sb strings.Builder
	fmt.Fprintf(&sb, "manager:\n  health_timeout: %d\n  default_ttl: %d\nmodels:\n", healthTimeout, ttl)
	for _, name := range models {
		fmt.Fprintf(&sb, "  %s:\n    cmd: \"%s -test.run=TestHelperProcess -- --port ${PORT}\"\n", name, bin)
	}
	return loadConfig(t, sb.String())
}

func loadConfig(t *testing.T, yaml string) *config.Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "c.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := config.Load(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	return c
}

func TestEnsureStartsAndStops(t *testing.T) {
	m := New(fakeConfig(t, 10, 300, "a"), nil)
	t.Cleanup(m.Stop)

	inst, err := m.Ensure("a")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if inst.State() != StateReady {
		t.Errorf("state = %q, want ready", inst.State())
	}
	if s := m.Snapshot(); !s.Running || s.Model != "a" {
		t.Errorf("snapshot = %+v", s)
	}

	m.Stop()
	if s := m.Snapshot(); s.Running {
		t.Errorf("still running after Stop: %+v", s)
	}
}

func TestSwapStopsOldInstance(t *testing.T) {
	m := New(fakeConfig(t, 10, 300, "a", "b"), nil)
	t.Cleanup(m.Stop)

	a, err := m.Ensure("a")
	if err != nil {
		t.Fatalf("Ensure a: %v", err)
	}
	oldPort := a.Port

	if _, err := m.Ensure("b"); err != nil {
		t.Fatalf("Ensure b: %v", err)
	}
	if s := m.Snapshot(); s.Model != "b" {
		t.Errorf("model = %q, want b", s.Model)
	}

	// The old instance must no longer answer.
	client := &http.Client{Timeout: time.Second}
	if resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/health", oldPort)); err == nil {
		resp.Body.Close()
		t.Errorf("old instance on port %d still responding", oldPort)
	}
}

func TestEnsureSameModelReusesInstance(t *testing.T) {
	m := New(fakeConfig(t, 10, 300, "a"), nil)
	t.Cleanup(m.Stop)

	first, _ := m.Ensure("a")
	second, err := m.Ensure("a")
	if err != nil {
		t.Fatal(err)
	}
	if first.Port != second.Port {
		t.Errorf("instance was restarted: %d != %d", first.Port, second.Port)
	}
}

func TestIdleStop(t *testing.T) {
	m := New(fakeConfig(t, 10, 1, "a"), nil) // 1s idle ttl
	t.Cleanup(m.Stop)

	if _, err := m.Ensure("a"); err != nil {
		t.Fatal(err)
	}
	// The idle timer fires after 1s, then the instance transitions through
	// "stopping" before it is cleared. Poll until it is actually gone.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if !m.Snapshot().Running {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Errorf("instance not stopped after idle ttl: %+v", m.Snapshot())
}

func TestHealthTimeout(t *testing.T) {
	// A process that never serves /health must fail Ensure.
	cfg := loadConfig(t, "manager:\n  health_timeout: 1\nmodels:\n  slow:\n    cmd: \"sleep 5\"\n")
	m := New(cfg, nil)
	t.Cleanup(m.Stop)

	if _, err := m.Ensure("slow"); err == nil {
		t.Errorf("expected health timeout error")
	}
	if s := m.Snapshot(); s.Running {
		t.Errorf("failed instance should be cleaned up: %+v", s)
	}
}

func TestEnsureUnknownModel(t *testing.T) {
	m := New(fakeConfig(t, 10, 300, "a"), nil)
	t.Cleanup(m.Stop)
	if _, err := m.Ensure("does-not-exist"); err == nil {
		t.Errorf("expected error for unknown model")
	}
}
