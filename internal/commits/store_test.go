package commits

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gitlab.com/system4us/issue-timeline/internal/model"
)

const (
	shaA = "1111111111111111111111111111111111111111"
	shaB = "2222222222222222222222222222222222222222"
	shaC = "3333333333333333333333333333333333333333"
	shaD = "4444444444444444444444444444444444444444"
)

func commit(sha, title string) model.Commit {
	return model.Commit{Sha: sha, Title: title}
}

// pager serves a fixed history in pages of size n, newest first, as a host does, and counts the pages it gave.
type pager struct {
	history []model.Commit
	n       int
	calls   int
}

func (p *pager) fetch(_ context.Context, cursor string) (model.CommitPage, error) {
	p.calls++
	start := 0
	if cursor != "" {
		for i, c := range p.history {
			if c.Sha == cursor {
				start = i
			}
		}
	}
	end := start + p.n
	if end >= len(p.history) {
		end = len(p.history)
	}
	page := model.CommitPage{Items: append([]model.Commit(nil), p.history[start:end]...)}
	if end < len(p.history) {
		page.Next = p.history[end].Sha
	}
	return page, nil
}

func shas(items []model.Commit) []string {
	out := []string{}
	for _, c := range items {
		out = append(out, c.Sha)
	}
	return out
}

func TestFirstSyncWalksEverythingAndLaterSyncsStopAtKnownCommit(t *testing.T) {
	file := filepath.Join(t.TempDir(), "x.commits.json")
	st, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	history := []model.Commit{commit(shaA, "a"), commit(shaB, "b"), commit(shaC, "c"), commit(shaD, "d")}
	p := &pager{history: history, n: 2}

	added, err := Sync(context.Background(), st, "owner/repo", p.fetch)
	if err != nil {
		t.Fatal(err)
	}
	if added != 4 || p.calls != 2 {
		t.Fatalf("first sync: added %d in %d pages, want 4 in 2", added, p.calls)
	}
	if got := shas(st.Items()); !reflect.DeepEqual(got, []string{shaA, shaB, shaC, shaD}) {
		t.Fatalf("items = %v", got)
	}

	// A new commit lands on top; the next sync reads one page and stops at the known one.
	newer := append([]model.Commit{commit(shaC[:0]+"5555555555555555555555555555555555555555", "e")}, history...)
	p2 := &pager{history: newer, n: 2}
	added, err = Sync(context.Background(), st, "owner/repo", p2.fetch)
	if err != nil {
		t.Fatal(err)
	}
	if added != 1 || p2.calls != 1 {
		t.Fatalf("later sync: added %d in %d pages, want 1 in 1", added, p2.calls)
	}
	items := st.Items()
	if items[0].Sha != "5555555555555555555555555555555555555555" || len(items) != 5 {
		t.Fatalf("new commit not on top: %v", shas(items))
	}
	if st.LastSync() == nil {
		t.Fatal("lastSync not recorded")
	}
	if c, ok := st.Get(shaB); !ok || c.Title != "b" || !st.Has(shaD) || st.Has("nope") {
		t.Fatalf("Get/Has wrong: %v %v", c, ok)
	}
}

func TestAnotherRepoStartsAgain(t *testing.T) {
	st, _ := Open(filepath.Join(t.TempDir(), "x.commits.json"))
	p := &pager{history: []model.Commit{commit(shaA, "a")}, n: 10}
	if _, err := Sync(context.Background(), st, "one/repo", p.fetch); err != nil {
		t.Fatal(err)
	}
	p2 := &pager{history: []model.Commit{commit(shaB, "b")}, n: 10}
	added, err := Sync(context.Background(), st, "two/repo", p2.fetch)
	if err != nil {
		t.Fatal(err)
	}
	if added != 1 || st.Has(shaA) || !st.Has(shaB) {
		t.Fatalf("switching repo kept the old history: added %d, items %v", added, shas(st.Items()))
	}
}

func TestSyncReportsHostErrorsAndRepeatedCursors(t *testing.T) {
	st, _ := Open(filepath.Join(t.TempDir(), "x.commits.json"))
	_, err := Sync(context.Background(), st, "o/r", func(context.Context, string) (model.CommitPage, error) {
		return model.CommitPage{}, errors.New("boom")
	})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("host error not passed on: %v", err)
	}
	_, err = Sync(context.Background(), st, "o/r", func(_ context.Context, cursor string) (model.CommitPage, error) {
		return model.CommitPage{Items: []model.Commit{commit(shaA, "a")}, Next: "same"}, nil
	})
	if err == nil {
		t.Fatal("a host that repeats its cursor must stop the sync")
	}
}

// A store written by the Node server (commits.js CommitStore.save) is read, and written back with the same fields.
func TestStoreFileKeepsTheNodeShape(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "repo.commits.json")
	js := `{"meta":{"repo":"owner/repo","lastSync":"2026-01-02T03:04:05.000Z"},"items":[` +
		`{"sha":"` + shaA + `","title":"first","author":"ana","at":"2026-01-02T03:00:00Z","additions":3,"deletions":1},` +
		`{"sha":"` + shaB + `","title":"second","author":null,"at":null,"additions":null,"deletions":null}]}`
	if err := os.WriteFile(file, []byte(js), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	if got := *st.LastSync(); got != "2026-01-02T03:04:05.000Z" {
		t.Fatalf("lastSync = %s", got)
	}
	if c, _ := st.Get(shaA); c.Author == nil || *c.Author != "ana" || *c.Additions != 3 {
		t.Fatalf("read wrong: %+v", c)
	}
	if err := st.Save(); err != nil {
		t.Fatal(err)
	}
	written := readJSON(t, file)
	if !reflect.DeepEqual(keys(written), []string{"items", "meta"}) {
		t.Fatalf("top-level keys = %v", keys(written))
	}
	meta := written["meta"].(map[string]any)
	if meta["repo"] != "owner/repo" || meta["lastSync"] != "2026-01-02T03:04:05.000Z" {
		t.Fatalf("meta = %v", meta)
	}
	items := written["items"].([]any)
	first := items[0].(map[string]any)
	if first["sha"] != shaA || first["title"] != "first" || first["author"] != "ana" || first["at"] != "2026-01-02T03:00:00Z" || first["additions"] != float64(3) || first["deletions"] != float64(1) {
		t.Fatalf("item = %v", first)
	}
}

func TestEmptyStoreWritesEmptyLists(t *testing.T) {
	file := filepath.Join(t.TempDir(), "sub", "x.commits.json")
	st, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Save(); err != nil {
		t.Fatal(err)
	}
	written := readJSON(t, file)
	if _, ok := written["items"].([]any); !ok {
		t.Fatalf("items must be a list, got %v", written["items"])
	}
	if st.LastSync() != nil {
		t.Fatal("a new store has never been synced")
	}
}

func readJSON(t *testing.T, file string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func keys(m map[string]any) []string {
	out := []string{}
	for k := range m {
		out = append(out, k)
	}
	// sort for a stable comparison
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}
