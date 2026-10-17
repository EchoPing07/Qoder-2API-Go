package main

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"qoder2api/auth"
	"qoder2api/stats"
	"qoder2api/store"
)

func TestHealthHandler(t *testing.T) {
	s, err := store.New(t.TempDir() + "/data.json")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	h := healthHandler(s)

	w := httptest.NewRecorder()
	h(w, httptest.NewRequest("GET", "/health", nil))
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["status"] != "ok" {
		t.Errorf("expected status ok, got %v", resp["status"])
	}
	if resp["has_pat"] != false {
		t.Errorf("expected has_pat=false without PAT, got %v", resp["has_pat"])
	}
	if int(resp["chat_timeout_seconds"].(float64)) != store.DefaultChatTimeoutSeconds {
		t.Errorf("expected default chat timeout %d, got %v", store.DefaultChatTimeoutSeconds, resp["chat_timeout_seconds"])
	}

	s.SetPAT("pat-x")
	w = httptest.NewRecorder()
	h(w, httptest.NewRequest("GET", "/health", nil))
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["has_pat"] != true {
		t.Errorf("expected has_pat=true with PAT, got %v", resp["has_pat"])
	}
}

func TestHealthHandlerMethodNotAllowed(t *testing.T) {
	s, err := store.New(t.TempDir() + "/data.json")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	w := httptest.NewRecorder()
	healthHandler(s)(w, httptest.NewRequest("POST", "/health", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

// A PAT change must notify the recorder (so the previous account's cached
// snapshot is dropped). Decision: the hook fires only when a previously
// configured PAT is replaced or cleared (p.pat != "" && p.pat != pat); the
// very first PAT has no stale snapshot to drop, and repeated reads of the same
// PAT must stay quiet.
func TestBridgeProviderFiresPatChangeHook(t *testing.T) {
	s, err := store.New(t.TempDir() + "/data.json")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	p := newBridgeProvider(s)
	calls := 0
	p.setOnPatChange(func() { calls++ })

	s.SetPAT("pat-a")
	p.currentBridge()
	if calls != 0 {
		t.Errorf("the first PAT must not fire the change hook, got %d calls", calls)
	}
	p.currentBridge()
	if calls != 0 {
		t.Errorf("an unchanged PAT must not fire the hook, got %d calls", calls)
	}

	s.SetPAT("pat-b")
	p.currentBridge()
	if calls != 1 {
		t.Errorf("switching PAT must fire the hook exactly once, got %d calls", calls)
	}

	s.SetPAT("")
	if p.currentBridge() != nil {
		t.Error("expected no bridge once the PAT is cleared")
	}
	if calls != 2 {
		t.Errorf("clearing the PAT must fire the hook once, got %d calls", calls)
	}
	// A subsequent fetch with no PAT must stay quiet.
	p.currentBridge()
	if calls != 2 {
		t.Errorf("an already-cleared PAT must not fire the hook again, got %d calls", calls)
	}
}

// Shutdown must join the account refresher before triggering the stats loop's
// final flush: the account loop may still be mid-fetch, and a
// SetAccount/SetBillingCycle landing after that flush would be lost on exit.
func TestStopBackgroundLoopsOrdersAccountBeforeFlush(t *testing.T) {
	accountStop := make(chan struct{})
	statsStop := make(chan struct{})
	accountDone := make(chan struct{})
	statsDone := make(chan struct{})

	order := make(chan string, 2)
	// Models an in-flight account fetch: it notices its stop signal only after
	// this delay, and its last write lands just before accountDone closes.
	go func() {
		<-accountStop
		time.Sleep(50 * time.Millisecond)
		order <- "account-stopped"
		close(accountDone)
	}()
	// Models the stats loop, which performs the final flush on stop.
	go func() {
		<-statsStop
		order <- "final-flush"
		close(statsDone)
	}()

	stopBackgroundLoops(&sync.Once{}, accountStop, statsStop, accountDone, statsDone)

	if got := <-order; got != "account-stopped" {
		t.Fatalf("expected the account loop to finish first, got %q", got)
	}
	if got := <-order; got != "final-flush" {
		t.Errorf("expected the final flush after the account join, got %q", got)
	}
}

// A second stop request (a future "second signal forces exit" path, or a
// duplicate call) must block until the first stop finishes and then return
// without panicking on a double close of the stop channels.
func TestStopBackgroundLoopsIsIdempotent(t *testing.T) {
	accountStop := make(chan struct{})
	statsStop := make(chan struct{})
	accountDone := make(chan struct{})
	statsDone := make(chan struct{})

	order := make(chan string, 2)
	go func() {
		<-accountStop
		order <- "account-stopped"
		close(accountDone)
	}()
	go func() {
		<-statsStop
		order <- "final-flush"
		close(statsDone)
	}()

	once := &sync.Once{}
	stopBackgroundLoops(once, accountStop, statsStop, accountDone, statsDone)

	// A second call must neither panic nor run the body again: it returns as
	// soon as the Once has fired.
	done := make(chan struct{})
	go func() {
		defer close(done)
		stopBackgroundLoops(once, accountStop, statsStop, accountDone, statsDone)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a repeated stopBackgroundLoops call did not return")
	}

	if got := <-order; got != "account-stopped" {
		t.Fatalf("expected the account loop to finish first, got %q", got)
	}
	if got := <-order; got != "final-flush" {
		t.Errorf("expected the final flush after the account join, got %q", got)
	}
	select {
	case extra := <-order:
		t.Errorf("the stop body ran more than once: %q", extra)
	default:
	}
}

// shutdown must not return before the HTTP drain completes. Cancelling the drain
// context early (as an earlier version did with a deferred cancel) turns
// graceful shutdown into an immediate abort of every in-flight streaming
// response on SIGTERM.
func TestShutdownWaitsForDrain(t *testing.T) {
	// A real server with a handler that outlives the background-loop join, so
	// the test exercises the actual srv.Shutdown contract rather than a stub.
	handlerEntered := make(chan struct{})
	release := make(chan struct{})
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(handlerEntered)
		<-release
		io.WriteString(w, "complete")
	})}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go srv.Serve(ln)

	type resp struct {
		body string
		err  error
	}
	clientDone := make(chan resp, 1)
	go func() {
		r, err := http.Get("http://" + ln.Addr().String() + "/")
		if err != nil {
			clientDone <- resp{err: err}
			return
		}
		defer r.Body.Close()
		b, err := io.ReadAll(r.Body)
		clientDone <- resp{body: string(b), err: err}
	}()
	<-handlerEntered

	accountStop := make(chan struct{})
	statsStop := make(chan struct{})
	accountDone := make(chan struct{})
	statsDone := make(chan struct{})
	// Both loops stop immediately, so the join adds no delay: any early return
	// would come from the cancelled drain context alone.
	close(accountDone)
	close(statsDone)

	shutdownErr := make(chan error, 1)
	returned := make(chan struct{})
	go func() {
		defer close(returned)
		shutdown(shutdownErr, srv, &sync.Once{}, accountStop, statsStop, accountDone, statsDone)
	}()

	// The in-flight request must still be served: the drain has to wait for it.
	select {
	case <-returned:
		t.Fatal("shutdown returned before the in-flight request completed")
	case <-time.After(150 * time.Millisecond):
	}
	close(release)

	select {
	case got := <-clientDone:
		if got.err != nil {
			t.Fatalf("in-flight request failed: %v", got.err)
		}
		if got.body != "complete" {
			t.Errorf("truncated response body %q; the drain was cancelled early", got.body)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("in-flight request never completed")
	}
	select {
	case err := <-shutdownErr:
		if err != nil {
			t.Errorf("shutdown error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("shutdown never reported")
	}
}

// The PAT-change clear and the account loop's write must be mutually exclusive:
// an in-flight refresh that already passed its PAT check must not resurrect the
// previous account after the hook cleared it.
func TestApplyAccountWriteIsAtomicWithPatClear(t *testing.T) {
	s, err := store.New(t.TempDir() + "/data.json")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	s.SetPAT("pat-a")
	p := newBridgeProvider(s)
	rec := stats.NewRecorder(nil)

	// Simulate: the refresh for pat-a finished fetching and is about to write.
	rec.SetAccount(&stats.Account{Plan: "PLAN_A", Tag: "A"})

	// The admin switches to pat-b, which clears the snapshot.
	s.SetPAT("pat-b")
	clearAccountOnPatChange(rec)

	// The in-flight write for pat-a must now be rejected inside the lock.
	if applyAccountWrite(rec, p, "pat-a", 0, &stats.Account{Plan: "PLAN_A", Tag: "A"}) {
		t.Error("a snapshot for the replaced PAT was written after the clear")
	}
	if got := rec.Report().Account; got != nil {
		t.Errorf("the previous PAT's account was resurrected: %+v", got)
	}

	// The current PAT's snapshot is accepted.
	if !applyAccountWrite(rec, p, "pat-b", 0, &stats.Account{Plan: "PLAN_B", Tag: "B"}) {
		t.Error("the current PAT's snapshot was rejected")
	}
	if got := rec.Report().Account; got == nil || got.Plan != "PLAN_B" {
		t.Errorf("expected the current PAT's account, got %+v", got)
	}
}

// The protection the §22 fix must actually deliver: after the admin switches
// PAT, a refresh that FAILS must not leave the previous account on the panel.
// The hook clears the snapshot at switch time, so the failure branch of
// applyAccount (which has nothing to write) cannot resurrect it.
//
// This exercises the real wiring — provider.setOnPatChange → clearAccountOnPatChange
// — rather than the hook in isolation, because the bug was that the wiring was
// missing entirely.
func TestPatSwitchDoesNotShowPreviousAccountWhenNewRefreshFails(t *testing.T) {
	s, err := store.New(t.TempDir() + "/data.json")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	rec := stats.NewRecorder(nil)
	p := newBridgeProvider(s)
	p.setOnPatChange(func() { clearAccountOnPatChange(rec) })

	// PAT A is configured and its refresh has populated the panel.
	s.SetPAT("pat-a")
	p.currentBridge()
	if !applyAccountWrite(rec, p, "pat-a", 0, &stats.Account{Plan: "PLAN_A", Tag: "A"}) {
		t.Fatal("the initial PAT's snapshot was rejected")
	}
	if got := rec.Report().Account; got == nil || got.Tag != "A" {
		t.Fatalf("expected account A before the switch, got %+v", got)
	}

	// The admin switches to pat-b. B's first refresh then fails: applyAccount
	// returns early on the nil status and writes nothing.
	s.SetPAT("pat-b")
	p.currentBridge() // fires the hook
	// applyAccount's failure branch performs no write, so nothing follows here.

	// A's account must be gone even though B never produced one.
	if got := rec.Report().Account; got != nil {
		t.Errorf("the previous PAT's account survived a failed refresh: %+v", got)
	}
	// And the stale A snapshot must still be rejected if a write were attempted.
	if applyAccountWrite(rec, p, "pat-a", 0, &stats.Account{Plan: "PLAN_A", Tag: "A"}) {
		t.Error("a late write for the replaced PAT was accepted")
	}
	if got := rec.Report().Account; got != nil {
		t.Errorf("a late write resurrected the previous PAT's account: %+v", got)
	}
}

// ═══ Automatic account switching ═══

// newSwitchTestProvider builds a provider + store with a two-account pool:
// acc1 active (remaining 10%), acc2 idle (remaining 80%), auto-switch enabled
// at 20%.
func newSwitchTestPool(t *testing.T) (*bridgeProvider, *store.Store, string, string) {
	t.Helper()
	s, err := store.New(t.TempDir() + "/data.json")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	acc1, _ := s.AddAccount("pt-a", "一号")
	acc2, _ := s.AddAccount("pt-b", "二号")
	s.SetAccountSnapshot(acc1.ID, &store.AccountSnapshot{Remaining: 10, Total: 100, CheckedAt: time.Now().Unix()})
	s.SetAccountSnapshot(acc2.ID, &store.AccountSnapshot{Remaining: 80, Total: 100, CheckedAt: time.Now().Unix()})
	s.SetAutoSwitch(true, 20)
	p := newBridgeProvider(s)
	p.switcher = newAutoSwitcher(s)
	return p, s, acc1.ID, acc2.ID
}

// TestPickCandidate 候选选择：跳过冷却/自身，优先余额充足者；无充足者回退到最高余额。
func TestPickCandidate(t *testing.T) {
	accounts := []store.Account{
		{ID: "self", PAT: "pt-self", Snapshot: &store.AccountSnapshot{Remaining: 10, Total: 100}},
		{ID: "hi", PAT: "pt-hi", Snapshot: &store.AccountSnapshot{Remaining: 80, Total: 100}},
		{ID: "cool", PAT: "pt-cool", Snapshot: &store.AccountSnapshot{Remaining: 95, Total: 100, CooldownUntil: time.Now().Add(time.Hour).Unix()}},
		{ID: "low", PAT: "pt-low", Snapshot: &store.AccountSnapshot{Remaining: 15, Total: 100}},
		{ID: "nosnap", PAT: "pt-nosnap"},
	}
	if got := pickCandidate(accounts, "self", 20); got != "hi" {
		t.Errorf("pick = %q, want hi (highest comfortable)", got)
	}
	// hi 也进冷却 → 回退到未冷却的最高余额（low，虽低于阈值但强于猜测）
	accounts[1].Snapshot.CooldownUntil = time.Now().Add(time.Hour).Unix()
	if got := pickCandidate(accounts, "self", 20); got != "low" {
		t.Errorf("pick = %q, want low (fallback)", got)
	}
	// 全冷却 → 无候选
	accounts[3].Snapshot.CooldownUntil = time.Now().Add(time.Hour).Unix()
	if got := pickCandidate(accounts, "self", 20); got != "" {
		t.Errorf("pick = %q, want empty", got)
	}
}

// TestAutoSwitchOnThreshold 低于阈值的 usage 扣减触发自动切号，并复用 PAT-change
// 链路（hook 清快照）。
func TestAutoSwitchOnThreshold(t *testing.T) {
	p, s, acc1, acc2 := newSwitchTestPool(t)
	clears := 0
	p.switcher.setOnSwitch(func() { clears++ })

	// 扣到阈值下方 → 切到二号（先把驻留期拨过去：新建的 switcher 因 60s 驻留
	// 会挡下首次阈值切换）
	p.switcher.mu.Lock()
	p.switcher.since = time.Now().Add(-2 * minResidence)
	p.switcher.mu.Unlock()
	p.deductCredits(acc1, 5) // 10-5=5% < 20%
	if s.GetActiveAccountID() != acc2 {
		t.Fatalf("active = %q, want %q", s.GetActiveAccountID(), acc2)
	}
	if s.GetPAT() != "pt-b" {
		t.Errorf("PAT mirror = %q, want pt-b (switch is SetPAT)", s.GetPAT())
	}
	if clears != 1 {
		t.Errorf("post-switch hook fired %d times, want 1", clears)
	}
	// 滞回外的正常扣减不切号（现在活跃的是二号，余额充足）
	p.deductCredits(acc2, 5) // 80-5=75% > 20%
	if s.GetActiveAccountID() != acc2 {
		t.Error("healthy deduction must not switch")
	}
}

// TestAutoSwitchDisabled 开关关闭时阈值突破不切号。
func TestAutoSwitchDisabled(t *testing.T) {
	p, s, acc1, _ := newSwitchTestPool(t)
	s.SetAutoSwitch(false, 20)
	p.deductCredits(acc1, 5)
	if s.GetActiveAccountID() != acc1 {
		t.Error("disabled auto-switch must not move the active account")
	}
}

// TestFailoverSwitchesAndCools chat 路径的 AuthError failover：失败号进冷却、
// 切到候选号、返回重建的 bridge；无候选时返回 nil（回退到原始 502）。
func TestFailoverSwitchesAndCools(t *testing.T) {
	p, s, acc1, acc2 := newSwitchTestPool(t)

	next := p.handleChatFailover(acc1)
	if next == nil {
		t.Fatal("failover should return a replacement bridge")
	}
	if s.GetActiveAccountID() != acc2 {
		t.Fatalf("active = %q, want %q", s.GetActiveAccountID(), acc2)
	}
	// 失败号进冷却（5 分钟）
	for _, acc := range s.ListAccounts() {
		if acc.ID == acc1 && (acc.Snapshot == nil || acc.Snapshot.CooldownUntil < time.Now().Unix()) {
			t.Errorf("failed account not cooling down: %+v", acc.Snapshot)
		}
	}
	// 返回的 bridge 指向新账号
	if next.AccountID != acc2 {
		t.Errorf("replacement AccountID = %q, want %q", next.AccountID, acc2)
	}
	// 二号也失败：无候选 → nil，激活不移动
	next = p.handleChatFailover(acc2)
	if next != nil {
		t.Error("no candidate must give up (nil)")
	}
	if s.GetActiveAccountID() != acc2 {
		t.Error("a failed switch attempt must not move the active account")
	}
}

// TestFailoverDedupesConcurrent 并发失败去重：第二个调用者发现激活已切换（failedID
// 不再是激活账号）时不再切号，直接回 nil（该请求在原 bridge 上重试也已被首切覆盖，
// 由 handler 重新 resolve 完成迁移）。
func TestFailoverDedupesConcurrent(t *testing.T) {
	p, s, acc1, _ := newSwitchTestPool(t)
	// 首切成功
	p.handleChatFailover(acc1)
	active := s.GetActiveAccountID()
	// 同一失败号再来：activeID 已变，直接返回（不再切，也不再冷却别人）
	next := p.handleChatFailover(acc1)
	if next != nil {
		t.Error("stale failure report must return nil")
	}
	if s.GetActiveAccountID() != active {
		t.Error("dedupe must keep the current active account")
	}
}

// TestMaybeSwitchRespectsResidence 阈值切换受最小驻留约束（60s），失败切换不受。
func TestMaybeSwitchRespectsResidence(t *testing.T) {
	p, s, acc1, acc2 := newSwitchTestPool(t)
	// 刚切过（since=now），阈值触发被驻留挡下
	p.deductCredits(acc1, 5)
	if s.GetActiveAccountID() != acc1 {
		t.Fatal("precondition: fresh account must resist threshold switch")
	}
	// 失败路径无视驻留
	if next := p.handleChatFailover(acc1); next == nil {
		t.Error("failure switch must ignore min residence")
	}
	if s.GetActiveAccountID() != acc2 {
		t.Errorf("active = %q, want %q", s.GetActiveAccountID(), acc2)
	}
}

// TestApplyQuotaOrgOnlyAccountNotZeroed 纯组织包账号（Teams）的 userQuota
// 上游恒为 0/0：快照必须采用组织包而非被清零（PR #6 修复的回归）。
func TestApplyQuotaOrgOnlyAccountNotZeroed(t *testing.T) {
	p, _, acc1, _ := newSwitchTestPool(t)
	p.applyQuotaToSnapshot(acc1, &auth.AccountStatus{
		UserQuota:           &auth.Quota{Total: 0, Remaining: 0},
		OrgResourcePackage: &auth.OrgResourcePackage{Cap: 1000, Remaining: 800, Available: true},
	})
	snap := p.snapshots[acc1]
	if snap.Total != 1000 || snap.Remaining != 800 {
		t.Errorf("org-only snapshot = total %.0f / remaining %.0f, want 1000 / 800", snap.Total, snap.Remaining)
	}
}

// TestApplyQuotaUnavailableOrgPackageExcluded 组织包对本成员不可用时
// 不计入额度；没有其他可用池则快照为零（被动扣减因此 no-op，不猜测）。
func TestApplyQuotaUnavailableOrgPackageExcluded(t *testing.T) {
	p, _, acc1, _ := newSwitchTestPool(t)
	p.applyQuotaToSnapshot(acc1, &auth.AccountStatus{
		UserQuota:           &auth.Quota{Total: 0, Remaining: 0},
		OrgResourcePackage: &auth.OrgResourcePackage{Cap: 1000, Remaining: 800, Available: false},
	})
	snap := p.snapshots[acc1]
	if snap.Total != 0 || snap.Remaining != 0 {
		t.Errorf("unavailable org package must not count: total %.0f / remaining %.0f", snap.Total, snap.Remaining)
	}
}

// TestSumSpendablePools 多池求和与负余额 clamp 的口径。
func TestSumSpendablePools(t *testing.T) {
	cases := []struct {
		name          string
		st            *auth.AccountStatus
		wantTotal     float64
		wantRemaining float64
	}{
		{
			name: "all pools",
			st: &auth.AccountStatus{
				UserQuota:           &auth.Quota{Total: 100, Remaining: 60},
				AddOnQuota:          &auth.Quota{Total: 50, Remaining: 50},
				OrgResourcePackage: &auth.OrgResourcePackage{Cap: 1000, Remaining: 800, Available: true},
			},
			wantTotal:     1150,
			wantRemaining: 910,
		},
		{
			name: "zero-total pools are absent",
			st: &auth.AccountStatus{
				UserQuota:  &auth.Quota{Total: 0, Remaining: 0},
				AddOnQuota: &auth.Quota{Total: 0, Remaining: 10}, // 零总额池不计入，其余额也不计
			},
			wantTotal:     0,
			wantRemaining: 0,
		},
		{
			name: "over-drawn pool clamped at zero",
			st: &auth.AccountStatus{
				UserQuota: &auth.Quota{Total: 100, Remaining: -20},
			},
			wantTotal:     100,
			wantRemaining: 0,
		},
		{
			name:          "nil status pools",
			st:            &auth.AccountStatus{},
			wantTotal:     0,
			wantRemaining: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			total, remaining := sumSpendablePools(tc.st)
			if total != tc.wantTotal || remaining != tc.wantRemaining {
				t.Errorf("sumSpendablePools = total %.0f / remaining %.0f, want %.0f / %.0f",
					total, remaining, tc.wantTotal, tc.wantRemaining)
			}
		})
	}
}

// TestApplyQuotaHardExhaustedCoolsUntilReset 权威校正把硬耗尽冷却到下次重置点，
// 恢复后清除冷却。
func TestApplyQuotaHardExhaustedCoolsUntilReset(t *testing.T) {
	p, _, acc1, _ := newSwitchTestPool(t)
	resetMs := time.Now().Add(48 * time.Hour).UnixMilli()
	p.applyQuotaToSnapshot(acc1, &auth.AccountStatus{
		UserQuota:      &auth.Quota{Total: 100, Remaining: 0},
		IsQuotaExceeded: true,
		NextResetAtMs:  resetMs,
	})
	snap := p.snapshots[acc1]
	if snap.CooldownUntil != resetMs/1000 {
		t.Errorf("cooldown = %d, want reset %d", snap.CooldownUntil, resetMs/1000)
	}
	// 恢复
	p.applyQuotaToSnapshot(acc1, &auth.AccountStatus{
		UserQuota:      &auth.Quota{Total: 100, Remaining: 50},
		IsQuotaExceeded: false,
	})
	if p.snapshots[acc1].CooldownUntil != 0 {
		t.Error("recovered account must clear the cooldown")
	}
}

// TestFlushAccountSnapshots 被动扣减的内存快照按 30s 循环批量落盘。
func TestFlushAccountSnapshots(t *testing.T) {
	p, s, acc1, _ := newSwitchTestPool(t)
	// 种子内存快照并扣减（不开自动切，免得切走）
	s.SetAutoSwitch(false, 20)
	p.snapMu.Lock()
	p.snapshots[acc1] = &store.AccountSnapshot{Remaining: 50, Total: 100, CheckedAt: 1}
	p.snapMu.Unlock()
	p.deductCredits(acc1, 10)
	p.flushAccountSnapshots()
	var got *store.AccountSnapshot
	for _, acc := range s.ListAccounts() {
		if acc.ID == acc1 {
			got = acc.Snapshot
		}
	}
	if got == nil || got.Remaining != 40 {
		t.Errorf("flushed snapshot = %+v, want remaining 40", got)
	}
	// 无快照时是 no-op（不写盘）
	if err := p.store.SaveSnapshots(nil); err != nil {
		t.Errorf("empty flush = %v", err)
	}
}

// TestDeductCreditsWithoutBaseline 无权威基线（无快照）时被动扣减是 no-op，不猜测。
func TestDeductCreditsWithoutBaseline(t *testing.T) {
	s, err := store.New(t.TempDir() + "/data.json")
	if err != nil {
		t.Fatal(err)
	}
	acc, _ := s.AddAccount("pt-x", "")
	s.SetAutoSwitch(true, 20)
	p := newBridgeProvider(s)
	p.switcher = newAutoSwitcher(s)
	p.deductCredits(acc.ID, 5) // 无快照：不能崩、不能切
	if s.GetActiveAccountID() != acc.ID {
		t.Error("deduction without baseline must not switch")
	}
	if p.snapshots[acc.ID] != nil {
		t.Error("deduction without baseline must not fabricate a snapshot")
	}
}
