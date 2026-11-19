package redmine

import (
	"context"
	"errors"
	"net/http"
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
		t.Fatalf("pickStale: %+v", got)
	}
}

// metaServer answers the single-issue metadata reads, with issue 3 as the one Redmine shows and 4 as deleted.
func metaServer(t *testing.T, fresh map[string]any) string {
	return fakeRedmine(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/issue_statuses.json":
			respond(w, 200, map[string]any{"issue_statuses": []any{map[string]any{"id": 1, "is_closed": false}, map[string]any{"id": 5, "is_closed": true}}})
		case r.URL.Path == "/custom_fields.json":
			respond(w, 403, nil)
		case r.URL.Path == "/issues/3.json":
			respond(w, 200, map[string]any{"issue": fresh})
		case r.URL.Path == "/issues/4.json":
			respond(w, 404, nil)
		default:
			respond(w, 404, nil)
		}
	})
}

func TestRefreshMetaAppliesTheBatchAndKeepsTheVersionsDueDate(t *testing.T) {
	key := metaServer(t, rawIssue(3, map[string]any{
		"status": map[string]any{"id": 5, "name": "Closed"}, "closed_on": "2026-01-09T00:00:00Z",
		"fixed_version": map[string]any{"id": 2, "name": "v1.0"}, "is_private": true,
		"relations": []any{map[string]any{"id": 9, "issue_id": 3, "issue_to_id": 4, "relation_type": "blocks"}},
	}))
	r, _ := New(key, Options{Key: keyOf("k")})
	st := newStore(t)
	due := "2027-03-01"
	st.Update(func(d *store.Data) {
		d.Issues["3"] = &model.Record{Kind: "issue", ID: "3", Key: "3", Title: "keep me", State: "OPEN",
			Milestone: &model.Milestone{Title: "v1.0", DueOn: &due}, ReadAt: "2026-01-01T00:00:00Z"}
		d.Issues["4"] = &model.Record{Kind: "issue", ID: "4", Key: "4", State: "OPEN"}
	})
	read, remaining, resetAt, err := r.RefreshMeta(context.Background(), st)
	if err != nil {
		t.Fatal(err)
	}
	if read != 1 || remaining != nil || resetAt != nil {
		t.Fatalf("read %d, remaining %v, reset %v", read, remaining, resetAt)
	}
	got := st.Data.Issues["3"]
	if got.State != "CLOSED" || got.ClosedAt == nil || got.IsPrivate == nil || !*got.IsPrivate {
		t.Fatalf("metadata applied: %+v", got)
	}
	if got.Title != "keep me" {
		t.Fatal("a field the batch does not read stays")
	}
	if got.Milestone == nil || got.Milestone.DueOn == nil || *got.Milestone.DueOn != due {
		t.Fatalf("the version's due date is kept across a batch read: %+v", got.Milestone)
	}
	if !model.HasRef(got.Blocking, "4") {
		t.Fatalf("relations applied: %v", got.Blocking)
	}
	if got.ReadAt == "2026-01-01T00:00:00Z" {
		t.Fatal("the read is stamped")
	}
	if st.Data.Issues["4"].ReadAt != "" {
		t.Fatal("an issue Redmine does not show is not changed")
	}
}

func TestRefreshOneIssueMergesTheFreshMetadataOntoTheRecord(t *testing.T) {
	key := metaServer(t, rawIssue(3, map[string]any{"subject": "not copied", "status": map[string]any{"id": 5, "name": "Closed"}}))
	r, _ := New(key, Options{Key: keyOf("k")})
	rec := &model.Record{Kind: "issue", ID: "3", Key: "3", Title: "mine", State: "OPEN"}
	got, err := r.Refresh(context.Background(), "issue", rec)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "CLOSED" || got.Title != "mine" {
		t.Fatalf("refreshed: state %s title %s", got.State, got.Title)
	}
	if rec.State != "OPEN" {
		t.Fatal("the record given is not changed: the copy is")
	}
	if _, err := r.Refresh(context.Background(), "pull", rec); !errors.Is(err, provider.ErrNotSupported) {
		t.Fatalf("a pull is not supported: %v", err)
	}
	gone := &model.Record{Kind: "issue", ID: "4", Key: "4"}
	if _, err := r.Refresh(context.Background(), "issue", gone); !errors.Is(err, provider.ErrNotFound) {
		t.Fatalf("an issue Redmine does not show is ErrNotFound: %v", err)
	}
	if _, err := r.Refresh(context.Background(), "issue", &model.Record{Key: "../x"}); errCode(err) != 400 {
		t.Fatalf("a key that is not an id is refused: %v", err)
	}
}

func TestRefreshMetaOfAnEmptyStoreReadsNothing(t *testing.T) {
	r, _ := New(repoKey, Options{Key: keyOf("k")})
	read, _, _, err := r.RefreshMeta(context.Background(), newStore(t))
	if err != nil || read != 0 {
		t.Fatalf("empty store: %d %v", read, err)
	}
}
