// Command lim is the local inference manager: an on-demand llama-server manager,
// OpenAI-compatible proxy and dashboard.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/nilsherzig/local-inference-manager/internal/auth"
	"github.com/nilsherzig/local-inference-manager/internal/config"
	"github.com/nilsherzig/local-inference-manager/internal/events"
	"github.com/nilsherzig/local-inference-manager/internal/manager"
	"github.com/nilsherzig/local-inference-manager/internal/metrics"
	"github.com/nilsherzig/local-inference-manager/internal/proxy"
	gormstore "github.com/nilsherzig/local-inference-manager/internal/store/gorm"
	"github.com/nilsherzig/local-inference-manager/internal/web"
)

func main() {
	cfgPath := flag.String("config", "config.yaml", "path to the YAML config file")
	showLlamaLogs := flag.Bool("show-llama-logs", false, "mirror each instance's stdout/stderr to this process, prefixed with [model]")
	preload := flag.Bool("preload", true, "download and validate every configured model at startup, before serving")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	log.Printf("config: loaded %q with %d model(s): %s", *cfgPath, len(cfg.Models), strings.Join(cfg.ModelNames(), ", "))

	db, err := gormstore.Open(cfg.Manager.DBPath)
	if err != nil {
		log.Fatalf("store: %v", err)
	}
	log.Printf("store: using sqlite at %q", cfg.Manager.DBPath)

	bus := events.New()
	mets := metrics.New()
	mets.RegisterTokenStats(db)
	var mgrOpts []manager.Option
	if *showLlamaLogs {
		mgrOpts = append(mgrOpts, manager.WithLogStreaming())
		log.Println("config: streaming llama-server logs to stdout/stderr")
	}
	mgr := manager.New(cfg, &publisher{bus: bus, mets: mets, cfg: cfg}, mgrOpts...)

	// Download/validate all models before serving, so downloads are visible up
	// front instead of blocking the first user request.
	if *preload {
		log.Printf("preload: checking %d configured model(s) before serving", len(cfg.Models))
		mgr.Preload(cfg.ModelNames())
		log.Println("preload: all models ready")
	}

	mux := http.NewServeMux()

	px := proxy.New(cfg, mgr, db, bus, mets)
	authMW := auth.Middleware(db)

	chatHandler := authMW(http.HandlerFunc(px.OpenAI))

	// Dashboard + SSE (open).
	web.New(cfg, mgr, db, db, bus).Routes(mux)

	mux.Handle("POST /v1/chat/completions", chatHandler)
	for _, ep := range []string{"/v1/completions", "/v1/embeddings"} {
		mux.Handle("POST "+ep, authMW(http.HandlerFunc(px.OpenAI)))
	}
	mux.Handle("GET /v1/models", authMW(http.HandlerFunc(px.Models)))

	// Open endpoints.
	mux.HandleFunc("GET /models", px.ModelsStatus)
	mux.HandleFunc("/llama-server/{model}/", px.LlamaWebUI)
	mux.Handle("GET /metrics", mets.Handler(func() (string, string, bool) {
		s := mgr.Snapshot()
		if !s.Running {
			return "", "", false
		}
		return s.Model, fmt.Sprintf("http://127.0.0.1:%d", s.Port), true
	}))

	srv := &http.Server{Addr: cfg.Manager.Listen, Handler: mux}

	go func() {
		log.Printf("listening on http://%s", cfg.Manager.Listen)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("serve: %v", err)
		}
	}()

	// Graceful shutdown: stop the llama-server instance, then the HTTP server.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	sig := <-stop
	log.Printf("shutdown: received %s, shutting down", sig)

	if snap := mgr.Snapshot(); snap.Running {
		log.Printf("shutdown: stopping active instance %q (port %d)", snap.Model, snap.Port)
	} else {
		log.Println("shutdown: no active instance to stop")
	}
	mgr.Stop()
	log.Println("shutdown: instance stopped, closing HTTP server")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("shutdown: HTTP server did not close cleanly: %v", err)
	} else {
		log.Println("shutdown: HTTP server closed")
	}
	log.Println("shutdown: complete")
}

// publisher fans manager events out to the SSE bus and updates Prometheus gauges.
type publisher struct {
	bus  *events.Bus
	mets *metrics.Metrics
	cfg  *config.Config
}

func (p *publisher) Publish(topic string, data any) {
	p.bus.Publish(topic, data)
	switch topic {
	case events.TopicQueue:
		if n, ok := data.(int64); ok {
			p.mets.SetQueueDepth(n)
		}
	case events.TopicInstances:
		s, ok := data.(manager.Snapshot)
		if !ok {
			return
		}
		if s.Running {
			p.mets.SetInstanceUp(s.Model, s.State == manager.StateReady)
			return
		}
		// No instance: clear every model's up gauge.
		for _, name := range p.cfg.ModelNames() {
			p.mets.SetInstanceUp(name, false)
		}
	}
}
