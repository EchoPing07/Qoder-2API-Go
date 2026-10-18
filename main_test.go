package main

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"qoder2api/auth"
	"qoder2api/bridge"
	"qoder2api/models"
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

// ═══ Multi-account routing ═══

// newRouteTestPool builds a provider + store with a two-account pool:
// acc1 (remaining 10, active), acc2 (remaining 80), auto-switch enabled at 20.
func newRouteTestPool(t *testing.T) (*bridgeProvider, *store.Store, string, string) {
	t.Helper()
	s, err := store.New(t.TempDir() + "/data.json")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	acc1, _ := s.AddAccount("pt-a", "一号")
	acc2, _ := s.AddAccount("pt-b", "二号")
	s.SetAccountSnapshot(acc1.ID, &store.AccountSnapshot{Remaining: 50, Total: 100, CheckedAt: time.Now().Unix()})
	s.SetAccountSnapshot(acc2.ID, &store.AccountSnapshot{Remaining: 80, Total: 100, CheckedAt: time.Now().Unix()})
	s.SetAutoSwitch(true, 20)
	p := newBridgeProvider(s)
	return p, s, acc1.ID, acc2.ID
}

// pickWith is a test helper: route once and return the chosen account ID.
func pickWith(p *bridgeProvider, model string, filter models.RegionFilter, exclude map[string]bool) string {
	acc, _, ok := p.router.pick("key-1", model, "seed", filter, exclude)
	if !ok {
		return ""
	}
	return acc.ID
}

// TestRouterRoundRobinSpreads 轮询：连续请求在不同账号间交替，不集中在同一账号。
func TestRouterRoundRobinSpreads(t *testing.T) {
	p, s, acc1, acc2 := newRouteTestPool(t)
	cfg := s.GetAutoSwitch()
	cfg.Strategy = store.StrategyRoundRobin
	if err := s.SetAutoSwitchConfig(cfg); err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for i := 0; i < 10; i++ {
		id := pickWith(p, "Qwen3.8-Flash", models.RegionDefault, nil)
		if id == "" {
			t.Fatal("no route")
		}
		seen[id]++
	}
	if seen[acc1] == 0 || seen[acc2] == 0 {
		t.Errorf("round-robin must use both accounts, got %+v", seen)
	}
	if seen[acc1] < 4 || seen[acc2] < 4 {
		t.Errorf("round-robin distribution too skewed: %+v", seen)
	}
}

// TestRouterStickyReusesThenRotates 粘性：同一会话连续命中同一账号，达到上限后轮换。
func TestRouterStickyReusesThenRotates(t *testing.T) {
	p, s, _, _ := newRouteTestPool(t)
	cfg := s.GetAutoSwitch()
	cfg.Strategy = store.StrategySticky
	cfg.StickyLimit = 3
	if err := s.SetAutoSwitchConfig(cfg); err != nil {
		t.Fatal(err)
	}
	// Same conversation seed for every pick (sticky keys on it).
	pickSame := func() string {
		acc, _, ok := p.router.pick("key-1", "m", "conv-1", models.RegionDefault, nil)
		if !ok {
			t.Fatal("no route")
		}
		return acc.ID
	}
	// First pick + successful bind pins the conversation to account A.
	first := pickSame()
	_, stickyKey, _ := p.router.pick("key-1", "m", "conv-1", models.RegionDefault, nil)
	// The pick above already bound? No: bind only happens on success. Bind
	// explicitly to simulate the first success.
	stickyKey = stickyKeyFor("key-1", "conv-1")
	p.router.bind(stickyKey, first)

	// Reuses stay on A until count exceeds StickyLimit.
	for i := 0; i < cfg.StickyLimit; i++ {
		if got := pickSame(); got != first {
			t.Fatalf("sticky reuse %d = %q, want %q", i, got, first)
		}
	}
	// The next pick rotates to the other account.
	if got := pickSame(); got == first {
		t.Errorf("after StickyLimit the router must rotate, still on %q", got)
	}
}

// TestRouterDefaultRegionPrefersCN 无前缀默认国内；池里无国内账号时跨区。
func TestRouterDefaultRegionPrefersCN(t *testing.T) {
	s, err := store.New(t.TempDir() + "/data.json")
	if err != nil {
		t.Fatal(err)
	}
	cn, _ := s.AddAccountWithRegion("pt-cn", "", "cn")
	gl, _ := s.AddAccountWithRegion("pt-gl", "", "global")
	s.SetAutoSwitch(true, 20)
	p := newBridgeProvider(s)

	for i := 0; i < 6; i++ {
		if id := pickWith(p, "m", models.RegionDefault, nil); id != cn.ID {
			t.Fatalf("default filter must prefer CN, got %q", id)
		}
	}
	// cn/ pins CN; gl/ pins global.
	if id := pickWith(p, "m", models.RegionGlobal, nil); id != gl.ID {
		t.Errorf("global filter = %q, want %q", id, gl.ID)
	}
	if id := pickWith(p, "m", models.RegionCN, nil); id != cn.ID {
		t.Errorf("cn filter = %q, want %q", id, cn.ID)
	}
	// auto balances across both.
	seen := map[string]bool{}
	cfg := s.GetAutoSwitch()
	cfg.Strategy = store.StrategyRoundRobin
	_ = s.SetAutoSwitchConfig(cfg)
	for i := 0; i < 6; i++ {
		seen[pickWith(p, "m", models.RegionAuto, nil)] = true
	}
	if !seen[cn.ID] || !seen[gl.ID] {
		t.Errorf("auto must span both regions, got %+v", seen)
	}
}

// TestRouterDefaultFallsBackWhenNoCN 只有国际账号时，无前缀请求跨区落到国际账号。
func TestRouterDefaultFallsBackWhenNoCN(t *testing.T) {
	s, err := store.New(t.TempDir() + "/data.json")
	if err != nil {
		t.Fatal(err)
	}
	gl, _ := s.AddAccountWithRegion("pt-gl", "", "global")
	s.SetAutoSwitch(true, 20)
	p := newBridgeProvider(s)
	if id := pickWith(p, "m", models.RegionDefault, nil); id != gl.ID {
		t.Errorf("default with no CN account must fall back to global, got %q", id)
	}
}

// TestRouterExcludesLowCreditsAndCooldown 低于阈值或冷却中的账号不接新请求。
func TestRouterExcludesLowCreditsAndCooldown(t *testing.T) {
	p, s, acc1, acc2 := newRouteTestPool(t)
	// acc1 剩余 10 <= 阈值 20 -> 排除；只剩 acc2。
	s.SetAccountSnapshot(acc1, &store.AccountSnapshot{Remaining: 10, Total: 100, CheckedAt: time.Now().Unix()})
	cfg := s.GetAutoSwitch()
	cfg.Strategy = store.StrategyRoundRobin
	if err := s.SetAutoSwitchConfig(cfg); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		if id := pickWith(p, "m", models.RegionDefault, nil); id != acc2 {
			t.Fatalf("low-credit account must be excluded, got %q", id)
		}
	}
	// acc2 也进冷却 -> 无可用路由。
	p.router.markFailure(acc2)
	if id := pickWith(p, "m", models.RegionDefault, nil); id != "" {
		t.Errorf("all accounts cooling must yield no route, got %q", id)
	}
	_ = acc1
}

// TestRouterExcludeSetSkipsFailedAccount 失败重试时被排除的账号不再选中。
func TestRouterExcludeSetSkipsFailedAccount(t *testing.T) {
	p, s, acc1, acc2 := newRouteTestPool(t)
	cfg := s.GetAutoSwitch()
	cfg.Strategy = store.StrategyRoundRobin
	if err := s.SetAutoSwitchConfig(cfg); err != nil {
		t.Fatal(err)
	}
	exclude := map[string]bool{acc1: true}
	for i := 0; i < 4; i++ {
		if id := pickWith(p, "m", models.RegionDefault, exclude); id != acc2 {
			t.Fatalf("excluded account selected: %q", id)
		}
	}
}

// TestFailoverRoutesToAnotherAccount chat 路径 failover：失败号冷却并排除，
// 请求改投另一账号，返回其 bridge。
func TestFailoverRoutesToAnotherAccount(t *testing.T) {
	p, s, acc1, acc2 := newRouteTestPool(t)
	req := bridge.ResolveRequest{APIKey: "sk-test", Model: "m", Region: models.RegionDefault}
	// 用真实 key 才能通过 LookupKey。
	key, _ := s.AddKey("sk-test", "")
	_ = key
	next := p.handleChatFailover(acc1, req, map[string]bool{})
	if next == nil {
		t.Fatal("failover should route to another account")
	}
	if next.AccountID == acc1 {
		t.Errorf("failover must not reuse the failed account")
	}
	// 失败号进冷却快照。
	if snap := p.snapshotFor(acc1); snap == nil || snap.CooldownUntil <= time.Now().Unix() {
		t.Errorf("failed account not cooling: %+v", snap)
	}
	_ = acc2
}

// TestApplyQuotaOrgOnlyAccountNotZeroed 纯组织包账号（Teams）的 userQuota
// 上游恒为 0/0：快照必须采用组织包而非被清零（PR #6 修复的回归）。
func TestApplyQuotaOrgOnlyAccountNotZeroed(t *testing.T) {
	p, _, acc1, _ := newRouteTestPool(t)
	p.applyQuotaToSnapshot(acc1, &auth.AccountStatus{
		UserQuota:          &auth.Quota{Total: 0, Remaining: 0},
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
	p, _, acc1, _ := newRouteTestPool(t)
	p.applyQuotaToSnapshot(acc1, &auth.AccountStatus{
		UserQuota:          &auth.Quota{Total: 0, Remaining: 0},
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
				UserQuota:          &auth.Quota{Total: 100, Remaining: 60},
				AddOnQuota:         &auth.Quota{Total: 50, Remaining: 50},
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
	p, _, acc1, _ := newRouteTestPool(t)
	resetMs := time.Now().Add(48 * time.Hour).UnixMilli()
	p.applyQuotaToSnapshot(acc1, &auth.AccountStatus{
		UserQuota:       &auth.Quota{Total: 100, Remaining: 0},
		IsQuotaExceeded: true,
		NextResetAtMs:   resetMs,
	})
	snap := p.snapshots[acc1]
	if snap.CooldownUntil != resetMs/1000 {
		t.Errorf("cooldown = %d, want reset %d", snap.CooldownUntil, resetMs/1000)
	}
	// 恢复
	p.applyQuotaToSnapshot(acc1, &auth.AccountStatus{
		UserQuota:       &auth.Quota{Total: 100, Remaining: 50},
		IsQuotaExceeded: false,
	})
	if p.snapshots[acc1].CooldownUntil != 0 {
		t.Error("recovered account must clear the cooldown")
	}
}

// TestFlushAccountSnapshots 被动扣减的内存快照按 30s 循环批量落盘。
func TestFlushAccountSnapshots(t *testing.T) {
	p, s, acc1, _ := newRouteTestPool(t)
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
	p.deductCredits(acc.ID, 5) // 无快照：不能崩、不能切
	if s.GetActiveAccountID() != acc.ID {
		t.Error("deduction without baseline must not switch")
	}
	if p.snapshots[acc.ID] != nil {
		t.Error("deduction without baseline must not fabricate a snapshot")
	}
}

// TestRouterWeightedDistribution 权重不同时按比例分配（平滑加权轮询）。
func TestRouterWeightedDistribution(t *testing.T) {
	s, err := store.New(t.TempDir() + "/data.json")
	if err != nil {
		t.Fatal(err)
	}
	a, _ := s.AddAccount("pt-a", "A")
	b, _ := s.AddAccount("pt-b", "B")
	s.SetAutoSwitch(true, 20)
	cfg := s.GetAutoSwitch()
	cfg.Strategy = store.StrategyRoundRobin
	if err := s.SetAutoSwitchConfig(cfg); err != nil {
		t.Fatal(err)
	}
	// A 权重 3，B 权重 1 → 期望约 3:1。
	if err := s.SetAccountWeight(a.ID, 3); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAccountWeight(b.ID, 1); err != nil {
		t.Fatal(err)
	}
	p := newBridgeProvider(s)
	counts := map[string]int{}
	const n = 40
	for i := 0; i < n; i++ {
		id := pickWith(p, "m", models.RegionDefault, nil)
		counts[id]++
	}
	ratio := float64(counts[a.ID]) / float64(counts[b.ID])
	if ratio < 2.5 || ratio > 3.5 {
		t.Errorf("weight 3:1 distribution off: A=%d B=%d ratio=%.2f", counts[a.ID], counts[b.ID], ratio)
	}
}

// TestRouterEqualWeightsEven 权重相同时退化为均分。
func TestRouterEqualWeightsEven(t *testing.T) {
	s, err := store.New(t.TempDir() + "/data.json")
	if err != nil {
		t.Fatal(err)
	}
	a, _ := s.AddAccount("pt-a", "A")
	b, _ := s.AddAccount("pt-b", "B")
	s.SetAutoSwitch(true, 20)
	cfg := s.GetAutoSwitch()
	cfg.Strategy = store.StrategyRoundRobin
	if err := s.SetAutoSwitchConfig(cfg); err != nil {
		t.Fatal(err)
	}
	p := newBridgeProvider(s)
	counts := map[string]int{}
	for i := 0; i < 20; i++ {
		counts[pickWith(p, "m", models.RegionDefault, nil)]++
	}
	if counts[a.ID] != 10 || counts[b.ID] != 10 {
		t.Errorf("equal weights must split evenly: A=%d B=%d", counts[a.ID], counts[b.ID])
	}
}

// -- Daily check-in scheduling --

func TestCheckinSlot(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.Local)
	got, ok := checkinSlot("09:00", "acct-1", now)
	if !ok {
		t.Fatal("checkinSlot returned !ok for a valid time")
	}
	if got.Hour() != 9 || got.Minute() != 0 || got.Day() != 18 {
		t.Errorf("unexpected slot: %v", got)
	}
	if _, ok := checkinSlot("bad", "acct-1", now); ok {
		t.Error("checkinSlot accepted an invalid time")
	}
}

func TestCheckinDue(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.Local)
	base := store.Account{ID: "a1", PAT: "p", AutoCheckin: true}

	// Disabled auto check-in is never due.
	off := base
	off.AutoCheckin = false
	if checkinDue(off, "09:00", now) {
		t.Error("account without AutoCheckin must not be due")
	}
	// Before the configured time: not due.
	if checkinDue(base, "13:00", now) {
		t.Error("account must not be due before its slot")
	}
	// After the slot, never checked: due.
	if !checkinDue(base, "09:00", now) {
		t.Error("account must be due after its slot when never checked")
	}
	// Already succeeded today: not due.
	done := base
	done.LastCheckinAt = time.Date(2026, 10, 18, 9, 1, 0, 0, time.Local).Unix()
	done.LastCheckinStatus = "success"
	if checkinDue(done, "09:00", now) {
		t.Error("account already checked in today must not be due")
	}
	// Errored recently: within the 30-minute retry cooldown, not due.
	errored := base
	errored.LastCheckinAt = now.Add(-10 * time.Minute).Unix()
	errored.LastCheckinStatus = "error"
	if checkinDue(errored, "09:00", now) {
		t.Error("account in retry cooldown must not be due")
	}
	// Errored long ago: due for retry.
	erroredOld := base
	erroredOld.LastCheckinAt = now.Add(-1 * time.Hour).Unix()
	erroredOld.LastCheckinStatus = "error"
	if !checkinDue(erroredOld, "09:00", now) {
		t.Error("account past retry cooldown must be due")
	}
}

func TestTryAcquireCheckinGuard(t *testing.T) {
	s, err := store.New(t.TempDir() + "/data.json")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	p := newBridgeProvider(s)
	if !p.tryAcquireCheckin("a1") {
		t.Fatal("first acquire should succeed")
	}
	if p.tryAcquireCheckin("a1") {
		t.Error("second concurrent acquire must be rejected")
	}
	p.releaseCheckin("a1")
	if !p.tryAcquireCheckin("a1") {
		t.Error("acquire after release should succeed")
	}
}

func TestCheckinRegionSupported(t *testing.T) {
	for _, r := range []string{"", "cn", "global"} {
		if !CheckinRegionSupported(r) {
			t.Errorf("region %q must be check-in supported", r)
		}
	}
}

func TestCheckinDueIncludesGlobal(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.Local)
	acc := store.Account{ID: "g1", PAT: "p", Region: "global", AutoCheckin: true}
	if !checkinDue(acc, "09:00", now) {
		t.Error("global account with auto check-in must be due after its slot")
	}
}

// 修改备注不应重建 bridge：备注只是展示用的标签，重建会丢掉已预热的会话与
// 机器指纹。PAT / 区域变化仍然重建。
func TestBridgeForAccountIgnoresNote(t *testing.T) {
	s, err := store.New(t.TempDir() + "/data.json")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	acc, err := s.AddAccount("pt-note", "旧备注")
	if err != nil {
		t.Fatalf("AddAccount: %v", err)
	}
	p := newBridgeProvider(s)

	b1 := p.bridgeForAccount(*acc)
	snap := *acc
	snap.Note = "新备注"
	b2 := p.bridgeForAccount(snap)
	if b1 != b2 {
		t.Error("a note change must reuse the existing bridge")
	}
	if got := b2.Label(); !strings.Contains(got, "新备注") {
		t.Errorf("reused bridge label = %q, want the new note", got)
	}

	snap.Region = "global"
	if b3 := p.bridgeForAccount(snap); b3 == b1 {
		t.Error("a region change must rebuild the bridge")
	}
}
