package metrics

import (
	"github.com/prometheus/client_golang/prometheus"

	"github.com/nilsherzig/local-inference-manager/internal/store"
)

// TokenStatsSource enumerates API tokens and their lifetime request stats. Both
// methods are already satisfied by the gorm store.
type TokenStatsSource interface {
	List() ([]store.APIToken, error)
	StatsByToken(id string) (store.TokenStats, error)
}

// tokenCollector exposes the per-token stats shown at the top of the token page
// (requests, prompt/generated/cache tokens) as Prometheus counters. It reads the
// DB at scrape time so the numbers match the UI exactly and survive restarts.
type tokenCollector struct {
	src       TokenStatsSource
	requests  *prometheus.Desc
	prompt    *prometheus.Desc
	generated *prometheus.Desc
	cache     *prometheus.Desc
}

func newTokenCollector(src TokenStatsSource) *tokenCollector {
	labels := []string{"token", "token_id"}
	return &tokenCollector{
		src:       src,
		requests:  prometheus.NewDesc("lim_token_requests_total", "Requests made with an API token.", labels, nil),
		prompt:    prometheus.NewDesc("lim_token_prompt_tokens_total", "Prompt tokens consumed by an API token.", labels, nil),
		generated: prometheus.NewDesc("lim_token_generated_tokens_total", "Generated (predicted) tokens produced for an API token.", labels, nil),
		cache:     prometheus.NewDesc("lim_token_cache_tokens_total", "Cache tokens attributed to an API token.", labels, nil),
	}
}

func (c *tokenCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.requests
	ch <- c.prompt
	ch <- c.generated
	ch <- c.cache
}

func (c *tokenCollector) Collect(ch chan<- prometheus.Metric) {
	tokens, err := c.src.List()
	if err != nil {
		return
	}
	for _, t := range tokens {
		if t.Revoked {
			continue
		}
		st, err := c.src.StatsByToken(t.ID)
		if err != nil {
			continue
		}
		ch <- prometheus.MustNewConstMetric(c.requests, prometheus.CounterValue, float64(st.Requests), t.Name, t.ID)
		ch <- prometheus.MustNewConstMetric(c.prompt, prometheus.CounterValue, float64(st.PromptTokens), t.Name, t.ID)
		ch <- prometheus.MustNewConstMetric(c.generated, prometheus.CounterValue, float64(st.PredictedTokens), t.Name, t.ID)
		ch <- prometheus.MustNewConstMetric(c.cache, prometheus.CounterValue, float64(st.CacheTokens), t.Name, t.ID)
	}
}
