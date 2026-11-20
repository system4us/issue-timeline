package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gitlab.com/system4us/issue-timeline/internal/app"
	"gitlab.com/system4us/issue-timeline/internal/auth"
	"gitlab.com/system4us/issue-timeline/internal/credentials"
	"gitlab.com/system4us/issue-timeline/internal/model"
	"gitlab.com/system4us/issue-timeline/internal/provider"
)

// blindHost signs a person in as a viewer who sees no issue of the repo (all confidential to them, say).
type blindHost struct{ *viewerHost }

func (h *blindHost) VisibilityOf(context.Context, provider.Cred, provider.User, provider.Role, []*model.Record) (provider.Visibility, error) {
	return provider.Visibility{Issues: map[string]bool{}}, nil
}

// liveServer is a real HTTP server over an app whose one repo, owner/name (write mode), has the given host, with a
// session of a person signed into it. It waits for the first sync.
func liveServer(t *testing.T, host provider.Host) (*httptest.Server, string) {
	t.Helper()
	dir := t.TempDir()
	a, err := app.New(app.Config{
		StoreDir:     dir,
		PollInterval: time.Hour,
		Env:          func(string) string { return "" },
		Secrets:      &credentials.Store{Dir: dir, Env: func(string) string { return "" }, Keyring: memKeyring{}},
		Log:          t.Logf,
		Hosts:        func(string) (provider.Host, error) { return host, nil },
		OwnToken:     func(context.Context, string) (string, error) { return "local-token", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Stop)
	if _, _, err := a.AddRepo(context.Background(), "github", "owner/name", "write"); err != nil {
		t.Fatal(err)
	}
	o := opts(nil)
	sid := o.Sessions.Create("github", "github", "tok", provider.User{ID: 1, Login: "ann"})
	srv := httptest.NewServer(New(a, o))
	t.Cleanup(srv.Close)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if d, err := a.Data("owner/name", nil); err == nil && len(d.Payload.Issues) > 0 && !d.Status.Syncing {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the first sync stored nothing")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return srv, sid
}

func newViewerHost() *viewerHost {
	return &viewerHost{fakeCode: &fakeCode{&fakeHost{key: "owner/name", prov: "github", origin: "github"}}}
}

// stream opens GET /api/events, with the session cookie when sid is not empty, and returns its events.
func stream(t *testing.T, ctx context.Context, base, sid string) <-chan app.Event {
	t.Helper()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/events?repo=owner%2Fname", nil)
	if sid != "" {
		req.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: sid})
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusOK || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("the stream answers %d %s", res.StatusCode, res.Header.Get("Content-Type"))
	}
	out := make(chan app.Event, 64)
	go func() {
		defer close(out)
		defer res.Body.Close()
		sc := bufio.NewScanner(res.Body)
		for sc.Scan() {
			if data, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
				var e app.Event
				if json.Unmarshal([]byte(data), &e) == nil {
					out <- e
				}
			}
		}
	}()
	return out
}

// await reads events until one matches, and returns every event read, failing after a while.
func await(t *testing.T, ch <-chan app.Event, what string, match func(app.Event) bool) []app.Event {
	t.Helper()
	var seen []app.Event
	timeout := time.After(5 * time.Second)
	for {
		select {
		case e, ok := <-ch:
			if !ok {
				t.Fatalf("the stream ended before %s; seen %+v", what, seen)
			}
			seen = append(seen, e)
			if match(e) {
				return seen
			}
		case <-timeout:
			t.Fatalf("no %s within 5 s; seen %+v", what, seen)
		}
	}
}

// send makes a write as the page does (the write header), from this machine.
func send(t *testing.T, method, u, body string) int {
	t.Helper()
	req, _ := http.NewRequest(method, u, strings.NewReader(body))
	req.Header.Set("x-issue-timeline", "1")
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	return res.StatusCode
}

func TestAnEditReachesEveryOpenPage(t *testing.T) {
	srv, sid := liveServer(t, newViewerHost())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	local := stream(t, ctx, srv.URL, "")
	viewer := stream(t, ctx, srv.URL, sid)
	if code := send(t, http.MethodPatch, srv.URL+"/api/issues/1?repo=owner%2Fname", `{"title":"Renamed"}`); code != http.StatusOK {
		t.Fatalf("edit: %d", code)
	}
	isEdit := func(e app.Event) bool {
		return e.Type == "edit" && len(e.Items) == 1 && e.Items[0] == app.EventItem{Kind: "issue", Key: "1"}
	}
	await(t, local, "the edit, on this machine's page", isEdit)
	await(t, viewer, "the edit, on the signed-in viewer's page", isEdit)
}

func TestAnEditOfAnIssueThePersonMayNotSeeIsNotSentToThem(t *testing.T) {
	srv, sid := liveServer(t, &blindHost{newViewerHost()})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	blind := stream(t, ctx, srv.URL, sid)
	local := stream(t, ctx, srv.URL, "")
	if code := send(t, http.MethodPatch, srv.URL+"/api/issues/1?repo=owner%2Fname", `{"title":"Renamed"}`); code != http.StatusOK {
		t.Fatalf("edit: %d", code)
	}
	await(t, local, "the edit on this machine's page", func(e app.Event) bool { return e.Type == "edit" })
	// a sync next: it names no record, so every page hears about it
	if code := send(t, http.MethodPost, srv.URL+"/api/sync?repo=owner%2Fname", ""); code != http.StatusAccepted {
		t.Fatalf("sync: %d", code)
	}
	for _, e := range await(t, blind, "the sync's end", func(e app.Event) bool { return e.Type == "sync-end" }) {
		if e.Type == "edit" {
			t.Fatalf("an edit of a hidden issue reached the person: %+v", e)
		}
	}
}

func TestAStreamNeedsASessionFromAnotherMachine(t *testing.T) {
	srv, _ := liveServer(t, newViewerHost())
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/events?repo=owner%2Fname", nil)
	req.Header.Set("X-Forwarded-For", "203.0.113.9") // through a proxy: not this machine
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("a stream without a session from elsewhere: %d", res.StatusCode)
	}
}
