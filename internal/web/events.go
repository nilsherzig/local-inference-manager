package web

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"

	"github.com/nilsherzig/local-inference-manager/internal/events"
	"github.com/nilsherzig/local-inference-manager/internal/manager"
	"github.com/nilsherzig/local-inference-manager/internal/store"
)

// events is the single SSE stream. It emits named events (instances, queue,
// requests) whose data is a ready-to-swap HTML fragment.
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch, cancel := s.bus.Subscribe()
	defer cancel()

	// Push current state on connect so a freshly loaded page is accurate.
	snap := s.mgr.Snapshot()
	s.writeSSE(w, flusher, "instances", s.fragment("instanceStatus", snap))
	s.writeSSE(w, flusher, "queue", s.fragment("queueBadge", snap.QueueDepth))

	for {
		select {
		case <-r.Context().Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			switch ev.Topic {
			case events.TopicInstances:
				if snap, ok := ev.Data.(manager.Snapshot); ok {
					s.writeSSE(w, flusher, "instances", s.fragment("instanceStatus", snap))
				}
			case events.TopicQueue:
				s.writeSSE(w, flusher, "queue", s.fragment("queueBadge", ev.Data))
			case events.TopicRequests:
				if log, ok := ev.Data.(*store.RequestLog); ok {
					s.writeSSE(w, flusher, "requests", s.fragment("requestRow", log))
				}
			}
		}
	}
}

// fragment renders a named fragment template to a string.
func (s *Server) fragment(name string, data any) string {
	var buf bytes.Buffer
	if err := s.fragments.ExecuteTemplate(&buf, name, data); err != nil {
		return "<!-- render error -->"
	}
	return buf.String()
}

// writeSSE writes a named event, splitting multi-line HTML into data: lines.
func (s *Server) writeSSE(w http.ResponseWriter, flusher http.Flusher, event, data string) {
	fmt.Fprintf(w, "event: %s\n", event)
	for _, line := range strings.Split(data, "\n") {
		fmt.Fprintf(w, "data: %s\n", line)
	}
	fmt.Fprint(w, "\n")
	flusher.Flush()
}
