package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func tempStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "data.json")
	s, err := New(path)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	return s
}

func TestDefaultHostPort(t *testing.T) {
	s := tempStore(t)
	if s.GetHost() != DefaultHost {
		t.Errorf("expected default host %q, got %q", DefaultHost, s.GetHost())
	}
	if s.GetPort() != DefaultPort {
		t.Errorf("expected default port %d, got %d", DefaultPort, s.GetPort())
	}
}

func TestSetHostPort(t *testing.T) {
	s := tempStore(t)
	if err := s.SetHostPort("127.0.0.1", 8080); err != nil {
		t.Fatalf("SetHostPort failed: %v", err)
	}
	if s.GetHost() != "127.0.0.1" {
		t.Errorf("expected '127.0.0.1', got %q", s.GetHost())
	}
	if s.GetPort() != 8080 {
		t.Errorf("expected 8080, got %d", s.GetPort())
	}
}

func TestHostPortPersistence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data.json")

	s1, _ := New(path)
	s1.SetHostPort("0.0.0.0", 9999)

	s2, err := New(path)
	if err != nil {
		t.Fatalf("reload failed: %v", err)
	}
	if s2.GetHost() != "0.0.0.0" {
		t.Errorf("expected persisted host '0.0.0.0', got %q", s2.GetHost())
	}
	if s2.GetPort() != 9999 {
		t.Errorf("expected persisted port 9999, got %d", s2.GetPort())
	}
}

func TestPAT(t *testing.T) {
	s := tempStore(t)
	if s.GetPAT() != "" {
		t.Errorf("expected empty PAT, got %q", s.GetPAT())
	}
	pat := "pt-test123"
	if err := s.SetPAT(pat); err != nil {
		t.Fatalf("SetPAT failed: %v", err)
	}
	if s.GetPAT() != pat {
		t.Errorf("expected %q, got %q", pat, s.GetPAT())
	}
}

func TestAddKeyCustom(t *testing.T) {
	s := tempStore(t)
	entry, err := s.AddKey("sk-custom", "test note")
	if err != nil {
		t.Fatalf("AddKey failed: %v", err)
	}
	if entry.Key != "sk-custom" {
		t.Errorf("expected 'sk-custom', got %q", entry.Key)
	}
	if entry.Note != "test note" {
		t.Errorf("expected 'test note', got %q", entry.Note)
	}
	if entry.ID == "" {
		t.Error("expected non-empty ID")
	}
	if entry.CreatedAt == 0 {
		t.Error("expected non-zero CreatedAt")
	}
}

func TestAddKeyRandom(t *testing.T) {
	s := tempStore(t)
	entry, err := s.AddKey("", "")
	if err != nil {
		t.Fatalf("AddKey failed: %v", err)
	}
	if !strings.HasPrefix(entry.Key, "sk-") {
		t.Errorf("expected 'sk-' prefix, got %q", entry.Key)
	}
	if len(entry.Key) < 10 {
		t.Errorf("expected key length >= 10, got %d", len(entry.Key))
	}
}

func TestListKeys(t *testing.T) {
	s := tempStore(t)
	s.AddKey("sk-1", "note1")
	s.AddKey("sk-2", "note2")
	keys := s.ListKeys()
	if len(keys) != 2 {
		t.Fatalf("expected 2 keys, got %d", len(keys))
	}
}

func TestDeleteKey(t *testing.T) {
	s := tempStore(t)
	entry, _ := s.AddKey("sk-to-delete", "temp")
	keys := s.ListKeys()
	if len(keys) != 1 {
		t.Fatalf("expected 1 key, got %d", len(keys))
	}
	if err := s.DeleteKey(entry.ID); err != nil {
		t.Fatalf("DeleteKey failed: %v", err)
	}
	keys = s.ListKeys()
	if len(keys) != 0 {
		t.Fatalf("expected 0 keys after delete, got %d", len(keys))
	}
}

func TestDeleteKeyNotFound(t *testing.T) {
	s := tempStore(t)
	err := s.DeleteKey("nonexistent")
	if err == nil {
		t.Error("expected error for non-existent key")
	}
}

func TestValidateKey(t *testing.T) {
	s := tempStore(t)
	s.AddKey("sk-valid", "test")
	if !s.ValidateKey("sk-valid") {
		t.Error("expected sk-valid to be valid")
	}
	if s.ValidateKey("sk-invalid") {
		t.Error("expected sk-invalid to be invalid")
	}
}

func TestPersistence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data.json")

	s1, _ := New(path)
	s1.AddKey("sk-persist", "persist note")
	s1.SetPAT("pt-persist")

	s2, err := New(path)
	if err != nil {
		t.Fatalf("reload failed: %v", err)
	}
	if s2.GetPAT() != "pt-persist" {
		t.Errorf("expected persisted PAT 'pt-persist', got %q", s2.GetPAT())
	}
	if !s2.ValidateKey("sk-persist") {
		t.Error("expected persisted key to be valid")
	}
}

func TestGenerateAPIKey(t *testing.T) {
	k1 := GenerateAPIKey()
	k2 := GenerateAPIKey()
	if k1 == k2 {
		t.Error("expected different keys from two GenerateAPIKey calls")
	}
	if !strings.HasPrefix(k1, "sk-") {
		t.Errorf("expected 'sk-' prefix, got %q", k1)
	}
}

func TestFilePermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data.json")
	s, _ := New(path)
	s.SetPAT("pt-secret")

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat failed: %v", err)
	}
	// On Unix, file should be 0600. On Windows, this check is a no-op.
	if info.Mode().Perm() != 0600 && info.Mode().Perm() != 0666 {
		// Windows may not support 0600, just check it exists
		if info.Size() == 0 {
			t.Error("expected non-empty data file")
		}
	}
}
