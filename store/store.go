// Package store provides JSON file-based persistence for API keys
// and PAT token.
package store

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

// APIKey represents a single API key entry.
type APIKey struct {
	ID        string `json:"id"`
	Key       string `json:"key"`
	Note      string `json:"note"`
	CreatedAt int64  `json:"created_at"`
}

// Config is the on-disk JSON structure.
type Config struct {
	Host     string   `json:"host"`
	Port     int      `json:"port"`
	PAT      string   `json:"pat"`
	Password string   `json:"password"`
	APIKeys  []APIKey `json:"api_keys"`
}

// DefaultHost is the default listen host.
const DefaultHost = "0.0.0.0"

// DefaultPort is the default listen port.
const DefaultPort = 18080

// DefaultPassword is the default admin password.
const DefaultPassword = "password"

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
			Host:    DefaultHost,
			Port:    DefaultPort,
			APIKeys: []APIKey{},
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
	if err := os.WriteFile(tmp, data, 0600); err != nil {
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
func (s *Store) AddKey(key, note string) (*APIKey, error) {
	if key == "" {
		key = GenerateAPIKey()
	}
	entry := &APIKey{
		ID:        GenerateID(),
		Key:       key,
		Note:      note,
		CreatedAt: time.Now().Unix(),
	}
	s.mu.Lock()
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
func (s *Store) ValidateKey(key string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, k := range s.config.APIKeys {
		if k.Key == key {
			return true
		}
	}
	return false
}

// --- Generators ---

// GenerateAPIKey returns a random "sk-" prefixed key (32 hex chars).
func GenerateAPIKey() string {
	b := make([]byte, 16)
	rand.Read(b)
	return "sk-" + hex.EncodeToString(b)
}

// GenerateID returns a short random hex ID.
func GenerateID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}
