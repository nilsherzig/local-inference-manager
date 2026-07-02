// Package gormstore implements the store interfaces with gorm on top of a
// pure-Go sqlite driver (no cgo).
package gormstore

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/nilsherzig/local-inference-manager/internal/store"
)

// Store is the gorm-backed implementation of the store interfaces.
type Store struct {
	db *gorm.DB
}

// Open opens (and migrates) the sqlite database at path.
func Open(path string) (*Store, error) {
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	if err := db.AutoMigrate(&store.APIToken{}, &store.RequestLog{}); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return &Store{db: db}, nil
}

// hashToken returns the sha256 hex of a plaintext token.
func hashToken(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

// newUUID returns a random RFC 4122 version-4 UUID string.
func newUUID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// Create generates a new random token, stores its hash, and returns the
// one-time plaintext.
func (s *Store) Create(name string) (string, *store.APIToken, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", nil, err
	}
	plaintext := "lim_" + base64.RawURLEncoding.EncodeToString(buf)

	id, err := newUUID()
	if err != nil {
		return "", nil, err
	}
	tok := &store.APIToken{
		ID:     id,
		Name:   name,
		Prefix: plaintext[:12],
		Hash:   hashToken(plaintext),
	}
	if err := s.db.Create(tok).Error; err != nil {
		return "", nil, err
	}
	return plaintext, tok, nil
}

func (s *Store) List() ([]store.APIToken, error) {
	var tokens []store.APIToken
	err := s.db.Order("created_at desc").Find(&tokens).Error
	return tokens, err
}

func (s *Store) Lookup(plaintext string) (*store.APIToken, error) {
	var tok store.APIToken
	err := s.db.Where("hash = ? AND revoked = ?", hashToken(plaintext), false).First(&tok).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &tok, nil
}

func (s *Store) Token(id string) (*store.APIToken, error) {
	var tok store.APIToken
	err := s.db.Where("id = ?", id).First(&tok).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &tok, nil
}

func (s *Store) Revoke(id string) error {
	return s.db.Model(&store.APIToken{}).Where("id = ?", id).Update("revoked", true).Error
}

func (s *Store) Save(log *store.RequestLog) error {
	if log.ID == "" {
		id, err := newUUID()
		if err != nil {
			return err
		}
		log.ID = id
	}
	return s.db.Create(log).Error
}

func (s *Store) Recent(limit int) ([]store.RequestLog, error) {
	var logs []store.RequestLog
	err := s.db.Order("created_at desc").Limit(limit).Find(&logs).Error
	return logs, err
}

func (s *Store) StatsByToken(tokenID string) (store.TokenStats, error) {
	var st store.TokenStats
	err := s.db.Model(&store.RequestLog{}).
		Where("token_id = ?", tokenID).
		Select("count(*) as requests, " +
			"coalesce(sum(prompt_n),0) as prompt_tokens, " +
			"coalesce(sum(predicted_n),0) as predicted_tokens, " +
			"coalesce(sum(cache_n),0) as cache_tokens").
		Scan(&st).Error
	if err != nil || st.Requests == 0 {
		return st, err
	}
	// Fetch LastUsed via a model query so gorm parses created_at into time.Time
	// (aggregating max(created_at) returns a driver string the scanner rejects).
	var last store.RequestLog
	if err := s.db.Where("token_id = ?", tokenID).Order("created_at desc").First(&last).Error; err == nil {
		st.LastUsed = &last.CreatedAt
	}
	return st, nil
}

func (s *Store) RecentByToken(tokenID string, limit, offset int) ([]store.RequestLog, error) {
	var logs []store.RequestLog
	err := s.db.Where("token_id = ?", tokenID).
		Order("created_at desc").Limit(limit).Offset(offset).Find(&logs).Error
	return logs, err
}

func (s *Store) Get(id string) (*store.RequestLog, error) {
	var log store.RequestLog
	err := s.db.Where("id = ?", id).First(&log).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &log, nil
}
