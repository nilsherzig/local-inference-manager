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
	if log.ID == 0 {
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
	if got, _ := s.Get(9999); got != nil {
		t.Errorf("missing log should be nil: %+v", got)
	}
}
