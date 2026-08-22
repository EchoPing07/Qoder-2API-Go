// Package store provides JSON file-based persistence for API keys
// and PAT token.
package store

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"qoder2api/stats"
)

// ErrDuplicateKey is returned by AddKey when an identical key already exists.
var ErrDuplicateKey = errors.New("api key already exists")

// APIKey represents a single API key entry.
type APIKey struct {
	ID        string `json:"id"`
	Key       string `json:"key"`
	Note      string `json:"note"`
	CreatedAt int64  `json:"created_at"`
}

// Config is the on-disk JSON structure.
type Config struct {
	Host     string      `json:"host"`
	Port     int         `json:"port"`
	PAT      string      `json:"pat"`
	Password string      `json:"password"`
	APIKeys  []APIKey    `json:"api_keys"`
	Stats    *stats.Data `json:"stats,omitempty"`
	// ChatTimeoutSeconds bounds how long the upstream chat stream may take
	// to start responding (time to response headers). IdleTimeoutSeconds
	// bounds how long an established SSE stream may stay silent.
	ChatTimeoutSeconds int `json:"chat_timeout_seconds"`
	IdleTimeoutSeconds int `json:"idle_timeout_seconds"`
}

// DefaultHost is the default listen host.
const DefaultHost = "0.0.0.0"

// DefaultPort is the default listen port.
const DefaultPort = 10081

// DefaultPassword is the default admin password.
const DefaultPassword = "password"

// Chat stream timeout bounds (seconds). The max cap also guards against
// overflow when callers convert seconds to time.Duration nanoseconds — a
// value beyond ~292 years wraps negative and silently disables the timeout.
const (
	DefaultChatTimeoutSeconds = 120
	MaxChatTimeoutSeconds     = 3600
	DefaultIdleTimeoutSeconds = 300
	MaxIdleTimeoutSeconds     = 3600
)

// ValidateTimeoutSeconds checks that n is a usable timeout in seconds.
func ValidateTimeoutSeconds(n, max int) error {
	if n < 1 || n > max {
		return fmt.Errorf("超时需在 1-%d 秒之间", max)
	}
	return nil
}

// Store manages persistent configuration with thread-safe access.
type Store struct {
	mu       sync.RWMutex
	filePath string
	config   *Config
}

// New creates a Store backed by the given file path.
// If the file does not exist, a default config is created.
func New(filePath string) (*Store, error) {
	s := &Store{
		filePath: filePath,
		config: &Config{
			Host:               DefaultHost,
			Port:               DefaultPort,
			ChatTimeoutSeconds: DefaultChatTimeoutSeconds,
			IdleTimeoutSeconds: DefaultIdleTimeoutSeconds,
			APIKeys:            []APIKey{},
		},
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) load() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(s.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return s.saveLocked()
		}
		return err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("store: parse %s: %w", s.filePath, err)
	}
	needsSave := false
	if cfg.APIKeys == nil {
		cfg.APIKeys = []APIKey{}
		needsSave = true
	}
	if cfg.Host == "" {
		cfg.Host = DefaultHost
		needsSave = true
	}
	if cfg.Port == 0 {
		cfg.Port = DefaultPort
		needsSave = true
	}
	if cfg.Password == "" {
		cfg.Password = DefaultPassword
		needsSave = true
	}
	if cfg.ChatTimeoutSeconds <= 0 {
		cfg.ChatTimeoutSeconds = DefaultChatTimeoutSeconds
		needsSave = true
	}
	if cfg.IdleTimeoutSeconds <= 0 {
		cfg.IdleTimeoutSeconds = DefaultIdleTimeoutSeconds
		needsSave = true
	}
	// Clamp out-of-range values (hand-edited files, older versions) instead
	// of rejecting the whole file.
	if cfg.ChatTimeoutSeconds > MaxChatTimeoutSeconds {
		cfg.ChatTimeoutSeconds = MaxChatTimeoutSeconds
		needsSave = true
	}
	if cfg.IdleTimeoutSeconds > MaxIdleTimeoutSeconds {
		cfg.IdleTimeoutSeconds = MaxIdleTimeoutSeconds
		needsSave = true
	}
	s.config = &cfg
	// Persist migrated defaults so the file stays in sync
	if needsSave {
		return s.saveLocked()
	}
	return nil
}

func (s *Store) saveLocked() error {
	data, err := json.MarshalIndent(s.config, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.filePath + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	// fsync before rename: without it a crash can leave a truncated or empty
	// data.json on some filesystems (rename is not a barrier for unflushed
	// file contents).
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, s.filePath)
}

func (s *Store) save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked()
}

// --- Server Config ---

func (s *Store) GetHost() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.config.Host
}

func (s *Store) GetPort() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.config.Port
}

func (s *Store) SetHostPort(host string, port int) error {
	s.mu.Lock()
	s.config.Host = host
	s.config.Port = port
	s.mu.Unlock()
	return s.save()
}

// --- Stream Timeouts ---

// GetChatTimeoutSeconds returns the chat response-header timeout in seconds
// (0-normalized to the default; clamped to the max).
func (s *Store) GetChatTimeoutSeconds() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return normalizeTimeout(s.config.ChatTimeoutSeconds, DefaultChatTimeoutSeconds, MaxChatTimeoutSeconds)
}

// GetIdleTimeoutSeconds returns the SSE idle timeout in seconds.
func (s *Store) GetIdleTimeoutSeconds() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return normalizeTimeout(s.config.IdleTimeoutSeconds, DefaultIdleTimeoutSeconds, MaxIdleTimeoutSeconds)
}

// SetChatTimeoutSeconds validates and persists the chat response-header
// timeout in seconds.
func (s *Store) SetChatTimeoutSeconds(n int) error {
	if err := ValidateTimeoutSeconds(n, MaxChatTimeoutSeconds); err != nil {
		return err
	}
	s.mu.Lock()
	s.config.ChatTimeoutSeconds = n
	s.mu.Unlock()
	return s.save()
}

// SetIdleTimeoutSeconds validates and persists the SSE idle timeout in
// seconds.
func (s *Store) SetIdleTimeoutSeconds(n int) error {
	if err := ValidateTimeoutSeconds(n, MaxIdleTimeoutSeconds); err != nil {
		return err
	}
	s.mu.Lock()
	s.config.IdleTimeoutSeconds = n
	s.mu.Unlock()
	return s.save()
}

// normalizeTimeout maps 0 to def and clamps above max.
func normalizeTimeout(n, def, max int) int {
	if n <= 0 {
		return def
	}
	if n > max {
		return max
	}
	return n
}

// --- Password ---

func (s *Store) GetPassword() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.config.Password == "" {
		return DefaultPassword
	}
	return s.config.Password
}

func (s *Store) SetPassword(password string) error {
	s.mu.Lock()
	s.config.Password = password
	s.mu.Unlock()
	return s.save()
}

// --- PAT ---

func (s *Store) GetPAT() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.config.PAT
}

func (s *Store) SetPAT(pat string) error {
	s.mu.Lock()
	s.config.PAT = pat
	s.mu.Unlock()
	return s.save()
}

// --- Stats ---

// LoadStats returns a deep copy of the persisted stats snapshot, or nil if
// none exists. A copy is returned so callers can never race with concurrent
// save() calls that marshal the live snapshot.
func (s *Store) LoadStats() *stats.Data {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.config.Stats == nil {
		return nil
	}
	return s.config.Stats.Clone()
}

// SaveStats persists the stats snapshot into the config file.
func (s *Store) SaveStats(d *stats.Data) error {
	s.mu.Lock()
	s.config.Stats = d
	s.mu.Unlock()
	return s.save()
}

// --- API Keys ---

// ListKeys returns a copy of all API keys.
func (s *Store) ListKeys() []APIKey {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]APIKey, len(s.config.APIKeys))
	copy(out, s.config.APIKeys)
	return out
}

// AddKey creates a new API key. If key is empty, a random one is generated.
// Returns an error if the key already exists (duplicate) or generation fails.
func (s *Store) AddKey(key, note string) (*APIKey, error) {
	if key == "" {
		var err error
		key, err = GenerateAPIKey()
		if err != nil {
			return nil, fmt.Errorf("generate api key: %w", err)
		}
	}
	id, err := GenerateID()
	if err != nil {
		return nil, fmt.Errorf("generate id: %w", err)
	}
	entry := &APIKey{
		ID:        id,
		Key:       key,
		Note:      note,
		CreatedAt: time.Now().Unix(),
	}
	s.mu.Lock()
	for _, k := range s.config.APIKeys {
		if k.Key == key {
			s.mu.Unlock()
			return nil, ErrDuplicateKey
		}
	}
	s.config.APIKeys = append(s.config.APIKeys, *entry)
	s.mu.Unlock()
	if err := s.save(); err != nil {
		return nil, err
	}
	return entry, nil
}

// DeleteKey removes the API key with the given ID.
func (s *Store) DeleteKey(id string) error {
	s.mu.Lock()
	found := false
	filtered := s.config.APIKeys[:0]
	for _, k := range s.config.APIKeys {
		if k.ID == id {
			found = true
			continue
		}
		filtered = append(filtered, k)
	}
	s.config.APIKeys = filtered
	s.mu.Unlock()
	if !found {
		return fmt.Errorf("key not found: %s", id)
	}
	return s.save()
}

// ValidateKey returns true if the given key exists in the store.
// Comparison is constant-time to avoid leaking key material via timing.
func (s *Store) ValidateKey(key string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	kb := []byte(key)
	for _, k := range s.config.APIKeys {
		// ConstantTimeCompare returns 1 only when lengths match and bytes equal.
		if subtle.ConstantTimeCompare([]byte(k.Key), kb) == 1 {
			return true
		}
	}
	return false
}

// --- Generators ---

// GenerateAPIKey returns a random "sk-" prefixed key (32 hex chars).
func GenerateAPIKey() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "sk-" + hex.EncodeToString(b), nil
}

// GenerateID returns a short random hex ID.
func GenerateID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
