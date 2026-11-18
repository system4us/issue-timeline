package github

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"gitlab.com/system4us/issue-timeline/internal/model"
	"gitlab.com/system4us/issue-timeline/internal/provider"
)

const sha40 = "0123456789abcdef0123456789abcdef01234567"

func TestCommitFilesNormalizesTheInfoAndTheFiles(t *testing.T) {
	commit := `{"commit":{"message":"Fix the thing\n\nlong body","committer":{"date":"2026-01-01T00:00:00Z"},"author":{"name":"Ana","date":"2025-12-31T00:00:00Z"}},
	"author":{"login":"ana"},"files":[
	  {"filename":"a.go","status":"removed","additions":0,"deletions":3,"patch":"-x"},
	  {"filename":"b.go","previous_filename":"c.go","status":"renamed","additions":1,"deletions":1,"patch":null},
	  {"filename":"d.go","status":"copied","additions":2,"deletions":0,"patch":"+y"},
	  {"filename":"e.go","status":"something","additions":0,"deletions":0,"patch":null}]}`
	host := &fakeHost{routes: map[string]route{"GET /repos/o/r/commits/" + sha40: {body: commit}}}
	r, _ := testRepo(t, host)
	info, files, err := r.CommitFiles(context.Background(), sha40)
	if err != nil {
		t.Fatal(err)
	}
	if info.Title != "Fix the thing" || info.Author == nil || *info.Author != "ana" || info.At == nil || *info.At != "2026-01-01T00:00:00Z" {
		t.Fatalf("info: %+v", info)
	}
	want := []string{"deleted", "renamed", "added", "modified"}
	for i, f := range files {
		if f.Status != want[i] {
			t.Errorf("file %d status %q, want %q", i, f.Status, want[i])
		}
	}
	if files[1].OldPath == nil || *files[1].OldPath != "c.go" {
		t.Fatalf("renamed from: %+v", files[1])
	}
}

func TestCommitFilesRefusesWhatIsNotAFullSha(t *testing.T) {
	r, _ := testRepo(t, &fakeHost{})
	for _, bad := range []string{"abc", "../../user", strings.Repeat("G", 40)} {
		_, _, err := r.CommitFiles(context.Background(), bad)
		var pe *provider.Error
		if !errors.As(err, &pe) || pe.Status != 400 {
			t.Errorf("%q: want a 400, got %v", bad, err)
		}
	}
}

func TestCommitPageReadsOneCursorAtATime(t *testing.T) {
	var vars map[string]any
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Variables map[string]any `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		vars = body.Variables
		_, _ = w.Write([]byte(`{"data":{"repository":{"defaultBranchRef":{"target":{"history":{
		  "pageInfo":{"hasNextPage":true,"endCursor":"c2"},
		  "nodes":[{"oid":"` + sha40 + `","messageHeadline":"m","committedDate":"2026-01-01T00:00:00Z","author":{"name":"Ana Name","user":null},"additions":2,"deletions":1},
		           {"oid":"b","messageHeadline":"n","committedDate":"x","author":{"name":"B","user":{"login":"bo"}},"additions":null,"deletions":null}]}}}}}}`))
	})
	r, _ := testRepo(t, h)
	page, err := r.CommitPage(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if vars["cursor"] != nil {
		t.Fatalf("first page sends a null cursor: %v", vars["cursor"])
	}
	if page.Next != "c2" || len(page.Items) != 2 {
		t.Fatalf("page: %+v", page)
	}
	if page.Items[0].Author == nil || *page.Items[0].Author != "Ana Name" || page.Items[1].Author == nil || *page.Items[1].Author != "bo" {
		t.Fatalf("authors: %+v", page.Items)
	}
}

func TestMergeMethodClassifiesTheMergeCommit(t *testing.T) {
	var got struct {
		parents  int
		title    string
		prTitle  string
		number   int
		prTitles []string
	}
	host := &fakeHost{routes: map[string]route{
		"GET /repos/o/r/pulls/12":         {body: `{"merge_commit_sha":"` + sha40 + `"}`},
		"GET /repos/o/r/commits/" + sha40: {body: `{"commit":{"message":"Fix (#12)\n\nbody"},"parents":[{},{}]}`},
	}}
	r, _ := testRepo(t, host)
	r.o.ClassifyMerge = func(parents int, title, prTitle string, number int, prCommitTitles []string) string {
		got.parents, got.title, got.prTitle, got.number, got.prTitles = parents, title, prTitle, number, prCommitTitles
		return "merge commit"
	}
	rec := &model.Record{Kind: "pull", Key: "12", Title: "The PR", Commits: []model.PullCommit{{Message: "one"}}}
	m, err := r.MergeMethod(context.Background(), rec)
	if err != nil {
		t.Fatal(err)
	}
	if m != "merge commit" || got.parents != 2 || got.title != "Fix (#12)" || got.prTitle != "The PR" || got.number != 12 || len(got.prTitles) != 1 || got.prTitles[0] != "one" {
		t.Fatalf("method %q, classifier got %+v", m, got)
	}
}

func TestMergeMethodIsEmptyWithoutAMergeCommit(t *testing.T) {
	host := &fakeHost{routes: map[string]route{"GET /repos/o/r/pulls/12": {body: `{"merge_commit_sha":null}`}}}
	r, _ := testRepo(t, host)
	m, err := r.MergeMethod(context.Background(), &model.Record{Key: "12"})
	if err != nil || m != "" {
		t.Fatalf("got %q, %v", m, err)
	}
}
