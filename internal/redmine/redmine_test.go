package redmine

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"

	"gitlab.com/system4us/issue-timeline/internal/model"
	"gitlab.com/system4us/issue-timeline/internal/provider"
)

const repoKey = "redmine:" + testOrigin + "/backend"

func TestRepoKeyCarriesInstanceAndIdentifier(t *testing.T) {
	origin, ident, err := ParseRepo(repoKey)
	if err != nil || origin != testOrigin || ident != "backend" {
		t.Fatalf("ParseRepo: %q %q %v", origin, ident, err)
	}
	for _, in := range []string{testOrigin + "/projects/backend", testOrigin + "/backend/"} {
		if got, err := KeyFromURL(in); err != nil || got != repoKey {
			t.Fatalf("KeyFromURL(%s) = %q, %v", in, got, err)
		}
	}
	for _, bad := range []string{"redmine:ftp://x/y", "owner/repo"} {
		if _, _, err := ParseRepo(bad); err == nil {
			t.Fatalf("ParseRepo(%q) should fail", bad)
		}
	}
	if _, err := KeyFromURL(testOrigin + "/projects"); err == nil {
		t.Fatal("a URL with no project should fail")
	}
}

func TestJournalStatusMovesAreEventsAndNotesAreComments(t *testing.T) {
	r := normalize(t, rawIssue(7, map[string]any{
		"status": map[string]any{"id": 2, "name": "In progress"},
		"journals": []any{
			map[string]any{"id": 1, "user": map[string]any{"id": 3, "name": "Bea"}, "notes": "", "created_on": "2026-01-03T00:00:00Z",
				"details": []any{map[string]any{"property": "attr", "name": "status_id", "old_value": "1", "new_value": "5"}}},
			map[string]any{"id": 2, "user": map[string]any{"id": 3, "name": "Bea"}, "notes": "reopening", "created_on": "2026-01-04T00:00:00Z",
				"details": []any{map[string]any{"property": "attr", "name": "status_id", "old_value": "5", "new_value": "2"}}},
		},
	}))
	if r.State != "OPEN" || r.ClosedAt != nil {
		t.Fatalf("the last status is In progress, not closed: %s %v", r.State, r.ClosedAt)
	}
	var types []string
	for _, e := range r.Events {
		types = append(types, e.Type)
	}
	if strings.Join(types, ",") != "Closed,Reopened" {
		t.Fatalf("events: %v", types)
	}
	if len(r.Comments) != 1 || r.Comments[0].Body != "reopening" {
		t.Fatalf("comments: %+v", r.Comments)
	}
	if r.Key != "7" || r.URL != testOrigin+"/issues/7" {
		t.Fatalf("key %q url %q", r.Key, r.URL)
	}
}

func TestPrivateNotesBecomePrivateComments(t *testing.T) {
	r := normalize(t, rawIssue(8, map[string]any{
		"journals": []any{
			map[string]any{"id": 20, "notes": "for the team", "created_on": "2026-01-03T00:00:00Z", "private_notes": true},
			map[string]any{"id": 21, "notes": "for all", "created_on": "2026-01-03T00:00:00Z", "private_notes": false},
		},
	}))
	if len(r.Comments) != 2 {
		t.Fatalf("comments: %+v", r.Comments)
	}
	if !r.Comments[0].Private || r.Comments[1].Private {
		t.Fatalf("private notes must be Private, the others not: %+v", r.Comments)
	}
}

func TestRelationsParentTrackerAndVersion(t *testing.T) {
	r := normalize(t, rawIssue(3, map[string]any{
		"parent": map[string]any{"id": 1}, "children": []any{map[string]any{"id": 4}, map[string]any{"id": 6}},
		"fixed_version": map[string]any{"id": 2, "name": "v1.0"}, "assigned_to": map[string]any{"id": 8, "name": "Caro"},
		"relations": []any{
			map[string]any{"id": 1, "issue_id": 3, "issue_to_id": 4, "relation_type": "blocks"},  // 3 blocks 4
			map[string]any{"id": 2, "issue_id": 2, "issue_to_id": 3, "relation_type": "blocks"},  // 2 blocks 3
			map[string]any{"id": 3, "issue_id": 3, "issue_to_id": 5, "relation_type": "blocked"}, // 3 is blocked by 5
			map[string]any{"id": 4, "issue_id": 3, "issue_to_id": 6, "relation_type": "relates"}, // not a dependency
		},
	}))
	if len(r.Blocking) != 1 || r.Blocking[0] != "4" {
		t.Fatalf("blocking: %v", r.Blocking)
	}
	if len(r.BlockedBy) != 2 || !model.HasRef(r.BlockedBy, "2") || !model.HasRef(r.BlockedBy, "5") {
		t.Fatalf("blocked by: %v", r.BlockedBy)
	}
	if r.Parent == nil || *r.Parent != "1" {
		t.Fatalf("parent: %v", r.Parent)
	}
	if len(r.SubIssues) != 2 || r.SubIssues[0] != "4" || r.SubIssues[1] != "6" {
		t.Fatalf("subissues: %v", r.SubIssues)
	}
	if r.Milestone == nil || r.Milestone.Title != "v1.0" || r.Milestone.DueOn != nil {
		t.Fatalf("milestone: %+v", r.Milestone)
	}
	if len(r.Labels) != 1 || r.Labels[0].Name != "Bug" || r.Labels[0].Color != "b6c5d2" {
		t.Fatalf("labels: %+v", r.Labels)
	}
	if len(r.Assignees) != 1 || r.Assignees[0] != "Caro" {
		t.Fatalf("assignees: %v", r.Assignees)
	}
}

// syncServer answers a project of 101 issues (1000 down to 900) with the details of each.
func syncServer(t *testing.T, rec *recorder) string {
	return fakeRedmine(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		rec.add(r, body)
		if r.Header.Get("X-Redmine-API-Key") != "secret" {
			respond(w, 401, nil)
			return
		}
		switch r.URL.Path {
		case "/projects/backend.json":
			respond(w, 200, map[string]any{"project": map[string]any{"id": 42, "name": "Backend", "identifier": "backend"}})
		case "/issue_statuses.json":
			respond(w, 200, map[string]any{"issue_statuses": []any{map[string]any{"id": 1, "name": "New", "is_closed": false}, map[string]any{"id": 5, "name": "Closed", "is_closed": true}}})
		case "/issues.json":
			offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
			var issues []any
			if offset == 0 {
				for i := 0; i < 100; i++ {
					issues = append(issues, map[string]any{"id": 1000 - i, "updated_on": "2026-01-02T00:00:00Z"})
				}
			} else {
				issues = append(issues, map[string]any{"id": 900, "updated_on": "2026-01-02T00:00:00Z"})
			}
			respond(w, 200, map[string]any{"issues": issues, "total_count": 101, "offset": offset, "limit": 100})
		case "/custom_fields.json":
			respond(w, 404, nil)
		default:
			id, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/issues/"), ".json"))
			if strings.HasPrefix(r.URL.Path, "/issues/") && err == nil {
				respond(w, 200, map[string]any{"issue": rawIssue(id, map[string]any{"description": "issue " + strconv.Itoa(id)})})
				return
			}
			respond(w, 404, nil)
		}
	})
}

func TestSyncPagesReadsEveryIssueAndSendsTheKey(t *testing.T) {
	var rec recorder
	key := syncServer(t, &rec)
	r, err := New(key, Options{Key: keyOf("secret")})
	if err != nil {
		t.Fatal(err)
	}
	st := newStore(t)
	entry, err := r.Sync(context.Background(), st, provider.SyncOptions{Now: fixedNow("2026-01-03T00:00:00Z")})
	if err != nil {
		t.Fatal(err)
	}
	if !entry.Full || entry.Issues != 101 {
		t.Fatalf("entry: %+v", entry)
	}
	if len(st.Data.Issues) != 101 || st.Data.Issues["1000"] == nil || st.Data.Issues["900"] == nil {
		t.Fatalf("issues: %d", len(st.Data.Issues))
	}
	if len(st.Data.Pulls) != 0 {
		t.Fatal("a tracker has no pull requests")
	}
	calls := rec.all()
	for _, c := range calls {
		if c.Key != "secret" {
			t.Fatalf("call %s %s without the key", c.Method, c.Path)
		}
	}
	var lists []call
	for _, c := range calls {
		if c.Path == "/issues.json" {
			lists = append(lists, c)
		}
	}
	if lists[0].Query["project_id"] != "42" {
		t.Fatalf("the list filters by the numeric project id: %v", lists[0].Query)
	}
	if lists[0].Query["status_id"] != "*" {
		t.Fatal("closed issues are read too")
	}

	// A later sync asks only for what changed since the last one.
	rec.reset()
	if _, err := r.Sync(context.Background(), st, provider.SyncOptions{Now: fixedNow("2026-01-04T00:00:00Z")}); err != nil {
		t.Fatal(err)
	}
	for _, c := range rec.all() {
		if c.Path == "/issues.json" {
			if !strings.HasPrefix(c.Query["updated_on"], ">=2026-01-02") {
				t.Fatalf("the filter starts at the last sync, with a margin: %q", c.Query["updated_on"])
			}
			return
		}
	}
	t.Fatal("no list call in the incremental sync")
}

func TestSyncWithoutKeyTellsWhatIsMissing(t *testing.T) {
	r, err := New(repoKey, Options{Key: func(context.Context) (string, error) { return "", errors.New("no key stored") }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Sync(context.Background(), newStore(t), provider.SyncOptions{}); err == nil || !strings.Contains(err.Error(), "no key stored") {
		t.Fatalf("expected the missing key, got %v", err)
	}
	none, _ := New(repoKey, Options{})
	if _, err := none.Sync(context.Background(), newStore(t), provider.SyncOptions{}); err == nil {
		t.Fatal("a repo with no key source cannot sync")
	}
}

func TestProjectTheKeyCannotSeeIsMissingNotAnError(t *testing.T) {
	notFound := fakeRedmine(t, func(w http.ResponseWriter, r *http.Request) { respond(w, 404, nil) })
	r, _ := New(notFound, Options{Key: keyOf("k")})
	if err := r.Exists(context.Background()); !errors.Is(err, provider.ErrNotFound) {
		t.Fatalf("a missing project is ErrNotFound, got %v", err)
	}
	refused := fakeRedmine(t, func(w http.ResponseWriter, r *http.Request) { respond(w, 403, nil) })
	r, _ = New(refused, Options{Key: keyOf("k")})
	err := r.Exists(context.Background())
	if err == nil || errors.Is(err, provider.ErrNotFound) || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("a refused key is an error that says so, got %v", err)
	}
}

func TestDueAndStartDatesAndMilestoneDueDate(t *testing.T) {
	n := normCtx{closed: closedStatuses, origin: testOrigin, versionDue: map[int]*string{2: strPtr("2027-03-01")}}
	r := normalizeIssue(parseIssue(t, rawIssue(8, map[string]any{
		"due_date": "2027-02-01", "start_date": "2027-01-10", "fixed_version": map[string]any{"id": 2, "name": "v1.0"},
	})), n)
	if r.DueDate == nil || *r.DueDate != "2027-02-01" || r.StartDate == nil || *r.StartDate != "2027-01-10" {
		t.Fatalf("dates: %v %v", r.DueDate, r.StartDate)
	}
	if r.Milestone == nil || r.Milestone.DueOn == nil || *r.Milestone.DueOn != "2027-03-01" {
		t.Fatalf("milestone: %+v", r.Milestone)
	}
	none := normalize(t, rawIssue(9, nil))
	if none.DueDate != nil || none.StartDate != nil {
		t.Fatal("no dates when Redmine has none")
	}
}

func strPtr(s string) *string { return &s }

// editServer answers an issue (7) in project backend, with statuses, and records every change sent to it.
func editServer(t *testing.T, rec *recorder, current map[string]any, customFields bool) string {
	return fakeRedmine(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		rec.add(r, body)
		switch {
		case r.Method != http.MethodGet && r.URL.Path != "/issues.json":
			// a change: Redmine answers a PUT or a DELETE with no body, and a relation with its own object
			w.WriteHeader(204)
		case r.URL.Path == "/issue_statuses.json":
			respond(w, 200, map[string]any{"issue_statuses": []any{map[string]any{"id": 1, "is_closed": false}, map[string]any{"id": 2, "is_closed": false}, map[string]any{"id": 5, "is_closed": true}, map[string]any{"id": 6, "is_closed": true}}})
		case r.URL.Path == "/custom_fields.json" && !customFields:
			respond(w, 403, nil)
		case r.URL.Path == "/custom_fields.json":
			respond(w, 200, map[string]any{"custom_fields": []any{}})
		case r.URL.Path == "/issues/7.json":
			respond(w, 200, map[string]any{"issue": current})
		default:
			respond(w, 404, nil)
		}
	})
}

func TestEditIsSentWithRedminesNamesAndReadBack(t *testing.T) {
	var rec recorder
	key := editServer(t, &rec, rawIssue(7, map[string]any{"subject": "renamed in Redmine", "status": map[string]any{"id": 5, "name": "Closed"}, "closed_on": "2026-01-09T00:00:00Z"}), false)
	r, _ := New(key, Options{Key: keyOf("k")})
	old := &model.Record{Kind: "issue", Key: "7"}
	got, warnings, err := r.EditIssue(context.Background(), provider.Cred{Token: "k"}, old, provider.Changes{
		"title": []byte(`"renamed"`), "dueDate": []byte(`"2027-01-01"`), "state": []byte(`"CLOSED"`), "comment": []byte(`"done here"`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings: %v", warnings)
	}
	var put *call
	for i, c := range rec.all() {
		if c.Method == http.MethodPut {
			put = &rec.all()[i]
		}
	}
	if put == nil {
		t.Fatal("no PUT sent")
	}
	jsonEq(t, put.Body, `{"issue":{"subject":"renamed","due_date":"2027-01-01","status_id":5,"notes":"done here"}}`)
	if got.Title != "renamed in Redmine" {
		t.Fatalf("the record is what Redmine says after the write: %q", got.Title)
	}
	if got.State != "CLOSED" {
		t.Fatalf("state %s", got.State)
	}
}

func TestStatusForPicksTheLowestOpenOrClosedStatus(t *testing.T) {
	m := map[int]bool{1: false, 5: true}
	if id, err := statusFor(m, "OPEN"); err != nil || id != 1 {
		t.Fatalf("open: %d %v", id, err)
	}
	if id, err := statusFor(m, "CLOSED"); err != nil || id != 5 {
		t.Fatalf("closed: %d %v", id, err)
	}
	if _, err := statusFor(map[int]bool{1: false}, "CLOSED"); err == nil {
		t.Fatal("no closed status is an error")
	}
}

func TestCustomFieldsKeepValuesAndTheirFormats(t *testing.T) {
	cf := []any{
		map[string]any{"id": 1, "name": "Cliente", "value": "ACME"},
		map[string]any{"id": 2, "name": "Notas largas", "value": "línea uno\nlínea dos"},
		map[string]any{"id": 6, "name": "Urgente", "value": "1"},
		map[string]any{"id": 8, "name": "Etiquetas", "value": []any{"seguridad", "ux"}},
		map[string]any{"id": 9, "name": "Responsable externo", "value": nil},
	}
	defs := map[int]fieldDef{6: {Format: strPtr("bool")}, 8: {Format: strPtr("list"), Multiple: true}}
	n := normCtx{closed: closedStatuses, origin: testOrigin, defs: defs}
	r := normalizeIssue(parseIssue(t, rawIssue(11, map[string]any{"custom_fields": cf})), n)
	find := func(list []model.CustomField, id int) model.CustomField {
		for _, c := range list {
			if c.ID == id {
				return c
			}
		}
		t.Fatalf("no custom field %d", id)
		return model.CustomField{}
	}
	if u := find(r.CustomFields, 6); u.Format == nil || *u.Format != "bool" || u.Multiple || string(u.Value) != `"1"` {
		t.Fatalf("urgent: %+v", u)
	}
	if e := find(r.CustomFields, 8); e.Format == nil || *e.Format != "list" || !e.Multiple || string(e.Value) != `["seguridad","ux"]` {
		t.Fatalf("labels: %+v", e)
	}
	// Without the definitions the format is unknown, but a list value still says it takes several values.
	blind := normalize(t, rawIssue(12, map[string]any{"custom_fields": cf}))
	if find(blind.CustomFields, 1).Format != nil {
		t.Fatal("the format is unknown without the definition")
	}
	if !find(blind.CustomFields, 8).Multiple {
		t.Fatal("a list value takes several values")
	}
	if string(find(blind.CustomFields, 9).Value) != "null" {
		t.Fatalf("a null value stays null: %s", find(blind.CustomFields, 9).Value)
	}
}

func TestEditWritesCustomFieldsAsRedminesList(t *testing.T) {
	var rec recorder
	key := editServer(t, &rec, rawIssue(7, nil), false)
	r, _ := New(key, Options{Key: keyOf("k")})
	_, _, err := r.EditIssue(context.Background(), provider.Cred{Token: "k"}, &model.Record{Key: "7"}, provider.Changes{
		"customFields": []byte(`{"6":"0","8":["ux"]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	jsonEq(t, firstWrite(t, &rec).Body, `{"issue":{"custom_fields":[{"id":6,"value":"0"},{"id":8,"value":["ux"]}]}}`)
}

func TestFormFieldsGoAsRedminesNamesAndRelationsAreOwnRequests(t *testing.T) {
	var rec recorder
	key := editServer(t, &rec, rawIssue(7, map[string]any{"relations": []any{map[string]any{"id": 99, "issue_id": 7, "issue_to_id": 8, "relation_type": "relates"}}}), false)
	r, _ := New(key, Options{Key: keyOf("k")})
	ch := provider.Changes{
		"tracker": []byte(`2`), "priority": []byte(`4`), "assignee": []byte(`null`), "version": []byte(`3`),
		"doneRatio": []byte(`40`), "estimatedHours": []byte(`6.5`), "parent": []byte(`5`),
		"addRelations": []byte(`[{"type":"relates","to":8}]`), "removeRelations": []byte(`[42]`),
	}
	got, _, err := r.EditIssue(context.Background(), provider.Cred{Token: "k"}, &model.Record{Key: "7"}, ch)
	if err != nil {
		t.Fatal(err)
	}
	writes := []call{}
	for _, c := range rec.all() {
		if c.Method != http.MethodGet {
			writes = append(writes, c)
		}
	}
	if len(writes) != 3 {
		t.Fatalf("writes: %+v", writes)
	}
	if writes[0].Method != http.MethodPut || writes[0].Path != "/issues/7.json" {
		t.Fatalf("first write: %+v", writes[0])
	}
	jsonEq(t, writes[0].Body, `{"issue":{"tracker_id":2,"priority_id":4,"assigned_to_id":"","fixed_version_id":3,"done_ratio":40,"estimated_hours":6.5,"parent_issue_id":5}}`)
	jsonEq(t, writes[1].Body, `{"relation":{"issue_to_id":8,"relation_type":"relates"}}`)
	if writes[1].Path != "/issues/7/relations.json" || writes[2].Method != http.MethodDelete || writes[2].Path != "/relations/42.json" {
		t.Fatalf("relations: %+v %+v", writes[1], writes[2])
	}
	if len(got.Relations) != 1 || got.Relations[0].ID != 99 || got.Relations[0].To != 8 {
		t.Fatalf("relations read back: %+v", got.Relations)
	}

	// Clearing: Redmine wants an empty string for an assignee, a version or a category.
	rec.reset()
	if _, _, err := r.EditIssue(context.Background(), provider.Cred{Token: "k"}, &model.Record{Key: "7"}, provider.Changes{
		"assignee": []byte(`null`), "version": []byte(`null`), "category": []byte(`null`),
	}); err != nil {
		t.Fatal(err)
	}
	jsonEq(t, firstWrite(t, &rec).Body, `{"issue":{"assigned_to_id":"","fixed_version_id":"","category_id":""}}`)
}

func TestEditRefusesBadValuesBeforeAnythingIsSent(t *testing.T) {
	var rec recorder
	key := editServer(t, &rec, rawIssue(7, nil), false)
	r, _ := New(key, Options{Key: keyOf("k")})
	cases := map[string]struct {
		old *model.Record
		ch  provider.Changes
	}{
		"state":          {&model.Record{Key: "7"}, provider.Changes{"title": []byte(`"x"`), "state": []byte(`"MERGED"`)}},
		"relation type":  {&model.Record{Key: "7"}, provider.Changes{"title": []byte(`"x"`), "addRelations": []byte(`[{"type":"nope","to":8}]`)}},
		"relation to":    {&model.Record{Key: "7"}, provider.Changes{"addRelations": []byte(`[{"type":"relates","to":0}]`)}},
		"too many files": {&model.Record{Key: "7"}, provider.Changes{"attachments": []byte(`[` + strings.Repeat(`{"filename":"a","data":""},`, 5) + `{"filename":"a","data":""}]`)}},
		"file too big": {&model.Record{Key: "7"}, provider.Changes{"attachments": []byte(`[{"filename":"big.bin","data":"` +
			base64.StdEncoding.EncodeToString(make([]byte, 5*1024*1024+1)) + `"}]`)}},
		"id not digits": {&model.Record{Key: "7/../../admin"}, provider.Changes{"title": []byte(`"x"`)}},
	}
	for name, c := range cases {
		_, _, err := r.EditIssue(context.Background(), provider.Cred{Token: "k"}, c.old, c.ch)
		if errCode(err) != 400 {
			t.Fatalf("%s: want a 400, got %v", name, err)
		}
	}
	for _, c := range rec.all() {
		if c.Method != http.MethodGet {
			t.Fatalf("a refused edit sent %s %s", c.Method, c.Path)
		}
	}
}

func TestRefusedWriteIsAProviderError(t *testing.T) {
	key := fakeRedmine(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			respond(w, 403, nil)
			return
		}
		if r.URL.Path == "/issue_statuses.json" {
			respond(w, 200, map[string]any{"issue_statuses": []any{}})
			return
		}
		respond(w, 404, nil)
	})
	r, _ := New(key, Options{Key: keyOf("k")})
	_, _, err := r.EditIssue(context.Background(), provider.Cred{Token: "person"}, &model.Record{Key: "7"}, provider.Changes{"title": []byte(`"x"`)})
	if errCode(err) != 403 {
		t.Fatalf("a refused change is a 403, got %v", err)
	}
}

func TestRedmineRejectionKeepsItsMessages(t *testing.T) {
	key := fakeRedmine(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			respond(w, 422, map[string]any{"errors": []string{"Subject cannot be blank"}})
			return
		}
		respond(w, 200, map[string]any{"issue_statuses": []any{}})
	})
	r, _ := New(key, Options{Key: keyOf("k")})
	_, _, err := r.EditIssue(context.Background(), provider.Cred{Token: "k"}, &model.Record{Key: "7"}, provider.Changes{"title": []byte(`""`)})
	if errCode(err) != 422 || !strings.Contains(err.Error(), "Subject cannot be blank") {
		t.Fatalf("a rejection is a 422 with Redmine's message, got %v", err)
	}
}

func TestCreateWithoutAnIdFails(t *testing.T) {
	key := fakeRedmine(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/issues.json":
			respond(w, 201, map[string]any{})
		case r.URL.Path == "/projects/backend.json":
			respond(w, 200, map[string]any{"project": map[string]any{"id": 42}})
		default:
			respond(w, 404, nil)
		}
	})
	r, _ := New(key, Options{Key: keyOf("k")})
	_, err := r.CreateIssue(context.Background(), provider.Cred{Token: "k"}, provider.Changes{"title": []byte(`"New thing"`)})
	if err == nil || !strings.Contains(err.Error(), "did not say which issue") {
		t.Fatalf("a create with no id must fail, got %v", err)
	}
	if _, err := r.CreateIssue(context.Background(), provider.Cred{Token: "k"}, provider.Changes{"title": []byte(`"   "`)}); errCode(err) != 400 {
		t.Fatalf("a blank title is a 400, got %v", err)
	}
}

func TestCreateSendsTheFormAndReadsItBack(t *testing.T) {
	var rec recorder
	key := fakeRedmine(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		rec.add(r, body)
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/issues.json":
			respond(w, 201, map[string]any{"issue": map[string]any{"id": 77}})
		case r.URL.Path == "/projects/backend.json":
			respond(w, 200, map[string]any{"project": map[string]any{"id": 42}})
		case r.URL.Path == "/issue_statuses.json":
			respond(w, 200, map[string]any{"issue_statuses": []any{map[string]any{"id": 1, "is_closed": false}}})
		case r.URL.Path == "/issues/77.json":
			respond(w, 200, map[string]any{"issue": rawIssue(77, map[string]any{"subject": "New thing"})})
		case r.URL.Path == "/custom_fields.json":
			respond(w, 403, nil)
		default:
			respond(w, 404, nil)
		}
	})
	r, _ := New(key, Options{Key: keyOf("k")})
	got, err := r.CreateIssue(context.Background(), provider.Cred{Token: "k"}, provider.Changes{
		"title": []byte(`"  New thing "`), "tracker": []byte(`2`), "assignee": []byte(`0`), "isPrivate": []byte(`true`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Key != "77" || got.Title != "New thing" {
		t.Fatalf("created: %+v", got)
	}
	for _, c := range rec.all() {
		if c.Method == http.MethodPost {
			jsonEq(t, c.Body, `{"issue":{"project_id":42,"subject":"New thing","description":"","tracker_id":2,"is_private":true}}`)
		}
	}
}

func TestRolesReadThroughGroupsAndMembersPageByPage(t *testing.T) {
	others := make([]any, 0, 100)
	for i := 0; i < 100; i++ {
		others = append(others, map[string]any{"user": map[string]any{"id": 1000 + i}, "roles": []any{map[string]any{"name": "Reporter"}}})
	}
	// Each key belongs to its own user: user 7 is in the group Devs, user 8 in no group, user 1000 is only a member.
	key := fakeRedmine(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/users/current.json":
			switch r.Header.Get("X-Redmine-API-Key") {
			case "k7":
				respond(w, 200, map[string]any{"user": map[string]any{"id": 7, "groups": []any{map[string]any{"id": 50, "name": "Devs"}}}})
			case "k8":
				respond(w, 200, map[string]any{"user": map[string]any{"id": 8, "groups": []any{}}})
			default:
				respond(w, 200, map[string]any{"user": map[string]any{"id": 1000, "groups": []any{}}})
			}
		case "/projects/backend/memberships.json":
			offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
			list := others
			if offset > 0 {
				list = []any{map[string]any{"group": map[string]any{"id": 50}, "roles": []any{map[string]any{"name": "Developer"}}}}
			}
			respond(w, 200, map[string]any{"memberships": list, "total_count": 101})
		default:
			respond(w, 404, nil)
		}
	})
	r, _ := New(key, Options{Key: keyOf("k")})
	ctx := context.Background()
	if role, err := r.RoleOf(ctx, provider.Cred{Token: "k7"}, provider.User{ID: 7}); err != nil || role != provider.RoleEditor {
		t.Fatalf("a member through a group with Developer is an editor: %q %v", role, err)
	}
	if role, _ := r.RoleOf(ctx, provider.Cred{Token: "k8"}, provider.User{ID: 8}); role != provider.RoleNone {
		t.Fatalf("not a member has no role: %q", role)
	}
	if role, _ := r.RoleOf(ctx, provider.Cred{Token: "k1000"}, provider.User{ID: 1000}); role != provider.RoleViewer {
		t.Fatalf("a member with Reporter is a viewer: %q", role)
	}
	if role, _ := r.RoleOf(ctx, provider.Cred{Token: "k8"}, provider.User{ID: 8, Admin: true}); role != provider.RoleAdmin {
		t.Fatalf("an admin is admin: %q", role)
	}
}

func TestRoleOfTheKeyRefusedOrMissingIsNoRole(t *testing.T) {
	key := fakeRedmine(t, func(w http.ResponseWriter, r *http.Request) { respond(w, 403, nil) })
	r, _ := New(key, Options{Key: keyOf("k")})
	role, err := r.RoleOf(context.Background(), provider.Cred{Token: "p"}, provider.User{ID: 7})
	if err != nil || role != provider.RoleNone {
		t.Fatalf("a refused key has no role, without an error: %q %v", role, err)
	}
	if _, err := r.RoleOf(context.Background(), provider.Cred{}, provider.User{ID: 7}); err == nil {
		t.Fatal("a person with no key is not given the server's key")
	}
}

func TestWhoAmIUsesNameOrLoginAndRefusalIs401(t *testing.T) {
	key := fakeRedmine(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Redmine-API-Key") == "good" {
			respond(w, 200, map[string]any{"user": map[string]any{"id": 7, "login": "ana", "firstname": "Ana", "lastname": "Ruiz", "admin": false}})
			return
		}
		respond(w, 403, nil)
	})
	r, _ := New(key, Options{})
	u, err := r.WhoAmI(context.Background(), "good")
	if err != nil || u.Name != "Ana Ruiz" || u.Login != "ana" || u.ID != 7 {
		t.Fatalf("who: %+v %v", u, err)
	}
	if _, err := r.WhoAmI(context.Background(), "bad"); errCode(err) != 401 {
		t.Fatalf("a refused key is a 401, got %v", err)
	}
}

func TestVisibilityHidesIssuesRedmineDoesNotListAndPrivateNotesTheReadDoesNot(t *testing.T) {
	var rec recorder
	key := fakeRedmine(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		rec.add(r, body)
		switch {
		case r.URL.Path == "/projects/backend.json":
			respond(w, 200, map[string]any{"project": map[string]any{"id": 42}})
		case r.URL.Path == "/issues.json":
			respond(w, 200, map[string]any{"issues": []any{map[string]any{"id": 1}, map[string]any{"id": 2}}, "total_count": 2})
		case r.URL.Path == "/issues/1.json":
			// the person's read: the private note 10 is theirs, 11 is not
			respond(w, 200, map[string]any{"issue": map[string]any{"id": 1, "journals": []any{map[string]any{"id": 10}}}})
		default:
			respond(w, 404, nil)
		}
	})
	r, _ := New(key, Options{Key: keyOf("server")})
	recs := []*model.Record{
		{Kind: "issue", Key: "1", Comments: []model.Comment{{ID: "10", Private: true}, {ID: "11", Private: true}, {ID: "12"}}},
		{Kind: "issue", Key: "2", Comments: []model.Comment{{ID: "20"}}},
		{Kind: "issue", Key: "3", Comments: []model.Comment{{ID: "30", Private: true}}}, // not listed for the person
	}
	vis, err := r.VisibilityOf(context.Background(), provider.Cred{Token: "person"}, provider.User{ID: 7}, provider.RoleViewer, recs)
	if err != nil {
		t.Fatal(err)
	}
	if !vis.Issues["1"] || !vis.Issues["2"] || vis.Issues["3"] {
		t.Fatalf("issues the person may see: %v", vis.Issues)
	}
	if len(vis.HiddenComments) != 1 || !vis.HiddenComments["1"]["11"] || vis.HiddenComments["1"]["10"] || vis.HiddenComments["1"]["12"] {
		t.Fatalf("hidden notes: %v", vis.HiddenComments)
	}
	if _, ok := vis.HiddenComments["3"]; ok {
		t.Fatal("an issue the person cannot list is hidden whole, not checked")
	}
	for _, c := range rec.all() {
		if c.Key != "person" {
			t.Fatalf("a visibility read used another key: %s %s", c.Method, c.Path)
		}
		if c.Path == "/issues/3.json" {
			t.Fatal("an issue the person cannot list must not be read")
		}
	}
}

func TestVisibilityOfAnIssueTheReadRefusesHidesItsPrivateNotes(t *testing.T) {
	key := fakeRedmine(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/projects/backend.json":
			respond(w, 200, map[string]any{"project": map[string]any{"id": 42}})
		case "/issues.json":
			respond(w, 200, map[string]any{"issues": []any{map[string]any{"id": 1}}, "total_count": 1})
		default:
			respond(w, 403, nil)
		}
	})
	r, _ := New(key, Options{})
	recs := []*model.Record{{Kind: "issue", Key: "1", Comments: []model.Comment{{ID: "10", Private: true}}}}
	vis, err := r.VisibilityOf(context.Background(), provider.Cred{Token: "p"}, provider.User{ID: 7}, provider.RoleViewer, recs)
	if err != nil {
		t.Fatal(err)
	}
	if !vis.HiddenComments["1"]["10"] {
		t.Fatalf("a refused read hides its private notes: %v", vis.HiddenComments)
	}
}

func TestVisibilityNeedsThePersonsOwnKey(t *testing.T) {
	r, _ := New(repoKey, Options{Key: keyOf("server")})
	if _, err := r.VisibilityOf(context.Background(), provider.Cred{}, provider.User{ID: 7}, provider.RoleViewer, nil); err == nil {
		t.Fatal("the server's key must not stand in for a person's visibility")
	}
}

func TestFullSyncDropsTheIssuesNoLongerListedAndReadsADuplicateOnce(t *testing.T) {
	// The handler runs concurrently (three detail reads at a time): the state the test changes is guarded.
	var mu sync.Mutex
	ids := []int{3, 2, 1}
	detailRead := 0
	setIDs := func(v ...int) {
		mu.Lock()
		defer mu.Unlock()
		ids = v
		detailRead = 0
	}
	reads := func() int {
		mu.Lock()
		defer mu.Unlock()
		return detailRead
	}
	key := fakeRedmine(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/projects/backend.json":
			respond(w, 200, map[string]any{"project": map[string]any{"id": 42}})
		case r.URL.Path == "/issue_statuses.json":
			respond(w, 200, map[string]any{"issue_statuses": []any{map[string]any{"id": 1, "is_closed": false}}})
		case r.URL.Path == "/issues.json":
			mu.Lock()
			list := []any{}
			for _, id := range ids {
				list = append(list, map[string]any{"id": id})
			}
			n := len(ids)
			mu.Unlock()
			respond(w, 200, map[string]any{"issues": list, "total_count": n})
		case strings.HasPrefix(r.URL.Path, "/issues/"):
			id, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/issues/"), ".json"))
			mu.Lock()
			detailRead++
			mu.Unlock()
			respond(w, 200, map[string]any{"issue": rawIssue(id, nil)})
		default:
			respond(w, 404, nil)
		}
	})
	r, _ := New(key, Options{Key: keyOf("k")})
	st := newStore(t)
	if _, err := r.Sync(context.Background(), st, provider.SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	if len(st.Data.Issues) != 3 {
		t.Fatalf("first sync: %d issues", len(st.Data.Issues))
	}
	setIDs(3, 1, 1)
	if _, err := r.Sync(context.Background(), st, provider.SyncOptions{Full: true}); err != nil {
		t.Fatal(err)
	}
	if len(st.Data.Issues) != 2 || st.Data.Issues["2"] != nil {
		t.Fatalf("issue 2 was deleted in Redmine: %v", st.Data.Issues)
	}
	if n := reads(); n != 2 {
		t.Fatalf("issue 1 is read once: %d reads", n)
	}
}

func TestBusyRedmineIsRetried(t *testing.T) {
	tries := 0
	key := fakeRedmine(t, func(w http.ResponseWriter, r *http.Request) {
		tries++
		if tries == 1 {
			w.Header().Set("Retry-After", "0")
			respond(w, 429, nil)
			return
		}
		respond(w, 200, map[string]any{"user": map[string]any{"id": 7, "login": "ana"}})
	})
	r, _ := New(key, Options{})
	if _, err := r.WhoAmI(context.Background(), "k"); err != nil {
		t.Fatalf("the second try should read: %v", err)
	}
	if tries != 2 {
		t.Fatalf("tries: %d", tries)
	}
}

func TestEditOptionsListsTheFormAndKeepsEmptyLists(t *testing.T) {
	key := fakeRedmine(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/projects/backend.json":
			respond(w, 200, map[string]any{"project": map[string]any{"id": 42}})
		case "/trackers.json":
			respond(w, 200, map[string]any{"trackers": []any{map[string]any{"id": 1, "name": "Bug"}}})
		case "/projects/42/memberships.json":
			respond(w, 200, map[string]any{"memberships": []any{map[string]any{"user": map[string]any{"id": 7, "name": "Ana"}}, map[string]any{"group": map[string]any{"id": 50}}}})
		default:
			respond(w, 403, nil) // a list the key may not read comes back empty
		}
	})
	r, _ := New(key, Options{Key: keyOf("k")})
	opts, err := r.EditOptions(context.Background(), provider.Cred{Token: "k"})
	if err != nil {
		t.Fatal(err)
	}
	if len(opts.Trackers) != 1 || len(opts.Members) != 1 || len(opts.Priorities) != 0 || opts.Versions == nil {
		t.Fatalf("options: %+v", opts)
	}
	if len(opts.RelationTypes) != len(RelationTypes) {
		t.Fatalf("relation types: %v", opts.RelationTypes)
	}
}

func TestKeyOfTheRepoIsTheOneItWasMadeWith(t *testing.T) {
	r, err := New(repoKey, Options{})
	if err != nil || r.Key() != repoKey || r.Provider() != "redmine" || r.Origin() != testOrigin {
		t.Fatalf("repo: %v %v %v %v", r.Key(), r.Provider(), r.Origin(), err)
	}
}

// firstWrite is the first request that changes something (not a read).
func firstWrite(t *testing.T, rec *recorder) call {
	t.Helper()
	for _, c := range rec.all() {
		if c.Method != http.MethodGet {
			return c
		}
	}
	t.Fatal("no write was sent")
	return call{}
}
