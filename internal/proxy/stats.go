package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"sync"
)

// timings mirrors the llama-server "timings" object.
type timings struct {
	CacheN             int     `json:"cache_n"` // prompt tokens served from cache (not reprocessed)
	PromptN            int     `json:"prompt_n"`
	PredictedN         int     `json:"predicted_n"`
	PromptPerSecond    float64 `json:"prompt_per_second"`
	PredictedPerSecond float64 `json:"predicted_per_second"`
	DraftN             int     `json:"draft_n"`          // draft tokens proposed (speculative decoding)
	DraftNAccepted     int     `json:"draft_n_accepted"` // draft tokens accepted
}

type llamaResp struct {
	Timings *timings `json:"timings"`
	Usage   *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

// stats is the extracted per-request statistics.
type stats struct {
	CacheN          int
	PromptN         int
	PredictedN      int
	PromptPerSec    float64
	PredictedPerSec float64
	DraftN          int
	DraftNAccepted  int
}

// extractStats pulls timings from a proxied response body. It handles both the
// non-stream JSON body and the SSE stream (where llama-server appends timings to
// the final data chunk). A body without timings yields zero stats, never panics.
func extractStats(body []byte, contentType string) stats {
	if strings.Contains(contentType, "text/event-stream") {
		return extractStreamStats(body)
	}
	return fromLlamaResp(body)
}

func fromLlamaResp(data []byte) stats {
	var r llamaResp
	if err := json.Unmarshal(data, &r); err != nil {
		return stats{}
	}
	var s stats
	if r.Timings != nil {
		s.CacheN = r.Timings.CacheN
		s.PromptN = r.Timings.PromptN
		s.PredictedN = r.Timings.PredictedN
		s.PromptPerSec = r.Timings.PromptPerSecond
		s.PredictedPerSec = r.Timings.PredictedPerSecond
		s.DraftN = r.Timings.DraftN
		s.DraftNAccepted = r.Timings.DraftNAccepted
	}
	if s.PromptN == 0 && r.Usage != nil {
		s.PromptN = r.Usage.PromptTokens
		s.PredictedN = r.Usage.CompletionTokens
	}
	return s
}

// extractStreamStats scans SSE "data:" lines and returns the stats from the last
// chunk that carries a timings/usage object.
func extractStreamStats(body []byte) stats {
	var last stats
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		if s := fromLlamaResp([]byte(payload)); s.PredictedN > 0 || s.PromptN > 0 || s.CacheN > 0 {
			last = s
		}
	}
	return last
}

// captureBody wraps a response body, buffering it and running finalize exactly
// once when the body is fully read or closed.
type captureBody struct {
	rc       io.ReadCloser
	buf      bytes.Buffer
	once     sync.Once
	finalize func(body []byte)
}

func (c *captureBody) Read(p []byte) (int, error) {
	n, err := c.rc.Read(p)
	if n > 0 {
		c.buf.Write(p[:n])
	}
	if err == io.EOF {
		c.done()
	}
	return n, err
}

func (c *captureBody) Close() error {
	c.done()
	return c.rc.Close()
}

func (c *captureBody) done() {
	c.once.Do(func() { c.finalize(c.buf.Bytes()) })
}
