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
	"strings"
	"sync"
	"time"

	"qoder2api/logs"
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
	Host string `json:"host"`
	Port int    `json:"port"`
	// PAT is the ACTIVE account's token, mirrored from Accounts[ActiveAccountID]
	// so the bridge (which binds to PAT) and every legacy reader keep working
	// unchanged when the active account switches.
	PAT      string      `json:"pat"`
	Password string      `json:"password"`
	APIKeys  []APIKey    `json:"api_keys"`
	Stats    *stats.Data `json:"stats,omitempty"`
	// Logs is the persisted request-log ring (metadata only), flushed out of
	// band by the logs recorder — the JSON equivalent of Buddy-2API's logs
	// table.
	Logs []logs.LogEntry `json:"logs,omitempty"`
	// Request-log limits: RetentionDays bounds how long entries live,
	// MaxEntries the slice length, so data.json (rewritten on every flush)
	// cannot grow without limit.
	LogRetentionDays int `json:"log_retention_days"`
	LogMaxEntries    int `json:"log_max_entries"`
	// ChatTimeoutSeconds bounds how long the upstream chat stream may take
	// to start responding (time to response headers). IdleTimeoutSeconds
	// bounds how long an established SSE stream may stay silent.
	ChatTimeoutSeconds int `json:"chat_timeout_seconds"`
	IdleTimeoutSeconds int `json:"idle_timeout_seconds"`
	// Multi-account pool. A legacy config carrying a bare PAT migrates into a
	// one-entry pool on load (see load).
	Accounts        []Account  `json:"accounts,omitempty"`
	ActiveAccountID string     `json:"active_account_id,omitempty"`
	AutoSwitch      AutoSwitch `json:"auto_switch"`
}

// Account is one Qoder PAT in the multi-account pool. Snapshot is nil until
// the first successful check (admin「检测」or, later, the background loops);
// a stale snapshot is still shown, labelled by CheckedAt.
type Account struct {
	ID   string `json:"id"`
	PAT  string `json:"pat"`
	Note string `json:"note"`
	// Region selects the Qoder deployment for this PAT: ""/"cn" = China
	// (qoder.com.cn), "global" = international (qoder.sh). Empty keeps the
	// historical CN default for accounts created before regions existed.
	Region string `json:"region,omitempty"`
	// Weight is the relative share of requests this account receives when the
	// pool uses weighted rotation (1..100). 0 means the default weight (1);
	// equal weights degrade to plain rotation.
	Weight    int              `json:"weight,omitempty"`
	CreatedAt int64            `json:"created_at"`
	Snapshot  *AccountSnapshot `json:"snapshot,omitempty"`
}

// DefaultAccountWeight is the weight assumed when an account carries none.
const DefaultAccountWeight = 1

// MaxAccountWeight bounds the per-account weight (the smooth weighted
// round-robin arithmetic stays well within int range).
const MaxAccountWeight = 100

// AccountWeight returns the effective weight (>=1) for routing.
func AccountWeight(acc Account) int {
	if acc.Weight < 1 {
		return DefaultAccountWeight
	}
	if acc.Weight > MaxAccountWeight {
		return MaxAccountWeight
	}
	return acc.Weight
}

// AccountSnapshot is the per-account quota snapshot from the authoritative
// OpenAPI data. Remaining/Total sum the spendable pools with the same policy
// as the panel's quota list: pools with a zero total are absent, and the
// organization pool counts only when it is available to this member — so the
// numbers stay comparable across accounts and consistent with the stats page.
type AccountSnapshot struct {
	Remaining float64 `json:"remaining,omitempty"`
	Total     float64 `json:"total,omitempty"`
	// Plan is the human-facing plan label ("Teams", ...) from user/status.
	Plan string `json:"plan,omitempty"`
	// IsExceeded is the OpenAPI out-of-quota verdict.
	IsExceeded bool `json:"is_exceeded,omitempty"`
	// NextResetAtMs is the subscription refresh instant (epoch millis); the
	// hard-exhaustion cooldown runs until it (automatic switching).
	NextResetAtMs int64 `json:"next_reset_at_ms,omitempty"`
	// CheckedAt is when the snapshot was captured (epoch seconds).
	CheckedAt int64 `json:"checked_at,omitempty"`
	// CooldownUntil is the automatic-switching exclusion instant (epoch
	// seconds): hard exhaustion cools down to the next reset, a generic
	// failure (upstream 401/403 on a chat) for a short fixed window.
	CooldownUntil int64 `json:"cooldown_until,omitempty"`
	// LastError is the most recent check failure; empty after a good check.
	LastError string `json:"last_error,omitempty"`
}

// AutoSwitch configures multi-account routing. Enabled/Threshold still exclude
// low-credit accounts from receiving new requests (and drive the legacy
// switch-to-richest behaviour); the remaining fields tune how requests are
// spread across the healthy pool.
type AutoSwitch struct {
	Enabled   bool    `json:"enabled"`
	Threshold float64 `json:"threshold"`
	// Strategy selects the account-picking policy:
	//   "sticky"        — reuse the same account for a conversation until it
	//                     serves StickyLimit requests, then rotate (default);
	//   "round-robin"   — rotate every request;
	//   "least-inflight" — prefer the account with the fewest in-flight requests.
	Strategy string `json:"strategy"`
	// StickyLimit is how many requests one conversation may stay on a single
	// account before it is rotated to the next one. 0 disables rotation (the
	// binding is kept until it expires).
	StickyLimit int `json:"sticky_limit"`
	// MaxRetryAccounts caps how many accounts one chat request may try when it
	// hits an upstream auth failure (1..16).
	MaxRetryAccounts int `json:"max_retry_accounts"`
}

// Routing strategy identifiers.
const (
	StrategySticky        = "sticky"
	StrategyRoundRobin    = "round-robin"
	StrategyLeastInFlight = "least-inflight"
)

// Defaults for the routing knobs.
const (
	DefaultAutoSwitchThreshold = 20
	// DefaultStickyLimit is how many requests one conversation may stay on a
	// single account before rotating. 30 balances context affinity against the
	// per-account rate limiter; the panel lets the user tune it.
	DefaultStickyLimit      = 30
	DefaultMaxRetryAccounts = 3
	MinMaxRetryAccounts     = 1
	MaxMaxRetryAccounts     = 16
)

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

// Request-log limits mirrored from the logs package, so callers that only see
// the store (admin config validation) need no logs import.
const (
	DefaultLogRetentionDays = logs.DefaultRetentionDays
	MaxLogRetentionDays     = logs.MaxRetentionDays
	DefaultLogMaxEntries    = logs.DefaultMaxEntries
	MinLogMaxEntries        = logs.MinMaxEntries
	MaxLogEntriesHardCap    = logs.MaxEntriesHardCap
)

// ValidateLogRetentionDays checks that n is a usable retention in days.
func ValidateLogRetentionDays(n int) error {
	if n < 1 || n > MaxLogRetentionDays {
		return fmt.Errorf("日志保留天数需在 1-%d 之间", MaxLogRetentionDays)
	}
	return nil
}

// ValidateLogMaxEntries checks that n is a usable entry cap.
func ValidateLogMaxEntries(n int) error {
	if n < MinLogMaxEntries || n > MaxLogEntriesHardCap {
		return fmt.Errorf("日志条数上限需在 %d-%d 之间", MinLogMaxEntries, MaxLogEntriesHardCap)
	}
	return nil
}

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
			LogRetentionDays:   DefaultLogRetentionDays,
			LogMaxEntries:      DefaultLogMaxEntries,
			APIKeys:            []APIKey{},
			Accounts:           []Account{},
			AutoSwitch:         AutoSwitch{Threshold: DefaultAutoSwitchThreshold},
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
	if cfg.LogRetentionDays <= 0 {
		cfg.LogRetentionDays = DefaultLogRetentionDays
		needsSave = true
	}
	if cfg.LogMaxEntries <= 0 {
		cfg.LogMaxEntries = DefaultLogMaxEntries
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
	// Multi-account migration: a legacy file carries a bare PAT and no pool.
	// It becomes the single「默认账号」 entry; a hand-edited file whose PAT is
	// not in the pool is imported the same way so the token is never dropped.
	if cfg.Accounts == nil {
		cfg.Accounts = []Account{}
		needsSave = true
	}
	if cfg.PAT != "" && findAccountByPAT(cfg.Accounts, cfg.PAT) < 0 {
		cfg.Accounts = append(cfg.Accounts, Account{
			ID:        GenerateIDMust(),
			PAT:       cfg.PAT,
			Note:      "默认账号",
			CreatedAt: time.Now().Unix(),
		})
		needsSave = true
	}
	if cfg.ActiveAccountID == "" && cfg.PAT != "" {
		if i := findAccountByPAT(cfg.Accounts, cfg.PAT); i >= 0 {
			cfg.ActiveAccountID = cfg.Accounts[i].ID
			needsSave = true
		}
	}
	if cfg.AutoSwitch.Threshold <= 0 {
		cfg.AutoSwitch.Threshold = DefaultAutoSwitchThreshold
		needsSave = true
	}
	if cfg.AutoSwitch.Strategy == "" {
		cfg.AutoSwitch.Strategy = StrategySticky
		needsSave = true
	}
	if cfg.AutoSwitch.MaxRetryAccounts == 0 {
		cfg.AutoSwitch.MaxRetryAccounts = DefaultMaxRetryAccounts
		needsSave = true
	}
	// StickyLimit is a positive count; a missing/zero value (older files) gets
	// the default so existing deployments still rotate. There is no "never
	// rotate" mode: set a large limit instead.
	if cfg.AutoSwitch.StickyLimit <= 0 {
		cfg.AutoSwitch.StickyLimit = DefaultStickyLimit
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

// --- Multi-Account Pool ---

// ErrDuplicateAccount is returned by AddAccount when the same PAT is already
// in the pool.
var ErrDuplicateAccount = errors.New("该令牌已存在")

// ErrActiveAccount is returned when an operation would leave the pool without
// an active account or would delete the one currently in use.
var ErrActiveAccount = errors.New("不能删除当前激活的账号，请先切换到其他账号")

// findAccountByPAT returns the index of the account holding pat, or -1.
// Caller must hold the lock.
func findAccountByPAT(accounts []Account, pat string) int {
	for i := range accounts {
		if accounts[i].PAT == pat {
			return i
		}
	}
	return -1
}

// GenerateIDMust is GenerateID with panic-on-error: ID generation only fails
// when the system CSPRNG is broken, in which case the process is in no state
// to serve anyway.
func GenerateIDMust() string {
	id, err := GenerateID()
	if err != nil {
		panic(fmt.Sprintf("store: generate id: %v", err))
	}
	return id
}

// ListAccounts returns a copy of the account pool.
func (s *Store) ListAccounts() []Account {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Account, len(s.config.Accounts))
	copy(out, s.config.Accounts)
	return out
}

// GetActiveAccountID returns the active account's ID ("" when unset).
func (s *Store) GetActiveAccountID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.config.ActiveAccountID
}

// ClearActiveAccount unsets the active account marker while keeping the pool
// entries. Used when the admin explicitly clears the PAT with an empty pool
// mirror: the bridge is torn down, but the accounts remain for re-activation.
func (s *Store) ClearActiveAccount() {
	s.mu.Lock()
	s.config.ActiveAccountID = ""
	s.mu.Unlock()
	_ = s.save()
}

// AddAccount appends a new account in the default (CN) region. When no account
// is active yet (fresh install) it becomes the active one and its PAT is
// written to the mirrored PAT field; otherwise the caller decides activation
// explicitly via ActivateAccount, so adding never silently moves a running
// bridge.
func (s *Store) AddAccount(pat, note string) (*Account, error) {
	return s.AddAccountWithRegion(pat, note, "")
}

// AddAccountWithRegion is AddAccount with an explicit region ("cn"/"global").
// An already-pooled PAT is a duplicate regardless of region.
func (s *Store) AddAccountWithRegion(pat, note, region string) (*Account, error) {
	if pat == "" {
		return nil, fmt.Errorf("令牌不能为空")
	}
	id := GenerateIDMust()
	s.mu.Lock()
	if findAccountByPAT(s.config.Accounts, pat) >= 0 {
		s.mu.Unlock()
		return nil, ErrDuplicateAccount
	}
	entry := Account{ID: id, PAT: pat, Note: note, Region: normalizeRegion(region), CreatedAt: time.Now().Unix()}
	s.config.Accounts = append(s.config.Accounts, entry)
	if s.config.ActiveAccountID == "" {
		s.config.ActiveAccountID = id
		s.config.PAT = pat
	}
	s.mu.Unlock()
	if err := s.save(); err != nil {
		return nil, err
	}
	return &entry, nil
}

// normalizeRegion canonicalizes a stored region to "" (CN default) or "global".
func normalizeRegion(region string) string {
	switch strings.ToLower(strings.TrimSpace(region)) {
	case "global", "intl", "international":
		return "global"
	default:
		return ""
	}
}

// GetAccount returns the pool entry with the given id (zero value when absent).
func (s *Store) GetAccount(id string) (Account, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, acc := range s.config.Accounts {
		if acc.ID == id {
			return acc, true
		}
	}
	return Account{}, false
}

// AccountRegionByPAT returns the stored region for a PAT ("" when the PAT is
// unknown or has no explicit region, i.e. the CN default).
func (s *Store) AccountRegionByPAT(pat string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if i := findAccountByPAT(s.config.Accounts, pat); i >= 0 {
		return s.config.Accounts[i].Region
	}
	return ""
}

// DeleteAccount removes an account. Deleting the active one is allowed: the
// active marker then moves to the first remaining account (and the PAT mirror
// follows), so the pool always names an active account while it is non-empty.
// This lets the panel drop the manual "switch" action entirely.
func (s *Store) DeleteAccount(id string) error {
	s.mu.Lock()
	wasActive := s.config.ActiveAccountID == id
	found := false
	filtered := s.config.Accounts[:0]
	for _, acc := range s.config.Accounts {
		if acc.ID == id {
			found = true
			continue
		}
		filtered = append(filtered, acc)
	}
	if !found {
		s.mu.Unlock()
		return fmt.Errorf("账号不存在: %s", id)
	}
	s.config.Accounts = filtered
	if wasActive {
		if len(filtered) > 0 {
			s.config.ActiveAccountID = filtered[0].ID
			s.config.PAT = filtered[0].PAT
		} else {
			s.config.ActiveAccountID = ""
			s.config.PAT = ""
		}
	}
	s.mu.Unlock()
	return s.save()
}

// ActivateAccount switches the active account: the PAT mirror is updated in
// the same locked section so no reader can observe a split view. The bridge
// rebuilds itself from the new PAT on its next access (bridgeProvider), and
// the PAT-change hook clears the previous account's stats snapshot.
func (s *Store) ActivateAccount(id string) error {
	s.mu.Lock()
	idx := -1
	for i := range s.config.Accounts {
		if s.config.Accounts[i].ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		s.mu.Unlock()
		return fmt.Errorf("账号不存在: %s", id)
	}
	s.config.ActiveAccountID = id
	s.config.PAT = s.config.Accounts[idx].PAT
	s.mu.Unlock()
	return s.save()
}

// UpdateAccountNote renames an account's note.
func (s *Store) UpdateAccountNote(id, note string) error {
	s.mu.Lock()
	found := false
	for i := range s.config.Accounts {
		if s.config.Accounts[i].ID == id {
			s.config.Accounts[i].Note = note
			found = true
			break
		}
	}
	s.mu.Unlock()
	if !found {
		return fmt.Errorf("账号不存在: %s", id)
	}
	return s.save()
}

// SetAccountWeight updates one account's routing weight (1..MaxAccountWeight).
func (s *Store) SetAccountWeight(id string, weight int) error {
	if weight < 1 || weight > MaxAccountWeight {
		return fmt.Errorf("权重需在 %d..%d 之间", 1, MaxAccountWeight)
	}
	s.mu.Lock()
	found := false
	for i := range s.config.Accounts {
		if s.config.Accounts[i].ID == id {
			s.config.Accounts[i].Weight = weight
			found = true
			break
		}
	}
	s.mu.Unlock()
	if !found {
		return fmt.Errorf("账号不存在: %s", id)
	}
	return s.save()
}

// SetAccountSnapshot replaces one account's quota snapshot. The zero
// LastError clears a previous failure (check succeeded).
func (s *Store) SetAccountSnapshot(id string, snap *AccountSnapshot) error {
	s.mu.Lock()
	found := false
	for i := range s.config.Accounts {
		if s.config.Accounts[i].ID == id {
			s.config.Accounts[i].Snapshot = snap
			found = true
			break
		}
	}
	s.mu.Unlock()
	if !found {
		return fmt.Errorf("账号不存在: %s", id)
	}
	return s.save()
}

// SaveSnapshots merges a batch of snapshots into the pool and persists once.
// Unknown IDs are ignored (the account may have been deleted while the
// snapshot was in flight). Used by the periodic flush of passively deducted
// snapshots, where a save per usage frame would be far too hot.
func (s *Store) SaveSnapshots(snaps map[string]*AccountSnapshot) error {
	if len(snaps) == 0 {
		return nil
	}
	s.mu.Lock()
	applied := 0
	for i := range s.config.Accounts {
		if snap, ok := snaps[s.config.Accounts[i].ID]; ok {
			s.config.Accounts[i].Snapshot = snap
			applied++
		}
	}
	s.mu.Unlock()
	if applied == 0 {
		return nil
	}
	return s.save()
}

// --- AutoSwitch (persisted from day one; consumed by automatic switching) ---

// GetAutoSwitch returns a normalized copy of the auto-switch settings.
func (s *Store) GetAutoSwitch() AutoSwitch {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return normalizeAutoSwitch(s.config.AutoSwitch)
}

// normalizeAutoSwitch fills in defaults for unset routing knobs so callers
// never have to special-case an empty strategy or zero retry budget.
func normalizeAutoSwitch(as AutoSwitch) AutoSwitch {
	if as.Threshold <= 0 {
		as.Threshold = DefaultAutoSwitchThreshold
	}
	switch as.Strategy {
	case StrategySticky, StrategyRoundRobin, StrategyLeastInFlight:
	default:
		as.Strategy = StrategySticky
	}
	if as.StickyLimit <= 0 {
		as.StickyLimit = DefaultStickyLimit
	}
	if as.MaxRetryAccounts <= 0 {
		as.MaxRetryAccounts = DefaultMaxRetryAccounts
	}
	return as
}

// SetAutoSwitch validates and persists the enabled flag plus credit threshold,
// preserving the routing knobs (Strategy/StickyLimit/MaxRetryAccounts).
func (s *Store) SetAutoSwitch(enabled bool, threshold float64) error {
	cfg := s.GetAutoSwitch()
	cfg.Enabled = enabled
	cfg.Threshold = threshold
	return s.SetAutoSwitchConfig(cfg)
}

// SetAutoSwitchConfig validates and persists the full routing configuration.
// Threshold is absolute credits (>= 1) with no upper bound: a pool where every
// balance sits below the threshold simply stops serving new requests.
func (s *Store) SetAutoSwitchConfig(cfg AutoSwitch) error {
	if cfg.Threshold < 1 {
		return fmt.Errorf("阈值需不小于 1 积分")
	}
	switch cfg.Strategy {
	case StrategySticky, StrategyRoundRobin, StrategyLeastInFlight:
	case "":
		cfg.Strategy = StrategySticky
	default:
		return fmt.Errorf("未知的路由策略: %s", cfg.Strategy)
	}
	if cfg.StickyLimit < 1 {
		return fmt.Errorf("粘性次数需不小于 1")
	}
	if cfg.MaxRetryAccounts < MinMaxRetryAccounts || cfg.MaxRetryAccounts > MaxMaxRetryAccounts {
		return fmt.Errorf("最大重试账号数需在 %d..%d 之间", MinMaxRetryAccounts, MaxMaxRetryAccounts)
	}
	s.mu.Lock()
	s.config.AutoSwitch = cfg
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

// --- Request Logs ---

// LoadLogs returns a copy of the persisted request-log entries, or nil if
// none exist — copied so the caller never aliases the marshaled slice.
func (s *Store) LoadLogs() []logs.LogEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.config.Logs) == 0 {
		return nil
	}
	return append([]logs.LogEntry(nil), s.config.Logs...)
}

// SaveLogs persists the request-log entries into the config file.
func (s *Store) SaveLogs(entries []logs.LogEntry) error {
	s.mu.Lock()
	s.config.Logs = entries
	s.mu.Unlock()
	return s.save()
}

// GetLogRetentionDays returns the configured log retention in days
// (0-normalized to the default; clamped to the max).
func (s *Store) GetLogRetentionDays() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if n := s.config.LogRetentionDays; n > 0 {
		if n > MaxLogRetentionDays {
			return MaxLogRetentionDays
		}
		return n
	}
	return DefaultLogRetentionDays
}

// SetLogRetentionDays validates and persists the log retention in days.
func (s *Store) SetLogRetentionDays(n int) error {
	if err := ValidateLogRetentionDays(n); err != nil {
		return err
	}
	s.mu.Lock()
	s.config.LogRetentionDays = n
	s.mu.Unlock()
	return s.save()
}

// GetLogMaxEntries returns the configured log entry cap
// (0-normalized to the default; clamped into its valid range).
func (s *Store) GetLogMaxEntries() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if n := s.config.LogMaxEntries; n > 0 {
		if n < MinLogMaxEntries {
			return MinLogMaxEntries
		}
		if n > MaxLogEntriesHardCap {
			return MaxLogEntriesHardCap
		}
		return n
	}
	return DefaultLogMaxEntries
}

// SetLogMaxEntries validates and persists the log entry cap.
func (s *Store) SetLogMaxEntries(n int) error {
	if err := ValidateLogMaxEntries(n); err != nil {
		return err
	}
	s.mu.Lock()
	s.config.LogMaxEntries = n
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

// LookupKey returns the metadata (ID, note) of the given key and whether it
// exists. Comparison is constant-time, as in ValidateKey: the key material is
// a secret and must not leak through early-exit timing either.
func (s *Store) LookupKey(key string) (APIKey, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	kb := []byte(key)
	for _, k := range s.config.APIKeys {
		if subtle.ConstantTimeCompare([]byte(k.Key), kb) == 1 {
			return k, true
		}
	}
	return APIKey{}, false
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
