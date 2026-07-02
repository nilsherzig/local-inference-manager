package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nilsherzig/local-inference-manager/internal/store"
)

// fakeTokens implements store.TokenStore; only Lookup is exercised.
type fakeTokens struct {
	valid string
	id    uint
}

func (f *fakeTokens) Create(string) (string, *store.APIToken, error) { return "", nil, nil }
func (f *fakeTokens) List() ([]store.APIToken, error)                { return nil, nil }
func (f *fakeTokens) Revoke(uint) error                              { return nil }
func (f *fakeTokens) Lookup(plaintext string) (*store.APIToken, error) {
	if plaintext == f.valid {
		return &store.APIToken{ID: f.id}, nil
	}
	return nil, nil
}

func protectedHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id := TokenID(r.Context()); id == nil {
			t := "no token in ctx"
			http.Error(w, t, http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
}

func TestValidTokenPasses(t *testing.T) {
	h := Middleware(&fakeTokens{valid: "secret", id: 7})(protectedHandler())
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestMissingTokenRejected(t *testing.T) {
	h := Middleware(&fakeTokens{valid: "secret"})(protectedHandler())
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestInvalidTokenRejected(t *testing.T) {
	h := Middleware(&fakeTokens{valid: "secret"})(protectedHandler())
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}
