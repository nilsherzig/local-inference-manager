package gormstore

import (
	"path/filepath"
	"testing"

	"github.com/nilsherzig/local-inference-manager/internal/store"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return s
}

func TestTokenCreateLookupRevoke(t *testing.T) {
	s := open(t)

	plaintext, tok, err := s.Create("ci")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if tok.Hash == plaintext || tok.Hash == "" {
		t.Errorf("token stored in plaintext or empty hash")
	}

	// Positive: valid token resolves.
	got, err := s.Lookup(plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.ID != tok.ID {
		t.Fatalf("lookup returned %+v", got)
	}

	// Negative: unknown token resolves to nil.
	if got, _ := s.Lookup("lim_bogus"); got != nil {
		t.Errorf("unknown token should not resolve: %+v", got)
	}

	// Negative: revoked token no longer resolves.
	if err := s.Revoke(tok.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Lookup(plaintext); got != nil {
		t.Errorf("revoked token should not resolve: %+v", got)
	}
}

func TestRequestLogSaveRecentGet(t *testing.T) {
	s := open(t)

	log := &store.RequestLog{Model: "gemma", Endpoint: "/v1/chat/completions", Status: 200, PredictedN: 42}
	if err := s.Save(log); err != nil {
		t.Fatalf("save: %v", err)
	}
	if log.ID == "" {
		t.Errorf("id not populated after save")
	}

	recent, err := s.Recent(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 1 || recent[0].Model != "gemma" {
		t.Errorf("recent = %+v", recent)
	}

	got, err := s.Get(log.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.PredictedN != 42 {
		t.Errorf("get = %+v", got)
	}

	// Negative: missing id resolves to nil.
	if got, _ := s.Get("no-such-id"); got != nil {
		t.Errorf("missing log should be nil: %+v", got)
	}
}

func TestTokenGet(t *testing.T) {
	s := open(t)
	_, tok, _ := s.Create("ci")

	got, err := s.Token(tok.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Name != "ci" {
		t.Errorf("Token(%q) = %+v", tok.ID, got)
	}
	// Negative: unknown id resolves to nil.
	if got, _ := s.Token("no-such-id"); got != nil {
		t.Errorf("unknown token id should be nil: %+v", got)
	}
}

func TestStatsByToken(t *testing.T) {
	s := open(t)
	id := "token-1"
	other := "token-2"

	// Two requests for token 1, one for token 2, one with no token.
	for _, l := range []*store.RequestLog{
		{Model: "gemma", TokenID: &id, PromptN: 10, PredictedN: 5, CacheN: 100},
		{Model: "gemma", TokenID: &id, PromptN: 20, PredictedN: 7, CacheN: 200},
		{Model: "gemma", TokenID: &other, PromptN: 99, PredictedN: 99, CacheN: 99},
		{Model: "gemma", PromptN: 1, PredictedN: 1},
	} {
		if err := s.Save(l); err != nil {
			t.Fatal(err)
		}
	}

	st, err := s.StatsByToken(id)
	if err != nil {
		t.Fatal(err)
	}
	if st.Requests != 2 {
		t.Errorf("requests = %d, want 2", st.Requests)
	}
	if st.PromptTokens != 30 || st.PredictedTokens != 12 || st.CacheTokens != 300 {
		t.Errorf("sums wrong: %+v", st)
	}
	if st.LastUsed == nil {
		t.Errorf("last used should be set")
	}

	recent, err := s.RecentByToken(id, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 2 {
		t.Errorf("recent for token = %d rows, want 2", len(recent))
	}

	// Negative: a token with no requests yields zeroes and a nil LastUsed.
	empty, err := s.StatsByToken("unused-token")
	if err != nil {
		t.Fatal(err)
	}
	if empty.Requests != 0 || empty.PromptTokens != 0 || empty.LastUsed != nil {
		t.Errorf("empty stats should be zero/nil: %+v", empty)
	}
}
