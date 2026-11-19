package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"gitlab.com/system4us/issue-timeline/internal/app"
	"gitlab.com/system4us/issue-timeline/internal/auth"
	"gitlab.com/system4us/issue-timeline/internal/webhook"
)

// Live updates: the pages open on a repo follow GET /api/events (server-sent events) and read again what an event names,
// so a change made by someone else, or read by a sync, shows without polling. The hosts tell the server through one
// webhook (POST /api/webhook), shared by all of them, signed with one secret (WEBHOOK_SECRET).

// heartbeat keeps an idle stream open through proxies, and is when a stream checks that its session still holds.
const heartbeat = 25 * time.Second

// maxHookBody is the largest delivery read: GitHub caps payloads at 25 MB.
const maxHookBody = 25 << 20

// webhookPath is the route the hosts post to. It answers without a session and without the page's write header (a host
// sends neither), and checks the delivery's signature instead.
const webhookPath = "/api/webhook"

// events streams what changes in the repo. Each event goes out as "event: change" with the app.Event as JSON; the items
// the caller may not see are left out, and an event left with none is not sent.
func (s *server) events(w http.ResponseWriter, r *http.Request) {
	rr, ok := s.route(w, r)
	if !ok {
		return
	}
	rc := http.NewResponseController(w)
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no") // nginx: do not hold the stream back
	w.WriteHeader(http.StatusOK)
	// the browser reconnects 3 s after a drop (EventSource does it on its own)
	fmt.Fprint(w, "retry: 3000\n: connected\n\n")
	if rc.Flush() != nil {
		return
	}
	ch, stop := s.app.Subscribe(rr.repo)
	defer stop()
	sid := auth.SessionIDOf(r)
	signedIn := callerOf(r).sess != nil
	tick := time.NewTicker(heartbeat)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case e, open := <-ch:
			if !open {
				return // the server is stopping
			}
			e, keep := s.visibleEvent(r, rr, e)
			if !keep {
				continue
			}
			b, _ := json.Marshal(e)
			if _, err := fmt.Fprintf(w, "event: change\ndata: %s\n\n", b); err != nil || rc.Flush() != nil {
				return
			}
		case <-tick.C:
			// a session that ended (signed out, idle or too old) ends its stream too
			if signedIn && s.sessions.Get(sid) == nil {
				return
			}
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil || rc.Flush() != nil {
				return
			}
		}
	}
}

// visibleEvent is the event as the caller may see it: the records it names that are hidden from them are dropped. An
// event about records only (an edit) with none left is not sent; a sync's event always is (it carries no record).
func (s *server) visibleEvent(r *http.Request, rr repoRoute, e app.Event) (app.Event, bool) {
	if len(e.Items) == 0 {
		return e, true
	}
	v, err := s.app.Visibility(r.Context(), rr.repo, rr.who)
	if err != nil {
		return e, false // unsure what they may see: say nothing, the next sync event refreshes the page anyway
	}
	items := make([]app.EventItem, 0, len(e.Items))
	for _, it := range e.Items {
		rec, ok := s.app.Record(rr.repo, rr.linked, it.Kind, it.Key)
		if !ok {
			continue
		}
		if _, ok := app.VisibleRecord(rec, v); ok {
			items = append(items, it)
		}
	}
	if len(items) == 0 {
		return e, false
	}
	e.Items = items
	return e, true
}

// receiveWebhook takes a delivery from any host: checked against the secret, it syncs the repos it names that are
// configured here. Nothing of the payload is kept; the sync reads what changed.
func (s *server) receiveWebhook(w http.ResponseWriter, r *http.Request) {
	if s.hookSecret == "" {
		writeError(w, http.StatusNotFound, "webhooks are off: start the server with WEBHOOK_SECRET set")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxHookBody))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "the delivery is larger than 25 MB")
		return
	}
	hook, err := webhook.Parse(r.Header, r.URL.Query(), body, s.hookSecret)
	switch {
	case errors.Is(err, webhook.ErrUnsigned), errors.Is(err, webhook.ErrBadSignature):
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	case err != nil:
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	matched := []string{}
	if hook.Event != "ping" {
		if m := s.app.HookReceived(hook.Repos); m != nil {
			matched = m
		}
	}
	// 202 whether or not a repo matched: a host retries a failed delivery, and a repo not configured here never will be
	writeJSON(w, http.StatusAccepted, map[string]any{"provider": hook.Provider, "event": hook.Event, "repos": matched})
}
