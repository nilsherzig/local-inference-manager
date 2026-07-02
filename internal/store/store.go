// Package store defines the persistence domain types and repository interfaces.
// It deliberately has no ORM dependency so the backend (currently gorm/sqlite)
// can be swapped without touching callers.
package store

import "time"

// APIToken is a bearer credential for the /v1 API. Only the hash is persisted.
type APIToken struct {
	ID        uint
	Name      string
	Prefix    string // leading chars of the plaintext, for display only
	Hash      string // sha256 hex of the full token
	CreatedAt time.Time
	Revoked   bool
}

// RequestLog is one proxied request with captured llama-server timings.
type RequestLog struct {
	ID              uint
	CreatedAt       time.Time
	Model           string
	Endpoint        string
	TokenID         *uint
	Status          int
	WallMs          int64
	CacheN          int
	PromptN         int
	PredictedN      int
	PromptPerSec    float64
	PredictedPerSec float64
	DraftN          int
	DraftNAccepted  int
	RequestBody     string
	ResponseBody    string
}

// TokenStore manages API tokens.
type TokenStore interface {
	// Create returns the one-time plaintext token and its stored record.
	Create(name string) (plaintext string, token *APIToken, err error)
	List() ([]APIToken, error)
	// Lookup returns the matching, non-revoked token for a plaintext value.
	Lookup(plaintext string) (*APIToken, error)
	Revoke(id uint) error
}

// RequestLogStore persists and queries request logs.
type RequestLogStore interface {
	Save(log *RequestLog) error
	Recent(limit int) ([]RequestLog, error)
	Get(id uint) (*RequestLog, error)
}
