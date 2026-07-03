package manager

import (
	"bufio"
	"bytes"
	"log"
	"os/exec"
	"strings"
	"time"
)

// Preload downloads and validates every configured model before the server
// starts serving. For each model it starts the process, waits until /health is
// green (which triggers any HuggingFace download, with progress logged), then
// stops it again. Models already in the llama.cpp cache are skipped so normal
// restarts stay fast; only genuinely missing models are downloaded, visibly and
// up front, instead of during the first user request.
func (m *Manager) Preload(models []string) {
	cached := m.cachedRepos(models)
	for _, name := range models {
		repos := m.cfg.HFRepos(name)
		if len(repos) > 0 && allCached(cached, repos) {
			log.Printf("preload: %q (%s) already cached, skipping", name, strings.Join(repos, ", "))
			continue
		}
		if missing := uncached(cached, repos); len(missing) > 0 {
			log.Printf("preload: downloading %q (%s)", name, strings.Join(missing, ", "))
		} else {
			log.Printf("preload: loading %q", name)
		}
		if err := m.preloadOne(name); err != nil {
			log.Printf("preload: %q failed: %v", name, err)
			continue
		}
		log.Printf("preload: %q ready, stopped", name)
	}
}

// preloadOne starts a model, waits until healthy, then terminates it. It holds
// swapMu for the whole cycle, so it runs in isolation during startup.
func (m *Manager) preloadOne(canonical string) error {
	m.swapMu.Lock()
	defer m.swapMu.Unlock()

	if cur := m.current.Load(); cur != nil {
		m.stopCurrentLocked()
	}

	stop := make(chan struct{})
	go m.logProgress(canonical, stop)
	inst, err := m.startLocked(canonical)
	close(stop)
	if err != nil {
		return err
	}

	m.terminate(inst)
	m.current.Store(nil)
	m.publishInstance()
	return nil
}

// logProgress prints the model's most recent log line every 2s while it starts,
// so download/load progress is visible in the terminal even though the web UI is
// not up yet. LastLine collapses \r progress bars to their latest state.
func (m *Manager) logProgress(canonical string, stop <-chan struct{}) {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	var last string
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			cur := m.current.Load()
			if cur == nil {
				continue
			}
			if line := cur.logs.LastLine(); line != "" && line != last {
				last = line
				log.Printf("preload: %s: %s", canonical, line)
			}
		}
	}
}

// cachedRepos returns the set of HuggingFace repos already in the llama.cpp
// cache, as reported by `<llama-server> --cache-list`. It derives the binary
// from the first model's argv[0]. On any error it returns nil, so every model is
// treated as needing a download (a redundant load, never a wrong result).
func (m *Manager) cachedRepos(models []string) map[string]bool {
	if len(models) == 0 {
		return nil
	}
	args, err := m.cfg.Args(models[0], "0")
	if err != nil {
		return nil
	}
	out, err := exec.Command(args[0], "--cache-list").Output()
	if err != nil {
		return nil
	}

	set := make(map[string]bool)
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		// Lines look like: "   1. unsloth/gemma-4-12b-it-GGUF:Q4_K_M".
		fields := strings.Fields(sc.Text())
		if len(fields) == 0 {
			continue
		}
		if repo := fields[len(fields)-1]; strings.Contains(repo, "/") {
			set[repo] = true
		}
	}
	return set
}

// allCached reports whether every repo in repos is present in the cache set.
func allCached(cached map[string]bool, repos []string) bool {
	for _, r := range repos {
		if !repoCached(cached, r) {
			return false
		}
	}
	return true
}

// uncached returns the subset of repos missing from the cache set.
func uncached(cached map[string]bool, repos []string) []string {
	var missing []string
	for _, r := range repos {
		if !repoCached(cached, r) {
			missing = append(missing, r)
		}
	}
	return missing
}

// repoCached reports whether hf (repo[:quant]) is present in the cache set.
// llama.cpp's --cache-list drops unsloth's "UD-" quant prefix, so a config value
// of "repo:UD-Q4_K_XL" is matched against the cached "repo:Q4_K_XL" too.
func repoCached(cached map[string]bool, hf string) bool {
	if cached[hf] {
		return true
	}
	if i := strings.LastIndex(hf, ":"); i >= 0 {
		normalized := hf[:i] + ":" + strings.TrimPrefix(hf[i+1:], "UD-")
		if cached[normalized] {
			return true
		}
	}
	return false
}
