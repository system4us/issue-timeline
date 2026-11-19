package github

import (
	"context"
	"net/http"
	"testing"
	"time"

	"gitlab.com/system4us/issue-timeline/internal/model"
)

func TestCIRunsReadTheWindowAndCategorize(t *testing.T) {
	var created string
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		created = r.URL.Query().Get("created")
		_, _ = w.Write([]byte(`{"workflow_runs":[{"id":77,"html_url":"https://x/77","name":"CI","path":".github/workflows/ci.yml","event":"push",
		  "actor":{"login":"ana"},"head_branch":"main","head_sha":"abc","status":"completed","conclusion":"failure","created_at":"2026-01-02T00:00:00Z"}]}`))
	})
	r, _ := testRepo(t, h)
	var seen model.CIRun
	r.o.Categorize = func(run model.CIRun) string { seen = run; return "other" }
	runs, err := r.CIRuns(context.Background(), time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if created != ">=2026-01-01" {
		t.Fatalf("created filter: %q", created)
	}
	if len(runs) != 1 || runs[0].ID != "77" || runs[0].Category != "other" || runs[0].Actor != "ana" || runs[0].Conclusion == nil || *runs[0].Conclusion != "failure" {
		t.Fatalf("runs: %+v", runs)
	}
	if seen.Name != "CI" || seen.Path != ".github/workflows/ci.yml" || seen.Event != "push" {
		t.Fatalf("the categorizer got the run's own fields: %+v", seen)
	}
}

func TestWorkflowChangesReadTheWorkflowFiles(t *testing.T) {
	var path, since string
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Query().Get("path")
		since = r.URL.Query().Get("since")
		_, _ = w.Write([]byte(`[{"sha":"s1","html_url":"https://x/s1","commit":{"message":"ci: pin\n\nmore","committer":{"date":"2026-01-02T00:00:00Z"}},"author":{"login":"ana"}}]`))
	})
	r, _ := testRepo(t, h)
	changes, err := r.WorkflowChanges(context.Background(), time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if path != ".github/workflows" || since != "2026-01-01T00:00:00.000Z" {
		t.Fatalf("query: path=%q since=%q", path, since)
	}
	if len(changes) != 1 || changes[0].Title != "ci: pin" || changes[0].Author == nil || *changes[0].Author != "ana" {
		t.Fatalf("changes: %+v", changes)
	}
}
