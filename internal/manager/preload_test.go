package manager

import (
	"os"
	"testing"
)

// testBinCmd is a cmd that re-execs the test binary as a fake healthy server.
func testBinCmd() string {
	os.Setenv("LIM_HELPER", "1")
	return os.Args[0] + " -test.run=TestHelperProcess -- --port ${PORT}"
}

func TestPreloadLoadsAndStops(t *testing.T) {
	m := New(fakeConfig(t, 10, 300, "a", "b"), nil)
	t.Cleanup(m.Stop)

	// Positive: preloading healthy models completes and leaves nothing running,
	// since each model is stopped again after its health check passes.
	m.Preload([]string{"a", "b"})
	if s := m.Snapshot(); s.Running {
		t.Errorf("instance still running after preload: %+v", s)
	}
}

func TestPreloadContinuesOnFailure(t *testing.T) {
	// "bad" never serves /health (health_timeout 1s) and must not hang preload or
	// leave a dangling instance; "a" is healthy and still gets preloaded.
	cfg := loadConfig(t, "manager:\n  health_timeout: 1\nmodels:\n"+
		"  bad:\n    cmd: \"sleep 5\"\n"+
		"  a:\n    cmd: \""+testBinCmd()+"\"\n")
	m := New(cfg, nil)
	t.Cleanup(m.Stop)

	m.Preload([]string{"bad", "a"})
	if s := m.Snapshot(); s.Running {
		t.Errorf("instance still running after preload: %+v", s)
	}
}

func TestRepoCached(t *testing.T) {
	cached := map[string]bool{
		"unsloth/gemma-4-12b-it-GGUF:Q4_K_M":      true,
		"unsloth/gemma-4-E4B-it-qat-GGUF:Q4_K_XL": true,
	}
	// Positive: exact match.
	if !repoCached(cached, "unsloth/gemma-4-12b-it-GGUF:Q4_K_M") {
		t.Errorf("exact repo should be reported cached")
	}
	// Positive: config keeps unsloth's UD- prefix that --cache-list drops.
	if !repoCached(cached, "unsloth/gemma-4-E4B-it-qat-GGUF:UD-Q4_K_XL") {
		t.Errorf("UD- prefixed quant should match its normalized cache entry")
	}
	// Negative: unknown repo.
	if repoCached(cached, "unsloth/other-GGUF:Q4_K_M") {
		t.Errorf("unknown repo must not be reported cached")
	}
}
