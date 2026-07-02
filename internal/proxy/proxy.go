// Package proxy resolves the requested model, ensures its instance is running,
// forwards the request to llama-server and captures per-request stats. It adds
// no abstraction over the llama-server API itself.
package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"

	"github.com/nilsherzig/local-inference-manager/internal/auth"
	"github.com/nilsherzig/local-inference-manager/internal/config"
	"github.com/nilsherzig/local-inference-manager/internal/events"
	"github.com/nilsherzig/local-inference-manager/internal/manager"
	"github.com/nilsherzig/local-inference-manager/internal/metrics"
	"github.com/nilsherzig/local-inference-manager/internal/store"
)

const maxBodyBytes = 32 << 20 // 32 MiB

// Proxy holds the dependencies for request routing.
type Proxy struct {
	cfg     *config.Config
	mgr     *manager.Manager
	logs    store.RequestLogStore
	bus     *events.Bus
	metrics *metrics.Metrics
}

// New builds a Proxy.
func New(cfg *config.Config, mgr *manager.Manager, logs store.RequestLogStore, bus *events.Bus, m *metrics.Metrics) *Proxy {
	return &Proxy{cfg: cfg, mgr: mgr, logs: logs, bus: bus, metrics: m}
}

// OpenAI handles the inference endpoints (/v1/chat/completions, /v1/completions,
// /v1/embeddings): extract model → ensure instance → reverse proxy → capture.
func (p *Proxy) OpenAI(w http.ResponseWriter, r *http.Request) {
	reqBody, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes))
	r.Body.Close()
	if err != nil {
		writeError(w, http.StatusBadRequest, "read request body")
		return
	}

	modelName := extractModel(reqBody)
	if modelName == "" {
		writeError(w, http.StatusBadRequest, "missing model field")
		return
	}
	canonical, ok := p.cfg.Resolve(modelName)
	if !ok {
		log.Printf("proxy: %s rejected: unknown model %q", r.URL.Path, modelName)
		writeError(w, http.StatusNotFound, "unknown model: "+modelName)
		return
	}

	log.Printf("proxy: %s -> model %q", r.URL.Path, canonical)
	inst, err := p.mgr.Ensure(canonical)
	if err != nil {
		log.Printf("proxy: ensure %q failed: %v", canonical, err)
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}

	target, _ := url.Parse(inst.BaseURL())
	rp := httputil.NewSingleHostReverseProxy(target)
	start := time.Now()
	tokenID := auth.TokenID(r.Context())

	rp.ModifyResponse = func(resp *http.Response) error {
		ct := resp.Header.Get("Content-Type")
		status := resp.StatusCode
		resp.Body = &captureBody{rc: resp.Body, finalize: func(body []byte) {
			p.record(canonical, r.URL.Path, tokenID, status, start, reqBody, body, ct)
		}}
		return nil
	}
	rp.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		log.Printf("proxy: upstream error for %q: %v", canonical, err)
		writeError(w, http.StatusBadGateway, "upstream error: "+err.Error())
	}

	// Restore the buffered body for the upstream request.
	r.Body = io.NopCloser(bytes.NewReader(reqBody))
	r.ContentLength = int64(len(reqBody))
	rp.ServeHTTP(w, r)
}

// record persists the request log, updates metrics and publishes a live event.
func (p *Proxy) record(model, endpoint string, tokenID *string, status int, start time.Time, reqBody, respBody []byte, contentType string) {
	st := extractStats(respBody, contentType)
	wall := time.Since(start)

	entry := &store.RequestLog{
		Model:           model,
		Endpoint:        endpoint,
		TokenID:         tokenID,
		Status:          status,
		WallMs:          wall.Milliseconds(),
		CacheN:          st.CacheN,
		PromptN:         st.PromptN,
		PredictedN:      st.PredictedN,
		PromptPerSec:    st.PromptPerSec,
		PredictedPerSec: st.PredictedPerSec,
		DraftN:          st.DraftN,
		DraftNAccepted:  st.DraftNAccepted,
	}
	if p.cfg.Manager.LogRequestsBody {
		entry.RequestBody = string(reqBody)
		entry.ResponseBody = string(respBody)
	}
	if err := p.logs.Save(entry); err == nil {
		p.bus.Publish(events.TopicRequests, entry)
	}
	p.metrics.RecordRequest(model, status, st.PromptN, st.PredictedN, wall)
	log.Printf("proxy: %s model=%q status=%d wall=%dms in=%dtok out=%dtok decode=%.1ftok/s",
		endpoint, model, status, wall.Milliseconds(), st.PromptN, st.PredictedN, st.PredictedPerSec)
}

// Models serves the OpenAI-compatible /v1/models list.
func (p *Proxy) Models(w http.ResponseWriter, r *http.Request) {
	type md struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		OwnedBy string `json:"owned_by"`
	}
	data := make([]md, 0)
	for _, name := range p.cfg.ModelNames() {
		data = append(data, md{ID: name, Object: "model", OwnedBy: "llama-server"})
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
}

// ModelStatus is one entry of the dashboard-oriented /models endpoint.
type ModelStatus struct {
	Name    string   `json:"name"`
	Aliases []string `json:"aliases"`
	TTL     int      `json:"ttl"`
	Running bool     `json:"running"`
	Active  bool     `json:"active"`
}

// ModelsStatus serves the /models endpoint (model list + live running state).
func (p *Proxy) ModelsStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, p.ModelStatuses())
}

// ModelStatuses returns per-model status for the API and the UI.
func (p *Proxy) ModelStatuses() []ModelStatus {
	snap := p.mgr.Snapshot()
	out := make([]ModelStatus, 0, len(p.cfg.Models))
	for name, m := range p.cfg.Models {
		active := snap.Running && snap.Model == name
		out = append(out, ModelStatus{
			Name:    name,
			Aliases: m.Aliases,
			TTL:     p.cfg.TTL(name),
			Running: active,
			Active:  active,
		})
	}
	return out
}

// LlamaWebUI reverse-proxies the real llama-server web UI under
// /llama-server/{model}/... starting the instance on demand.
func (p *Proxy) LlamaWebUI(w http.ResponseWriter, r *http.Request) {
	model := r.PathValue("model")
	canonical, ok := p.cfg.Resolve(model)
	if !ok {
		http.Error(w, "unknown model: "+model, http.StatusNotFound)
		return
	}
	inst, err := p.mgr.Ensure(canonical)
	if err != nil {
		log.Printf("proxy: webui ensure %q failed: %v", canonical, err)
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	target, _ := url.Parse(inst.BaseURL())
	rp := httputil.NewSingleHostReverseProxy(target)
	http.StripPrefix("/llama-server/"+model, rp).ServeHTTP(w, r)
}

func extractModel(body []byte) string {
	var v struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(body, &v)
	return v.Model
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"message": msg}})
}
