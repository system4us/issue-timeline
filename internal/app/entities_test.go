package app

import (
	"context"
	"gitlab.com/system4us/issue-timeline/internal/model"
	"sync"
	"testing"
	"time"

	"gitlab.com/system4us/issue-timeline/internal/provider"
)

// creatorHost is a repo that lists edit options and can create entities: it counts how often the options are read.
type creatorHost struct {
	base
	mu    *sync.Mutex
	reads *int
}

func (h *creatorHost) EditIssue(context.Context, provider.Cred, *model.Record, provider.Changes) (*model.Record, []string, error) {
	return nil, nil, nil
}
func (h *creatorHost) CreateIssue(context.Context, provider.Cred, provider.Changes) (*model.Record, error) {
	return nil, nil
}
func (h *creatorHost) EditOptions(context.Context, provider.Cred) (provider.EditOptions, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	*h.reads++
	return provider.EditOptions{}, nil
}
func (h *creatorHost) EntityKinds() []string { return []string{provider.EntityLabel} }
func (h *creatorHost) CreateEntity(_ context.Context, _ provider.Cred, e provider.NewEntity) (map[string]any, error) {
	return map[string]any{"name": e.Name}, nil
}

func TestCreatingAnEntityForgetsTheCachedOptions(t *testing.T) {
	var mu sync.Mutex
	reads := 0
	dir := t.TempDir()
	writeRepos(t, dir, "acme/app")
	a, err := New(Config{
		StoreDir: dir, PollInterval: time.Hour, Env: func(string) string { return "" }, Log: func(string, ...any) {},
		Hosts: func(repo string) (provider.Host, error) {
			return &creatorHost{base: base{key: repo, prov: "github", origin: "github"}, mu: &mu, reads: &reads}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Stop(); waitIdle(t, a, "acme/app") })
	ctx := context.Background()
	for i := 0; i < 2; i++ { // the second read is the cache's
		if _, err := a.EditOptions(ctx, "acme/app", provider.Cred{}, true); err != nil {
			t.Fatal(err)
		}
	}
	if reads != 1 {
		t.Fatalf("the options were read %d times, want 1 (cached)", reads)
	}
	if got := a.EntityKinds("acme/app"); len(got) != 1 || got[0] != provider.EntityLabel {
		t.Fatalf("kinds %v", got)
	}
	out, err := a.CreateEntity(ctx, "acme/app", provider.Cred{}, provider.NewEntity{Kind: provider.EntityLabel, Name: "p1"})
	if err != nil || out["name"] != "p1" {
		t.Fatalf("%v, %v", out, err)
	}
	if _, err := a.EditOptions(ctx, "acme/app", provider.Cred{}, true); err != nil {
		t.Fatal(err)
	}
	if reads != 2 {
		t.Fatalf("after creating one, the options were not read again (reads %d)", reads)
	}
	if _, err := a.CreateEntity(ctx, "acme/app", provider.Cred{}, provider.NewEntity{Kind: provider.EntityVersion, Name: "x"}); err == nil {
		t.Fatal("a kind the host cannot create must be refused")
	}
}

func TestAHostThatCannotCreateAnswers501(t *testing.T) {
	dir := t.TempDir()
	writeRepos(t, dir, "acme/app")
	a, err := New(Config{
		StoreDir: dir, PollInterval: time.Hour, Env: func(string) string { return "" }, Log: func(string, ...any) {},
		Hosts: func(repo string) (provider.Host, error) {
			return &base{key: repo, prov: "github", origin: "github"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Stop(); waitIdle(t, a, "acme/app") })
	_, err = a.CreateEntity(context.Background(), "acme/app", provider.Cred{}, provider.NewEntity{Kind: provider.EntityLabel, Name: "x"})
	if pe, ok := err.(*provider.Error); !ok || pe.Status != 501 {
		t.Fatalf("want 501, got %v", err)
	}
	if a.EntityKinds("acme/app") != nil {
		t.Fatal("no kinds expected")
	}
}
