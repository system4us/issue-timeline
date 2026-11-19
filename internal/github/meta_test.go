package github

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.com/system4us/issue-timeline/internal/model"
	"gitlab.com/system4us/issue-timeline/internal/provider"
	"gitlab.com/system4us/issue-timeline/internal/store"
)

func TestTheLeastRecentlyReadIssuesComeFirst(t *testing.T) {
	issues := map[string]*model.Record{
		"1": {Kind: "issue", Key: "1", ReadAt: "2026-01-03"},
		"2": {Kind: "issue", Key: "2"},
		"3": {Kind: "issue", Key: "3", ReadAt: "2026-01-01"},
		"4": {Kind: "pull", Key: "4", ReadAt: "2025-01-01"},
	}
	got := pickStale(issues, 2)
	if len(got) != 2 || got[0].Key != "2" || got[1].Key != "3" {
		t.Fatalf("pickStale: %v", got)
	}
}

func TestTheBatchIsOneQueryWithAnAliasPerIssueAndTheQuota(t *testing.T) {
	q := githubBatchQuery([]int{3, 7})
	for _, want := range []string{"i0: issue(number: 3)", "i1: issue(number: 7)", "rateLimit { remaining resetAt cost }"} {
		if !strings.Contains(q, want) {
			t.Errorf("query lacks %q", want)
		}
	}
}

func TestABatchNodeBecomesTheMetadata(t *testing.T) {
	var n metaNode
	raw := `{"state":"CLOSED","stateReason":"NOT_PLANNED","locked":true,"issueType":{"name":"Bug"},"labels":{"nodes":[{"name":"x","color":"aaa"}]},
	"assignees":{"nodes":[{"login":"ana"}]},"milestone":null,"parent":{"number":2},"subIssues":{"nodes":[]},"blockedBy":{"nodes":[{"number":9}]},"blocking":{"nodes":[]}}`
	if err := json.Unmarshal([]byte(raw), &n); err != nil {
		t.Fatal(err)
	}
	m := metaFromNode(&n)
	if m.State != "CLOSED" || m.IssueType == nil || *m.IssueType != "Bug" || !m.Locked {
		t.Fatalf("meta: %+v", m)
	}
	if len(m.BlockedBy) != 1 || m.BlockedBy[0] != "9" || m.Parent == nil || *m.Parent != "2" {
		t.Fatalf("relations: %+v", m)
	}
}

func TestRefreshMetaAppliesTheBatchStampsItAndKeepsTheRest(t *testing.T) {
	calls := 0
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"data":{"repository":{"i0":{"number":3,"state":"OPEN","stateReason":null,"locked":true,"issueType":{"name":"Task"},
		  "labels":{"nodes":[]},"assignees":{"nodes":[]},"milestone":null,"parent":null,"subIssues":{"nodes":[]},"blockedBy":{"nodes":[]},"blocking":{"nodes":[]}}},
		  "rateLimit":{"remaining":4000,"resetAt":"2026-11-18T13:00:00Z","cost":1}}}`))
	})
	r, _ := testRepo(t, h)
	st, err := store.Open(filepath.Join(t.TempDir(), "s.json"))
	if err != nil {
		t.Fatal(err)
	}
	st.Update(func(d *store.Data) {
		d.Upsert("issues", &model.Record{Kind: "issue", Key: "3", Number: model.Ptr(3), Title: "keep me", ReadAt: "2026-01-01T00:00:00.000Z"})
	})
	read, remaining, resetAt, err := r.RefreshMeta(context.Background(), st)
	if err != nil {
		t.Fatal(err)
	}
	if read != 1 || remaining == nil || *remaining != 4000 || resetAt == nil {
		t.Fatalf("read=%d remaining=%v resetAt=%v", read, remaining, resetAt)
	}
	st.View(func(d *store.Data) {
		rec := d.Issues["3"]
		if rec.Locked == nil || !*rec.Locked || rec.IssueType == nil || *rec.IssueType != "Task" {
			t.Errorf("meta not applied: %+v", rec)
		}
		if rec.Title != "keep me" {
			t.Errorf("a field the batch does not read was lost: %q", rec.Title)
		}
		if rec.ReadAt == "2026-01-01T00:00:00.000Z" || rec.ReadAt == "" {
			t.Errorf("read not stamped: %q", rec.ReadAt)
		}
	})
	// an empty batch makes no call
	empty, _ := store.Open(filepath.Join(t.TempDir(), "e.json"))
	before := calls
	if n, _, _, err := r.RefreshMeta(context.Background(), empty); err != nil || n != 0 || calls != before {
		t.Fatalf("empty store: n=%d err=%v calls=%d", n, err, calls)
	}
}

func TestRefreshOfAPullReadsItsDraftAndReviewers(t *testing.T) {
	host := &fakeHost{routes: map[string]route{
		"GET /repos/o/r/issues/4":                    {body: `{"title":"PR","body":"","state":"open","labels":[],"assignees":[],"milestone":null,"updated_at":"x"}`},
		"GET /repos/o/r/pulls/4":                     {body: `{"state":"open","draft":true,"merged_at":null}`},
		"GET /repos/o/r/pulls/4/requested_reviewers": {body: `{"users":[{"login":"bea"}]}`},
	}}
	r, _ := testRepo(t, host)
	rec := &model.Record{Kind: "pull", Key: "4", Title: "old"}
	out, err := r.Refresh(context.Background(), "pull", rec)
	if err != nil {
		t.Fatal(err)
	}
	if out.IsDraft == nil || !*out.IsDraft || len(out.Reviewers) != 1 || out.Reviewers[0] != "bea" || out.State != "OPEN" || out.Title != "PR" {
		t.Fatalf("refreshed: %+v", out)
	}
	if rec.Title != "old" {
		t.Fatal("the record given was changed in place")
	}
}

func TestRefreshKeepsTheMilestoneDueDateOfTheSameVersion(t *testing.T) {
	host := &fakeHost{routes: map[string]route{
		"GET /repos/o/r/issues/5": {body: `{"title":"t","body":"","state":"open","labels":[],"assignees":[],"milestone":{"title":"v1","number":1,"due_on":null},"updated_at":"x"}`},
	}}
	r, _ := testRepo(t, host)
	due := "2026-12-01T00:00:00Z"
	rec := &model.Record{Kind: "issue", Key: "5", Milestone: &model.Milestone{Title: "v1", DueOn: &due}}
	out, err := r.Refresh(context.Background(), "issue", rec)
	if err != nil {
		t.Fatal(err)
	}
	if out.Milestone == nil || out.Milestone.DueOn == nil || *out.Milestone.DueOn != due {
		t.Fatalf("milestone: %+v", out.Milestone)
	}
}

func TestRefreshOfAnIssueTheHostNoLongerShowsIsNotFound(t *testing.T) {
	host := &fakeHost{routes: map[string]route{"GET /repos/o/r/issues/6": {status: 404, body: `{"message":"Not Found"}`}}}
	r, _ := testRepo(t, host)
	_, err := r.Refresh(context.Background(), "issue", &model.Record{Kind: "issue", Key: "6"})
	if !errors.Is(err, provider.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}
