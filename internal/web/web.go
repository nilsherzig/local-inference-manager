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
	"io"
	"io/fs"
	"log"
	"net/http"
	"sort"
	"strings"
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

	pages     map[string]*template.Template
	fragments *template.Template
}

// New builds the web server and parses templates.
func New(cfg *config.Config, mgr *manager.Manager, tokens store.TokenStore, logs store.RequestLogStore, bus *events.Bus) *Server {
	fm := template.FuncMap{
		"join": strings.Join,
		"secs": func(ms int64) float64 { return float64(ms) / 1000 },
		// prettyJSON indents a JSON string; non-JSON input is returned unchanged.
		"prettyJSON": func(s string) string {
			var buf bytes.Buffer
			if err := json.Indent(&buf, []byte(s), "", "  "); err != nil {
				return s
			}
			return buf.String()
		},
		// humanDur renders an uptime as the largest sensible unit (s/m/h/d).
		"humanDur": func(d time.Duration) string {
			switch {
			case d < time.Minute:
				return fmt.Sprintf("%ds", int(d.Seconds()))
			case d < time.Hour:
				return fmt.Sprintf("%dm", int(d.Minutes()))
			case d < 24*time.Hour:
				return fmt.Sprintf("%dh", int(d.Hours()))
			default:
				return fmt.Sprintf("%dd", int(d.Hours()/24))
			}
		},
		"pct": func(num, den int) float64 {
			if den == 0 {
				return 0
			}
			return float64(num) / float64(den) * 100
		},
	}
	base := []string{"templates/layout.gohtml", "templates/fragments.gohtml"}
	pages := map[string]*template.Template{}
	for _, p := range []string{"dashboard", "instances", "tokens", "token_detail", "playground", "request_detail"} {
		files := append(append([]string{}, base...), "templates/"+p+".gohtml")
		pages[p] = template.Must(template.New("").Funcs(fm).ParseFS(content, files...))
	}
	fragments := template.Must(template.New("").Funcs(fm).ParseFS(content, "templates/fragments.gohtml"))

	return &Server{
		cfg: cfg, mgr: mgr, tokens: tokens, logs: logs, bus: bus,
		pages: pages, fragments: fragments,
	}
}

// Routes registers the web + SSE handlers on mux.
func (s *Server) Routes(mux *http.ServeMux) {
	static, _ := fs.Sub(content, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(static))))

	mux.HandleFunc("GET /{$}", s.dashboard)
	mux.HandleFunc("GET /instances", s.instances)
	mux.HandleFunc("GET /instances/logs", s.instanceLogs)
	mux.HandleFunc("GET /instances/logs.txt", s.instanceLogsText)
	mux.HandleFunc("POST /instances/{model}/start", s.startInstance)
	mux.HandleFunc("POST /instances/stop", s.stopInstance)
	mux.HandleFunc("GET /tokens", s.tokensPage)
	mux.HandleFunc("POST /tokens", s.createToken)
	mux.HandleFunc("GET /tokens/{id}", s.tokenDetail)
	mux.HandleFunc("POST /tokens/{id}/revoke", s.revokeToken)
	mux.HandleFunc("GET /test", s.playground)
	mux.HandleFunc("GET /requests/{id}", s.requestDetail)
	mux.HandleFunc("GET /events", s.events)
}

// modelStatus is the per-model view used on the instances page.
type modelStatus struct {
	Name    string
	Aliases []string
	TTL     int
	Active  bool
	Cmd     string // full llama-server command line (${PORT} unsubstituted)
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
			Cmd:     strings.TrimSpace(s.cfg.Models[name].Cmd),
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

// instanceLogs renders the live process log fragment for the running instance.
// The instances page polls this endpoint (htmx) to keep the log fresh.
func (s *Server) instanceLogs(w http.ResponseWriter, r *http.Request) {
	s.renderFragment(w, "instanceLogs", s.mgr.Snapshot())
}

// instanceLogsText serves the current process log as a plain-text snapshot, so
// the user can open it fullscreen in a browser tab. It does not stream.
func (s *Server) instanceLogsText(w http.ResponseWriter, r *http.Request) {
	snap := s.mgr.Snapshot()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if !snap.Running {
		fmt.Fprintln(w, "No instance running.")
		return
	}
	fmt.Fprintf(w, "# %s (port %d, %s)\n\n", snap.Model, snap.Port, snap.State)
	io.WriteString(w, snap.Logs)
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

// tokenRow is a token plus its lifetime input/output token totals (no cache).
type tokenRow struct {
	store.APIToken
	In  int64
	Out int64
}

// tokenRows lists every token with its aggregated prompt/predicted token counts.
func (s *Server) tokenRows() []tokenRow {
	tokens, _ := s.tokens.List()
	rows := make([]tokenRow, 0, len(tokens))
	for _, t := range tokens {
		var in, out int64
		if s.logs != nil {
			if st, err := s.logs.StatsByToken(t.ID); err == nil {
				in, out = st.PromptTokens, st.PredictedTokens
			}
		}
		rows = append(rows, tokenRow{APIToken: t, In: in, Out: out})
	}
	return rows
}

func (s *Server) tokensPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, "tokens", map[string]any{
		"Active": "tokens",
		"Tokens": s.tokenRows(),
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
	s.render(w, "tokens", map[string]any{
		"Active":   "tokens",
		"Tokens":   s.tokenRows(),
		"NewToken": plaintext,
		"NewName":  name,
	})
}

func (s *Server) tokenDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	tok, err := s.tokens.Token(id)
	if err != nil || tok == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	stats, _ := s.logs.StatsByToken(id)
	recent, _ := s.logs.RecentByToken(id, 50)
	s.render(w, "token_detail", map[string]any{
		"Active": "tokens",
		"Token":  tok,
		"Stats":  stats,
		"Recent": recent,
	})
}

func (s *Server) revokeToken(w http.ResponseWriter, r *http.Request) {
	_ = s.tokens.Revoke(r.PathValue("id"))
	http.Redirect(w, r, "/tokens", http.StatusSeeOther)
}

// playground renders the Test page: it lists the models and shows a copy-ready
// curl command. The request itself is run by the user via curl, not in-process.
func (s *Server) playground(w http.ResponseWriter, r *http.Request) {
	names := s.cfg.ModelNames()
	sort.Strings(names)
	s.render(w, "playground", map[string]any{
		"Active": "playground",
		"Models": names,
		"Listen": s.cfg.Manager.Listen,
	})
}

func (s *Server) renderFragment(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.fragments.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) requestDetail(w http.ResponseWriter, r *http.Request) {
	log, err := s.logs.Get(r.PathValue("id"))
	if err != nil || log == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	s.render(w, "request_detail", map[string]any{
		"Active": "dashboard",
		"Log":    log,
	})
}
