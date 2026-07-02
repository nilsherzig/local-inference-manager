// Package web serves the manager dashboard: instances, tokens, a live request
// log (htmx SSE) and request detail. It manages no state of its own; it only
// reflects the manager/store and triggers actions.
package web

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nilsherzig/local-inference-manager/internal/config"
	"github.com/nilsherzig/local-inference-manager/internal/events"
	"github.com/nilsherzig/local-inference-manager/internal/manager"
	"github.com/nilsherzig/local-inference-manager/internal/store"
)

//go:embed templates/*.gohtml static/*
var content embed.FS

// Server renders the dashboard.
type Server struct {
	cfg    *config.Config
	mgr    *manager.Manager
	tokens store.TokenStore
	logs   store.RequestLogStore
	bus    *events.Bus

	// chat is the auth-wrapped /v1/chat/completions handler — the exact same
	// http.Handler the mux serves to external clients. The playground invokes it
	// with a bearer token so its queries take the complete normal path (auth →
	// ensure → forward → stats capture → request log → dashboard).
	chat http.Handler

	pgMu    sync.Mutex // guards pgToken
	pgToken string     // in-memory plaintext of the playground bearer token

	pages     map[string]*template.Template
	fragments *template.Template
}

// playgroundTokenName is the name of the auto-managed playground bearer token.
const playgroundTokenName = "playground"

// New builds the web server and parses templates.
func New(cfg *config.Config, mgr *manager.Manager, tokens store.TokenStore, logs store.RequestLogStore, bus *events.Bus, chat http.Handler) *Server {
	fm := template.FuncMap{
		"join": strings.Join,
	}
	base := []string{"templates/layout.gohtml", "templates/fragments.gohtml"}
	pages := map[string]*template.Template{}
	for _, p := range []string{"dashboard", "instances", "tokens", "playground", "request_detail"} {
		files := append(append([]string{}, base...), "templates/"+p+".gohtml")
		pages[p] = template.Must(template.New("").Funcs(fm).ParseFS(content, files...))
	}
	fragments := template.Must(template.New("").Funcs(fm).ParseFS(content, "templates/fragments.gohtml"))

	return &Server{
		cfg: cfg, mgr: mgr, tokens: tokens, logs: logs, bus: bus, chat: chat,
		pages: pages, fragments: fragments,
	}
}

// Routes registers the web + SSE handlers on mux.
func (s *Server) Routes(mux *http.ServeMux) {
	static, _ := fs.Sub(content, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(static))))

	mux.HandleFunc("GET /{$}", s.dashboard)
	mux.HandleFunc("GET /instances", s.instances)
	mux.HandleFunc("POST /instances/{model}/start", s.startInstance)
	mux.HandleFunc("POST /instances/stop", s.stopInstance)
	mux.HandleFunc("GET /tokens", s.tokensPage)
	mux.HandleFunc("POST /tokens", s.createToken)
	mux.HandleFunc("POST /tokens/{id}/revoke", s.revokeToken)
	mux.HandleFunc("GET /playground", s.playground)
	mux.HandleFunc("POST /playground", s.runPlayground)
	mux.HandleFunc("GET /requests/{id}", s.requestDetail)
	mux.HandleFunc("GET /events", s.events)
}

// modelStatus is the per-model view used on the instances page.
type modelStatus struct {
	Name    string
	Aliases []string
	TTL     int
	Active  bool
}

func (s *Server) modelStatuses() []modelStatus {
	snap := s.mgr.Snapshot()
	out := make([]modelStatus, 0, len(s.cfg.Models))
	for _, name := range s.cfg.ModelNames() {
		out = append(out, modelStatus{
			Name:    name,
			Aliases: s.cfg.Models[name].Aliases,
			TTL:     s.cfg.TTL(name),
			Active:  snap.Running && snap.Model == name,
		})
	}
	return out
}

func (s *Server) render(w http.ResponseWriter, page string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.pages[page].ExecuteTemplate(w, "layout", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	recent, _ := s.logs.Recent(50)
	snap := s.mgr.Snapshot()
	s.render(w, "dashboard", map[string]any{
		"Active":     "dashboard",
		"Snapshot":   snap,
		"QueueDepth": snap.QueueDepth,
		"Recent":     recent,
	})
}

func (s *Server) instances(w http.ResponseWriter, r *http.Request) {
	s.render(w, "instances", map[string]any{
		"Active":   "instances",
		"Models":   s.modelStatuses(),
		"Snapshot": s.mgr.Snapshot(),
	})
}

func (s *Server) startInstance(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("model")
	canonical, ok := s.cfg.Resolve(name)
	if !ok {
		http.Error(w, "unknown model", http.StatusNotFound)
		return
	}
	// Cold start can take a while; do it in the background. The SSE stream
	// reflects state changes as they happen.
	log.Printf("web: manual start of %q requested", canonical)
	go func() {
		if _, err := s.mgr.Ensure(canonical); err != nil {
			log.Printf("web: manual start of %q failed: %v", canonical, err)
		}
	}()
	http.Redirect(w, r, "/instances", http.StatusSeeOther)
}

func (s *Server) stopInstance(w http.ResponseWriter, r *http.Request) {
	log.Println("web: manual stop requested")
	s.mgr.Stop()
	http.Redirect(w, r, "/instances", http.StatusSeeOther)
}

func (s *Server) tokensPage(w http.ResponseWriter, r *http.Request) {
	tokens, _ := s.tokens.List()
	s.render(w, "tokens", map[string]any{
		"Active": "tokens",
		"Tokens": tokens,
	})
}

func (s *Server) createToken(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		name = "unnamed"
	}
	plaintext, _, err := s.tokens.Create(name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	tokens, _ := s.tokens.List()
	s.render(w, "tokens", map[string]any{
		"Active":   "tokens",
		"Tokens":   tokens,
		"NewToken": plaintext,
		"NewName":  name,
	})
}

func (s *Server) revokeToken(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseUint(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	_ = s.tokens.Revoke(uint(id))
	http.Redirect(w, r, "/tokens", http.StatusSeeOther)
}

// playgroundResult is the rendered outcome of a quick test query.
type playgroundResult struct {
	Model           string
	Answer          string
	WallMs          int64
	PromptN         int
	PredictedN      int
	PromptPerSec    float64
	PredictedPerSec float64
	Err             string
}

func (s *Server) playground(w http.ResponseWriter, r *http.Request) {
	names := s.cfg.ModelNames()
	sort.Strings(names)
	s.render(w, "playground", map[string]any{
		"Active": "playground",
		"Models": names,
	})
}

// runPlayground routes a quick test query through the real proxy handler, so it
// takes the exact same path as an external /v1 request (ensure → forward →
// stats capture → request log). The captured response is parsed for display.
func (s *Server) runPlayground(w http.ResponseWriter, r *http.Request) {
	model := r.FormValue("model")
	prompt := strings.TrimSpace(r.FormValue("prompt"))
	res := playgroundResult{Model: model}

	if prompt == "" {
		res.Err = "prompt is empty"
		s.renderFragment(w, "playgroundResult", res)
		return
	}
	log.Printf("playground: query model=%q (%d chars)", model, len(prompt))
	res = s.queryViaProxy(model, prompt)
	if res.Err != "" {
		log.Printf("playground: query model=%q failed: %s", model, res.Err)
	} else {
		log.Printf("playground: query model=%q done in %dms (%d tok out)", model, res.WallMs, res.PredictedN)
	}
	s.renderFragment(w, "playgroundResult", res)
}

// playgroundToken returns a valid plaintext bearer token for playground
// requests, creating one if none exists or the previous one was revoked. It is
// checked before every request, so a user revoking the token in the UI simply
// causes a fresh one to be minted on the next query.
func (s *Server) playgroundToken() (string, error) {
	s.pgMu.Lock()
	defer s.pgMu.Unlock()

	// Reuse the in-memory token if it is still valid (not revoked, still exists).
	if s.pgToken != "" {
		if tok, err := s.tokens.Lookup(s.pgToken); err == nil && tok != nil {
			return s.pgToken, nil
		}
	}

	// Revoke any stale playground tokens so the token list stays tidy.
	if list, err := s.tokens.List(); err == nil {
		for _, t := range list {
			if t.Name == playgroundTokenName && !t.Revoked {
				_ = s.tokens.Revoke(t.ID)
			}
		}
	}

	plaintext, _, err := s.tokens.Create(playgroundTokenName)
	if err != nil {
		return "", err
	}
	s.pgToken = plaintext
	log.Printf("playground: minted new %q token", playgroundTokenName)
	return s.pgToken, nil
}

// queryViaProxy builds an OpenAI chat request, authenticates it with the
// playground bearer token and invokes the same auth-wrapped chat handler the mux
// serves to external clients. It never returns an error; failures land in the
// result's Err field for display.
func (s *Server) queryViaProxy(model, prompt string) playgroundResult {
	res := playgroundResult{Model: model}

	token, err := s.playgroundToken()
	if err != nil {
		res.Err = "playground token: " + err.Error()
		return res
	}

	payload, _ := json.Marshal(map[string]any{
		"model":    model,
		"messages": []map[string]string{{"role": "user", "content": prompt}},
		"stream":   false,
	})
	req, err := http.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(payload))
	if err != nil {
		res.Err = err.Error()
		return res
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	rec := &captureRW{}
	start := time.Now()
	s.chat.ServeHTTP(rec, req)
	res.WallMs = time.Since(start).Milliseconds()
	body := rec.body.Bytes()

	if rec.status >= 400 {
		res.Err = fmt.Sprintf("status %d: %s", rec.status, strings.TrimSpace(string(body)))
		return res
	}

	var cr struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Timings struct {
			PromptN            int     `json:"prompt_n"`
			PredictedN         int     `json:"predicted_n"`
			PromptPerSecond    float64 `json:"prompt_per_second"`
			PredictedPerSecond float64 `json:"predicted_per_second"`
		} `json:"timings"`
	}
	if err := json.Unmarshal(body, &cr); err != nil {
		res.Err = "decode response: " + err.Error()
		return res
	}
	if len(cr.Choices) > 0 {
		res.Answer = cr.Choices[0].Message.Content
	}
	res.PromptN = cr.Timings.PromptN
	res.PredictedN = cr.Timings.PredictedN
	res.PromptPerSec = cr.Timings.PromptPerSecond
	res.PredictedPerSec = cr.Timings.PredictedPerSecond
	return res
}

// captureRW is an in-process http.ResponseWriter that buffers the handler's
// response so the playground can parse it after the proxy has logged the request.
type captureRW struct {
	header http.Header
	body   bytes.Buffer
	status int
}

func (c *captureRW) Header() http.Header {
	if c.header == nil {
		c.header = http.Header{}
	}
	return c.header
}

func (c *captureRW) Write(b []byte) (int, error) { return c.body.Write(b) }
func (c *captureRW) WriteHeader(status int)      { c.status = status }

func (s *Server) renderFragment(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.fragments.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) requestDetail(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseUint(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	log, err := s.logs.Get(uint(id))
	if err != nil || log == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	s.render(w, "request_detail", map[string]any{
		"Active": "dashboard",
		"Log":    log,
	})
}
