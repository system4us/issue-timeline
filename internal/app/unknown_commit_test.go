package app

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"gitlab.com/system4us/issue-timeline/internal/commits"
	"gitlab.com/system4us/issue-timeline/internal/model"
	"gitlab.com/system4us/issue-timeline/internal/provider"
)

// emptyCommits is a code host that knows no commit: its CommitFiles answers nothing.
type emptyCommits struct {
	codeHost
	asked atomic.Int32
}

func (e *emptyCommits) CommitFiles(context.Context, string) (*model.CommitInfo, []model.HostFile, error) {
	e.asked.Add(1)
	return nil, nil, nil
}

// A sha that the repo does not know and the host does not show is not found, and nothing is cached for it.
func TestAnUnknownCommitIsNotFoundAndNotCached(t *testing.T) {
	dir := t.TempDir()
	writeRepos(t, dir, codeRepo)
	host := &emptyCommits{codeHost: codeHost{signedHost{base: base{key: codeRepo, prov: "github", origin: "github"}}}}
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

	sha := strings.Repeat("c", 40)
	_, err = a.Commit(context.Background(), codeRepo, nil, sha)
	if !errors.Is(err, provider.ErrNotFound) {
		t.Fatalf("err = %v, want not found", err)
	}
	if host.asked.Load() != 1 {
		t.Fatalf("the host was asked %d times", host.asked.Load())
	}
	cached, err := commits.ReadCachedDiff(commits.DiffDirFor(dir, codeRepo), sha)
	if err != nil || cached != nil {
		t.Fatalf("a diff was cached for an unknown commit: %+v, %v", cached, err)
	}
}
