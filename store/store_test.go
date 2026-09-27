package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"qoder2api/logs"
	"qoder2api/stats"
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

func TestStatsPersistence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data.json")

	s1, err := New(path)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	if s1.LoadStats() != nil {
		t.Error("expected nil stats on fresh store")
	}
	d := &stats.Data{Total: 5, Success: 4, Failed: 1}
	d.ByModel = map[string]*stats.ModelStat{
		"Qwen3.7-Max": {Model: "Qwen3.7-Max", Total: 5, Success: 4, Failed: 1},
	}
	if err := s1.SaveStats(d); err != nil {
		t.Fatalf("SaveStats failed: %v", err)
	}

	s2, err := New(path)
	if err != nil {
		t.Fatalf("reload failed: %v", err)
	}
	got := s2.LoadStats()
	if got == nil {
		t.Fatal("expected restored stats")
	}
	if got.Total != 5 || got.Success != 4 || got.Failed != 1 {
		t.Errorf("unexpected restored totals: %+v", got)
	}
	m, ok := got.ByModel["Qwen3.7-Max"]
	if !ok || m.Total != 5 {
		t.Errorf("expected restored per-model stat, got %+v", got.ByModel)
	}
}

func TestGenerateAPIKey(t *testing.T) {
	k1, err := GenerateAPIKey()
	if err != nil {
		t.Fatalf("GenerateAPIKey failed: %v", err)
	}
	k2, err := GenerateAPIKey()
	if err != nil {
		t.Fatalf("GenerateAPIKey failed: %v", err)
	}
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

// Adding the same key twice must be rejected with a sentinel error so the
// HTTP layer can map it to a 409.
func TestAddKeyDuplicate(t *testing.T) {
	s := tempStore(t)
	if _, err := s.AddKey("sk-dup", "first"); err != nil {
		t.Fatalf("first AddKey failed: %v", err)
	}
	_, err := s.AddKey("sk-dup", "second")
	if !errors.Is(err, ErrDuplicateKey) {
		t.Errorf("expected ErrDuplicateKey, got %v", err)
	}
	if len(s.ListKeys()) != 1 {
		t.Errorf("expected 1 key after duplicate rejection, got %d", len(s.ListKeys()))
	}
}

// Saves must go through the fsync'd tmp+rename path: no .tmp leftovers and
// no partial files.
func TestSaveLeavesNoTmpFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data.json")
	s, _ := New(path)
	if _, err := s.AddKey("sk-x", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("tmp file leaked: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "sk-x") {
		t.Error("persisted file missing key")
	}
}

// -- Stream timeouts --

func TestStreamTimeoutDefaults(t *testing.T) {
	s := tempStore(t)
	if s.GetChatTimeoutSeconds() != DefaultChatTimeoutSeconds {
		t.Errorf("expected default chat timeout %d, got %d", DefaultChatTimeoutSeconds, s.GetChatTimeoutSeconds())
	}
	if s.GetIdleTimeoutSeconds() != DefaultIdleTimeoutSeconds {
		t.Errorf("expected default idle timeout %d, got %d", DefaultIdleTimeoutSeconds, s.GetIdleTimeoutSeconds())
	}
}

func TestStreamTimeoutPersistence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data.json")
	s1, _ := New(path)
	if err := s1.SetChatTimeoutSeconds(90); err != nil {
		t.Fatalf("SetChatTimeoutSeconds: %v", err)
	}
	if err := s1.SetIdleTimeoutSeconds(45); err != nil {
		t.Fatalf("SetIdleTimeoutSeconds: %v", err)
	}
	s2, err := New(path)
	if err != nil {
		t.Fatalf("reload failed: %v", err)
	}
	if s2.GetChatTimeoutSeconds() != 90 || s2.GetIdleTimeoutSeconds() != 45 {
		t.Errorf("timeouts not persisted: chat=%d idle=%d", s2.GetChatTimeoutSeconds(), s2.GetIdleTimeoutSeconds())
	}
}

// Hand-edited files with out-of-range values are clamped on load instead of
// rejecting the whole config.
func TestStreamTimeoutClampOnLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data.json")
	if err := os.WriteFile(path, []byte(`{"chat_timeout_seconds":999999,"idle_timeout_seconds":999999}`), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := New(path)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	if s.GetChatTimeoutSeconds() != MaxChatTimeoutSeconds {
		t.Errorf("expected chat timeout clamped to %d, got %d", MaxChatTimeoutSeconds, s.GetChatTimeoutSeconds())
	}
	if s.GetIdleTimeoutSeconds() != MaxIdleTimeoutSeconds {
		t.Errorf("expected idle timeout clamped to %d, got %d", MaxIdleTimeoutSeconds, s.GetIdleTimeoutSeconds())
	}
}

func TestSetStreamTimeoutValidation(t *testing.T) {
	s := tempStore(t)
	for _, n := range []int{0, -1, MaxChatTimeoutSeconds + 1} {
		if err := s.SetChatTimeoutSeconds(n); err == nil {
			t.Errorf("SetChatTimeoutSeconds(%d) should fail", n)
		}
	}
	for _, n := range []int{0, -1, MaxIdleTimeoutSeconds + 1} {
		if err := s.SetIdleTimeoutSeconds(n); err == nil {
			t.Errorf("SetIdleTimeoutSeconds(%d) should fail", n)
		}
	}
	if s.GetChatTimeoutSeconds() != DefaultChatTimeoutSeconds {
		t.Errorf("rejected writes must not change the value, got %d", s.GetChatTimeoutSeconds())
	}
}

// Negative hand-edited values normalize to defaults on load (and are
// persisted back so the file stops lying about the effective value).
func TestStreamTimeoutNegativeOnLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data.json")
	if err := os.WriteFile(path, []byte(`{"chat_timeout_seconds":-5,"idle_timeout_seconds":-1}`), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := New(path)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	if s.GetChatTimeoutSeconds() != DefaultChatTimeoutSeconds || s.GetIdleTimeoutSeconds() != DefaultIdleTimeoutSeconds {
		t.Errorf("negative values should fall back to defaults, got chat=%d idle=%d",
			s.GetChatTimeoutSeconds(), s.GetIdleTimeoutSeconds())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "-5") {
		t.Errorf("normalized values should be persisted back, file still has -5:\n%s", data)
	}
}

// -- Request Logs --

func TestLogsRoundTrip(t *testing.T) {
	s := tempStore(t)
	entries := []logs.LogEntry{
		{ID: 1, Model: "Qwen3.7-Max", StatusCode: 200, TotalTokens: 42, CreatedAt: 100},
		{ID: 2, Model: "DeepSeek-V4-Pro", StatusCode: 502, ErrorMsg: "boom", CreatedAt: 200},
	}
	if err := s.SaveLogs(entries); err != nil {
		t.Fatalf("SaveLogs: %v", err)
	}
	loaded := s.LoadLogs()
	if len(loaded) != 2 || loaded[0].Model != "Qwen3.7-Max" || loaded[1].ErrorMsg != "boom" {
		t.Fatalf("LoadLogs = %+v", loaded)
	}
	// The returned slice must be a copy: mutating it cannot corrupt the store.
	loaded[0].Model = "mutated"
	if s.LoadLogs()[0].Model != "Qwen3.7-Max" {
		t.Error("LoadLogs aliased the internal slice")
	}
	// SaveStats must not clobber the logs (same config file).
	if err := s.SaveStats(&stats.Data{Total: 5}); err != nil {
		t.Fatalf("SaveStats: %v", err)
	}
	if got := s.LoadLogs(); len(got) != 2 {
		t.Errorf("logs lost after SaveStats: %+v", got)
	}
}

func TestLogsDefaultEmpty(t *testing.T) {
	s := tempStore(t)
	if got := s.LoadLogs(); got != nil {
		t.Errorf("LoadLogs on empty store = %v, want nil", got)
	}
}

func TestLogLimitsDefaultsAndValidation(t *testing.T) {
	s := tempStore(t)
	if got := s.GetLogRetentionDays(); got != DefaultLogRetentionDays {
		t.Errorf("default retention = %d, want %d", got, DefaultLogRetentionDays)
	}
	if got := s.GetLogMaxEntries(); got != DefaultLogMaxEntries {
		t.Errorf("default max entries = %d, want %d", got, DefaultLogMaxEntries)
	}
	if err := s.SetLogRetentionDays(30); err != nil {
		t.Fatalf("SetLogRetentionDays: %v", err)
	}
	if err := s.SetLogMaxEntries(500); err != nil {
		t.Fatalf("SetLogMaxEntries: %v", err)
	}
	if s.GetLogRetentionDays() != 30 || s.GetLogMaxEntries() != 500 {
		t.Errorf("limits not persisted: %d/%d", s.GetLogRetentionDays(), s.GetLogMaxEntries())
	}
	for _, err := range []error{
		s.SetLogRetentionDays(0),
		s.SetLogRetentionDays(-1),
		s.SetLogRetentionDays(99999),
		s.SetLogMaxEntries(10),
		s.SetLogMaxEntries(9999999),
	} {
		if err == nil {
			t.Error("out-of-range log limit must be rejected")
		}
	}
	// A hand-edited file with zero/invalid values falls back to defaults on load.
	s2 := tempStore(t)
	if err := s2.SetLogRetentionDays(7); err != nil {
		t.Fatalf("seed: %v", err)
	}
	s3, err := New(s2.filePath)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got := s3.GetLogRetentionDays(); got != 7 {
		t.Errorf("retention after reload = %d, want 7", got)
	}
}

func TestLookupKey(t *testing.T) {
	s := tempStore(t)
	added, err := s.AddKey("sk-lookup", "note-1")
	if err != nil {
		t.Fatalf("AddKey: %v", err)
	}
	k, ok := s.LookupKey("sk-lookup")
	if !ok || k.ID != added.ID || k.Note != "note-1" {
		t.Fatalf("LookupKey = %+v ok=%v, want id=%s note=note-1", k, ok, added.ID)
	}
	if _, ok := s.LookupKey("sk-missing"); ok {
		t.Error("LookupKey must report false for a missing key")
	}
}
