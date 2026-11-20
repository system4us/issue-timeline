package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"gitlab.com/system4us/issue-timeline/internal/credentials"
)

// keyring is an in-memory keyring, so no test reaches the OS keyring.
type keyring map[string]string

func (k keyring) Get(name string) (string, error) {
	if v, ok := k[name]; ok {
		return v, nil
	}
	return "", credentials.ErrNotFound
}

func (k keyring) Set(name, value string) error { k[name] = value; return nil }
func (k keyring) Delete(name string) error     { delete(k, name); return nil }

// Two adds of the same new repo at once: one is new, the other is told it was already there.
func TestConcurrentAddsOfOneRepoAddItOnce(t *testing.T) {
	a := newTestApp(t, t.TempDir(), fixture{}, nil)
	var wg sync.WaitGroup
	var mu sync.Mutex
	added := 0
	errs := make([]error, 0)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, ok, err := a.AddRepo(context.Background(), "github", newRepo, "read")
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
			}
			if ok {
				added++
			}
		}()
	}
	wg.Wait()
	if len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if added != 1 {
		t.Fatalf("added %d times, want once", added)
	}
	if n := len(a.Repos()); n != 1 {
		t.Fatalf("repos: %v", a.Repos())
	}
	waitIdle(t, a, newRepo)
}

// A plain Redmine key is not bound to a new site by adding a repo of it: the add needs the site's own key, and says so.
func TestAddingARepoOfANewSiteDoesNotBindThePlainKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Redmine-API-Key") != "plain-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"project": map[string]any{"id": 1, "identifier": "backend"}})
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	secrets := &credentials.Store{Dir: dir, Env: func(string) string { return "" }, Keyring: keyring{}}
	if _, err := secrets.Set("redmine-api-key", "plain-key"); err != nil {
		t.Fatal(err)
	}
	a, err := New(Config{
		StoreDir:     dir,
		PollInterval: time.Hour,
		Env:          func(string) string { return "" },
		Secrets:      secrets,
		Log:          func(string, ...any) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	input := srv.URL + "/projects/backend"
	_, _, err = a.AddRepo(context.Background(), "redmine", input, "read")
	if err == nil {
		t.Fatal("a new site was added with only a plain key")
	}
	want := "issue-timeline credentials set redmine-api-key " + srv.URL
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("the error does not say what to run: %v", err)
	}
	if bound, _ := secrets.BoundOrigins(); len(bound) != 0 {
		t.Fatalf("adding a repo bound the plain key: %v", bound)
	}
	if n := len(a.Repos()); n != 0 {
		t.Fatalf("a repo was configured anyway: %v", a.Repos())
	}

	if _, err := secrets.Set("redmine-api-key@"+srv.URL, "plain-key"); err != nil {
		t.Fatal(err)
	}
	repo, added, err := a.AddRepo(context.Background(), "redmine", input, "read")
	if err != nil || !added {
		t.Fatalf("with the site's own key: %q, %v, %v", repo, added, err)
	}
	waitIdle(t, a, repo)
	if bound, _ := secrets.BoundOrigins(); len(bound) != 0 {
		t.Fatalf("the plain key was bound by adding a repo: %v", bound)
	}
}
