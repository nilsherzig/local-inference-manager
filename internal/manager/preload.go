package manager

import (
	"log"
	"os"
	"os/exec"
	"strings"

	"github.com/nilsherzig/local-inference-manager/internal/config"
)

// hfCLI is the HuggingFace downloader binary. The classic `huggingface-cli` was
// removed in recent huggingface_hub releases (it now just prints a deprecation
// notice and exits non-zero); `hf` is the replacement and takes the same
// download flags.
const hfCLI = "hf"

// hfXetEnv turns on the high-performance Xet backend, which splits each file
// into content-defined chunks and fetches them in parallel. On a high-bandwidth
// link this lifts the single-stream cap (~10MB/s in practice) to >1GB/s. It
// replaces the now-deprecated HF_HUB_ENABLE_HF_TRANSFER; needs the hf_xet
// package installed (the image adds it), otherwise it is a harmless no-op.
const hfXetEnv = "HF_XET_HIGH_PERFORMANCE=1"

// Preload downloads every configured model's files before the server starts
// serving, using the HuggingFace CLI. Downloads are visible up front (native
// progress bar, fast Xet backend) instead of blocking the first user request.
// hf download is idempotent: files already on disk are verified by hash and
// skipped, so restarts stay fast. Models with no `downloads` entries are assumed
// to already have their -m paths on disk and are skipped.
//
// A model may declare several entries (main model, speculative-decoding drafter,
// multimodal projector, ...). Every entry is fetched; a failure on one is logged
// and preload moves on, so one missing repo never blocks the rest.
func (m *Manager) Preload(models []string) {
	for _, name := range models {
		dls := m.cfg.Downloads(name)
		if len(dls) == 0 {
			log.Printf("preload: %q has no downloads, skipping", name)
			continue
		}
		ok := true
		for _, d := range dls {
			if err := m.downloader(d); err != nil {
				log.Printf("preload: %q download %s failed: %v", name, downloadDesc(d), err)
				ok = false
				continue
			}
		}
		if ok {
			log.Printf("preload: %q ready (%d file set(s) present)", name, len(dls))
		}
	}
}

// hfDownload runs `hf download` for one entry, streaming its output (including
// the progress bar) straight to the manager's stdout/stderr so the user sees
// live download progress during startup. It is the default value of
// m.downloader; tests substitute a fake.
func (m *Manager) hfDownload(d config.Download) error {
	dir := m.cfg.LocalDir(d.Repo)
	args := downloadArgs(d, dir)
	log.Printf("preload: downloading %s -> %s", downloadDesc(d), dir)

	cmd := exec.Command(hfCLI, args...)
	cmd.Env = append(os.Environ(), hfXetEnv)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// downloadArgs builds the `hf download` argv for one entry. A quant tag is
// translated into an --include glob so only the matching GGUF shards are pulled;
// without a quant the whole repo is fetched. The file lands under dir, which the
// cmd's -m path must reference.
func downloadArgs(d config.Download, dir string) []string {
	args := []string{"download", d.Repo, "--local-dir", dir}
	if d.Quant != "" {
		args = append(args, "--include", "*"+d.Quant+"*.gguf")
	}
	return args
}

// downloadDesc renders an entry as "repo:quant" (or just "repo") for logs.
func downloadDesc(d config.Download) string {
	if d.Quant == "" {
		return d.Repo
	}
	return strings.Join([]string{d.Repo, d.Quant}, ":")
}
