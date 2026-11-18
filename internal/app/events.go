package app

import (
	"strings"
	"sync"
	"time"
)

// Event is what changed in a repo, for the pages open on it (GET /api/events, server-sent events). It carries no record
// content, only which records and why: each page reads what it may see through the usual routes, with their
// visibility.
type Event struct {
	Repo  string      `json:"repo"`
	Type  string      `json:"type"` // "sync-start", "sync-end" (Changed: how many records the sync brought) or "edit"
	Items []EventItem `json:"items,omitempty"`
	// Changed is the number of issues and pulls a sync read again (0: nothing new, the page needs only the status).
	Changed int    `json:"changed"`
	At      string `json:"at"`
}

// EventItem names one record an event is about.
type EventItem struct {
	Kind string `json:"kind"` // "issue" or "pull"
	Key  string `json:"key"`
}

// subBuffer is how many events wait for a slow page before the newest are dropped: a page that misses one reads the
// data again on the next, so dropping never loses a change for good.
const subBuffer = 32

// hub hands every event to the subscriptions of its repo. Publishing never blocks.
type hub struct {
	mu     sync.Mutex
	subs   map[chan Event]string // channel -> repo
	closed bool
}

// subscribe returns the events of repo, and the function that ends the subscription. After the hub closes (the server
// stops), the channel is closed.
func (h *hub) subscribe(repo string) (<-chan Event, func()) {
	ch := make(chan Event, subBuffer)
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		close(ch)
		return ch, func() {}
	}
	if h.subs == nil {
		h.subs = map[chan Event]string{}
	}
	h.subs[ch] = repo
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			h.mu.Lock()
			defer h.mu.Unlock()
			if _, ok := h.subs[ch]; ok {
				delete(h.subs, ch)
				close(ch)
			}
		})
	}
}

// publish sends e to the subscriptions of its repo; a full one skips it.
func (h *hub) publish(e Event) {
	if e.At == "" {
		e.At = time.Now().UTC().Format(time.RFC3339Nano)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch, repo := range h.subs {
		if repo != e.Repo {
			continue
		}
		select {
		case ch <- e:
		default:
		}
	}
}

// close ends every subscription: the streams return, so the server's shutdown does not wait for them.
func (h *hub) close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	for ch := range h.subs {
		close(ch)
	}
	h.subs = nil
}

// subscribers is how many pages follow repo (for the tests and the logs).
func (h *hub) subscribers(repo string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, r := range h.subs {
		if r == repo {
			n++
		}
	}
	return n
}

// soonDelay is how long a sync asked for by an edit or a webhook waits, so a burst (a host sends several deliveries for
// one change; a form saves several fields) becomes one sync.
const soonDelay = 2 * time.Second

// syncSoon runs a sync of repo after soonDelay, once for every request that arrives in that time. When a sync is
// already running, another one follows it, so what changed during it is read too.
func (s *state) syncSoon(repo string) {
	es, ok := s.get(repo)
	if !ok || s.isStopped() {
		return
	}
	es.mu.Lock()
	defer es.mu.Unlock()
	if es.soon != nil {
		return
	}
	es.soon = time.AfterFunc(s.soonDelay(), func() {
		es.mu.Lock()
		es.soon = nil
		es.mu.Unlock()
		if !s.runSync(repo, false) {
			es.mu.Lock()
			es.again = true
			es.mu.Unlock()
		}
	})
}

// soonDelay is soonDelay, shorter in tests.
func (s *state) soonDelay() time.Duration {
	if s.soonWait > 0 {
		return s.soonWait
	}
	return soonDelay
}

// hookQuiet is how long after a webhook delivery the periodic poll leaves a repo alone: the host tells when something
// moves. If the deliveries stop (the hook was removed, the tunnel is down), the poll comes back on its own after it.
const hookQuiet = 30 * time.Minute

// hookReceived notes a delivery for the repos it names that are configured here (compared without case) and syncs each
// one soon. It returns the repos it matched.
func (s *state) hookReceived(names []string) []string {
	var matched []string
	for _, repo := range s.repos() {
		for _, n := range names {
			if strings.EqualFold(repo, n) {
				matched = append(matched, repo)
				break
			}
		}
	}
	now := time.Now()
	for _, repo := range matched {
		if es, ok := s.get(repo); ok {
			es.mu.Lock()
			es.lastHook = now
			es.mu.Unlock()
		}
		s.syncSoon(repo)
	}
	return matched
}

// hookFresh reports whether a webhook delivery for the repo arrived within hookQuiet: then the poll skips it.
func (es *entryState) hookFresh(now time.Time) bool {
	es.mu.Lock()
	defer es.mu.Unlock()
	return !es.lastHook.IsZero() && now.Sub(es.lastHook) < hookQuiet
}

// stop cancels the syncs waiting to start and ends the event streams: the server is going away (or a test ended).
// The syncs already running finish on their own; none starts after this.
func (s *state) stop() {
	s.mu.Lock()
	s.stopped = true
	entries := make([]*entryState, 0, len(s.entries))
	for _, es := range s.entries {
		entries = append(entries, es)
	}
	s.mu.Unlock()
	for _, es := range entries {
		es.mu.Lock()
		if es.soon != nil {
			es.soon.Stop()
			es.soon = nil
		}
		es.again = false
		es.mu.Unlock()
	}
	s.events.close()
}

// isStopped reports whether stop was called.
func (s *state) isStopped() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.stopped
}
