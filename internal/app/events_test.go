package app

import (
	"context"
	"testing"
	"time"

	"gitlab.com/system4us/issue-timeline/internal/provider"
)

// next is the next event of a subscription, or a failure after a while.
func next(t *testing.T, ch <-chan Event) Event {
	t.Helper()
	select {
	case e, ok := <-ch:
		if !ok {
			t.Fatal("the subscription closed")
		}
		return e
	case <-time.After(5 * time.Second):
		t.Fatal("no event within 5 s")
	}
	return Event{}
}

// until reads events until one of the given type, and returns the ones read before it with it last.
func until(t *testing.T, ch <-chan Event, typ string) []Event {
	t.Helper()
	var got []Event
	for {
		e := next(t, ch)
		got = append(got, e)
		if e.Type == typ {
			return got
		}
	}
}

func TestTheHubSendsEachRepoItsOwnEventsAndClosesWithTheServer(t *testing.T) {
	var h hub
	a, stopA := h.subscribe("o/a")
	b, _ := h.subscribe("o/b")
	h.publish(Event{Repo: "o/a", Type: "edit"})
	if e := next(t, a); e.Type != "edit" || e.At == "" {
		t.Fatalf("o/a gets its event, with a time: %+v", e)
	}
	select {
	case e := <-b:
		t.Fatalf("o/b got another repo's event: %+v", e)
	default:
	}
	if h.subscribers("o/a") != 1 {
		t.Fatal("one page follows o/a")
	}
	stopA()
	stopA() // ending twice is harmless
	if h.subscribers("o/a") != 0 {
		t.Fatal("the ended subscription is gone")
	}
	// a page that does not read does not block the others: its buffer fills, the rest is dropped for it
	for range subBuffer + 10 {
		h.publish(Event{Repo: "o/b", Type: "sync-end"})
	}
	h.close()
	n := 0
	for range b {
		n++
	}
	if n != subBuffer {
		t.Fatalf("a slow page keeps the first %d events, got %d", subBuffer, n)
	}
	if ch, _ := h.subscribe("o/a"); ch != nil {
		if _, open := <-ch; open {
			t.Fatal("after the server stops, a new subscription is closed at once")
		}
	}
}

func TestAnEditTellsTheOpenPagesAndASyncFollows(t *testing.T) {
	dir := t.TempDir()
	writeNodeFiles(t, dir)
	a := newTestApp(t, dir, fixture{role: provider.RoleEditor}, nil)
	a.impl.soonWait = 20 * time.Millisecond
	ch, stop := a.Subscribe(codeRepo)
	defer stop()
	ch2 := provider.Changes{"title": jsonOf(t, "Renamed")}
	if _, _, err := a.EditIssue(context.Background(), codeRepo, provider.Cred{}, "1", ch2); err != nil {
		t.Fatal(err)
	}
	e := next(t, ch)
	if e.Type != "edit" || len(e.Items) != 1 || e.Items[0] != (EventItem{Kind: "issue", Key: "1"}) {
		t.Fatalf("the edit names its record: %+v", e)
	}
	got := until(t, ch, "sync-end")
	if got[0].Type != "sync-start" {
		t.Fatalf("then the sync it asked for: %+v", got)
	}
	waitIdle(t, a, codeRepo)
}

func TestABurstOfDeliveriesIsOneSyncAndQuietsThePoll(t *testing.T) {
	dir := t.TempDir()
	writeNodeFiles(t, dir)
	a := newTestApp(t, dir, fixture{}, nil)
	a.impl.soonWait = 30 * time.Millisecond
	ch, stop := a.Subscribe(codeRepo)
	defer stop()
	for range 5 {
		if m := a.HookReceived([]string{"ACME/App", "someone/else"}); len(m) != 1 || m[0] != codeRepo {
			t.Fatalf("the configured repo matches without case, the other one does not: %v", m)
		}
	}
	until(t, ch, "sync-end")
	select {
	case e := <-ch:
		t.Fatalf("five deliveries in a burst are one sync, got another event: %+v", e)
	case <-time.After(150 * time.Millisecond):
	}
	es, _ := a.impl.get(codeRepo)
	if !es.hookFresh(time.Now()) {
		t.Fatal("after a delivery the poll leaves the repo alone")
	}
	if es.hookFresh(time.Now().Add(hookQuiet + time.Minute)) {
		t.Fatal("and comes back once the deliveries stop")
	}
	if m := a.HookReceived([]string{"someone/else"}); len(m) != 0 {
		t.Fatalf("a repo not configured here matches nothing: %v", m)
	}
	waitIdle(t, a, codeRepo)
}

func TestADeliveryDuringASyncIsReadByTheNextOne(t *testing.T) {
	dir := t.TempDir()
	writeNodeFiles(t, dir)
	a := newTestApp(t, dir, fixture{}, nil)
	a.impl.soonWait = time.Millisecond
	ch, stop := a.Subscribe(codeRepo)
	defer stop()
	es, _ := a.impl.get(codeRepo)
	// a sync is running: the one a delivery asks for cannot start, so it follows
	es.update(func(st *Status) { st.Syncing = true })
	a.HookReceived([]string{codeRepo})
	deadline := time.Now().Add(5 * time.Second)
	for {
		es.mu.Lock()
		again := es.again
		es.mu.Unlock()
		if again {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the delivery's sync is not kept for later")
		}
		time.Sleep(2 * time.Millisecond)
	}
	es.update(func(st *Status) { st.Syncing = false })
	// the running sync ends (simulated here by starting one, whose end runs the kept one)
	a.RunSync(codeRepo, false)
	until(t, ch, "sync-end")
	got := until(t, ch, "sync-end")
	if got[0].Type != "sync-start" {
		t.Fatalf("a second sync followed: %+v", got)
	}
	waitIdle(t, a, codeRepo)
}
