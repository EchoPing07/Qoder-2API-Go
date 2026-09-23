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
