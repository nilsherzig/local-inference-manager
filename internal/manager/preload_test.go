package manager

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/nilsherzig/local-inference-manager/internal/config"
)

func TestDownloadArgs(t *testing.T) {
	// Positive: a quant becomes an --include glob so only matching shards pull.
	got := downloadArgs(config.Download{Repo: "org/x-GGUF", Quant: "Q4_K_M"}, "/models/org/x-GGUF")
	want := []string{"download", "org/x-GGUF", "--local-dir", "/models/org/x-GGUF", "--include", "*Q4_K_M*.gguf"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("downloadArgs with quant = %v; want %v", got, want)
	}

	// Negative: no quant -> whole repo, no --include filter.
	got = downloadArgs(config.Download{Repo: "org/x-GGUF"}, "/models/org/x-GGUF")
	want = []string{"download", "org/x-GGUF", "--local-dir", "/models/org/x-GGUF"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("downloadArgs without quant = %v; want %v", got, want)
	}
}

func TestPreloadDownloadsEveryEntry(t *testing.T) {
	// A model may need more than one repo (main + drafter); every entry across
	// every model must be fetched, in order.
	cfg := loadConfig(t, "models:\n"+
		"  a:\n    cmd: \"x\"\n    downloads: [\"org/a-GGUF:Q4\"]\n"+
		"  b:\n    cmd: \"y\"\n    downloads: [\"org/b-GGUF:Q4\", \"org/b-draft-GGUF:Q4\"]\n")
	m := New(cfg, nil)

	var got []string
	m.downloader = func(d config.Download) error {
		got = append(got, downloadDesc(d))
		return nil
	}
	m.Preload([]string{"a", "b"})

	want := []string{"org/a-GGUF:Q4", "org/b-GGUF:Q4", "org/b-draft-GGUF:Q4"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("downloaded = %v; want %v", got, want)
	}
}

func TestPreloadSkipsModelsWithoutDownloads(t *testing.T) {
	// Negative: a model whose -m paths are already on disk (no downloads) must
	// not trigger any download.
	cfg := loadConfig(t, "models:\n  a:\n    cmd: \"x\"\n")
	m := New(cfg, nil)

	called := false
	m.downloader = func(d config.Download) error { called = true; return nil }
	m.Preload([]string{"a"})

	if called {
		t.Errorf("model without downloads must not trigger a download")
	}
}

func TestPreloadContinuesOnFailure(t *testing.T) {
	// A failing download must not abort preload: the failing model is logged and
	// the next model is still attempted.
	cfg := loadConfig(t, "models:\n"+
		"  bad:\n    cmd: \"x\"\n    downloads: [\"org/bad-GGUF:Q4\"]\n"+
		"  a:\n    cmd: \"y\"\n    downloads: [\"org/a-GGUF:Q4\"]\n")
	m := New(cfg, nil)

	var attempted []string
	m.downloader = func(d config.Download) error {
		attempted = append(attempted, d.Repo)
		if d.Repo == "org/bad-GGUF" {
			return fmt.Errorf("boom")
		}
		return nil
	}
	m.Preload([]string{"bad", "a"})

	want := []string{"org/bad-GGUF", "org/a-GGUF"}
	if !reflect.DeepEqual(attempted, want) {
		t.Errorf("attempted = %v; want %v (preload must continue past a failure)", attempted, want)
	}
}
