package app

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"gitlab.com/system4us/issue-timeline/internal/auth"
	"gitlab.com/system4us/issue-timeline/internal/model"
	"gitlab.com/system4us/issue-timeline/internal/provider"
	"gitlab.com/system4us/issue-timeline/internal/store"
)

// visHost is a signed-in host whose visibility is every issue in the store, and whose sync adds one issue each time.
type visHost struct {
	*signedHost
	mu    sync.Mutex
	asks  int
	syncs int
}

func (h *visHost) VisibilityOf(_ context.Context, _ provider.Cred, _ provider.User, _ provider.Role, issues []*model.Record) (provider.Visibility, error) {
	h.mu.Lock()
	h.asks++
	h.mu.Unlock()
	v := provider.Visibility{Issues: map[string]bool{}}
	for _, r := range issues {
		v.Issues[r.Key] = true
	}
	return v, nil
}

// Sync adds issue number n (the next one) and sets a new last sync, as a real sync does.
func (h *visHost) Sync(_ context.Context, st *store.Store, _ provider.SyncOptions) (store.SyncEntry, error) {
	h.mu.Lock()
	h.syncs++
	n := h.syncs
	h.mu.Unlock()
	at := fmt.Sprintf("2026-11-18T10:00:%02d.000Z", n)
	key := fmt.Sprint(100 + n)
	st.Update(func(d *store.Data) {
		d.Meta.LastSync = &at
		d.Meta.FullSyncAt = &at
		d.Upsert("issues", &model.Record{Kind: "issue", ID: key, Key: key, Title: "new " + key, State: "OPEN", CreatedAt: at, UpdatedAt: at})
	})
	return store.SyncEntry{At: at, Full: true, Issues: 1}, st.Save()
}

// After a sync adds an issue the person may see, the next visibility is read again: the issue is not hidden for the
// rest of the cache's five minutes.
func TestASyncMakesTheNextVisibilityRecompute(t *testing.T) {
	dir := t.TempDir()
	writeRepos(t, dir, codeRepo)
	host := &visHost{signedHost: &signedHost{base: base{key: codeRepo, prov: "github", origin: "github"}, role: provider.RoleViewer}}
	a, err := New(Config{
		StoreDir:     dir,
		PollInterval: 0,
		Env:          func(string) string { return "" },
		Log:          func(string, ...any) {},
		Hosts:        func(string) (provider.Host, error) { return host, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if !a.RunSync(codeRepo, false) {
		t.Fatal("no sync started")
	}
	waitIdle(t, a, codeRepo)

	sessions := auth.NewSessions(auth.SessionOptions{})
	sess := sessions.Get(sessions.Create("github", "github", "tok", provider.User{ID: 1, Login: "ann"}))
	who := Who{Session: sess}
	ctx := context.Background()

	v, err := a.Visibility(ctx, codeRepo, who)
	if err != nil {
		t.Fatal(err)
	}
	if !v.Issues["101"] || v.Issues["102"] {
		t.Fatalf("before the second sync: %v", v.Issues)
	}
	if _, err := a.Visibility(ctx, codeRepo, who); err != nil {
		t.Fatal(err)
	}
	host.mu.Lock()
	cached := host.asks
	host.mu.Unlock()
	if cached != 1 {
		t.Fatalf("the same sync asked the host %d times, want the cache to answer", cached)
	}

	if !a.RunSync(codeRepo, false) {
		t.Fatal("no second sync started")
	}
	waitIdle(t, a, codeRepo)
	v, err = a.Visibility(ctx, codeRepo, who)
	if err != nil {
		t.Fatal(err)
	}
	if !v.Issues["102"] {
		t.Fatal("the issue added by the sync is still hidden: the visibility was not read again")
	}
}
