package redmine

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"

	"gitlab.com/system4us/issue-timeline/internal/model"
	"gitlab.com/system4us/issue-timeline/internal/provider"
)

// Limits on the files of one edit: at most 5 files, 5 MB each (what the API accepted before).
const (
	maxAttachments    = 5
	maxAttachmentSize = 5 * 1024 * 1024
)

// editPlan is an edit as Redmine takes it, checked before anything is sent: a rejected field stops the edit with no
// change made.
type editPlan struct {
	body              map[string]any
	state             string // "OPEN" or "CLOSED" when the state changes, else ""
	addRelations      []relationAdd
	removeRelations   []int
	uploads           []upload
	removeAttachments []int
	addWatchers       []int
	removeWatchers    []int
	timeEntry         *timeEntry
}

type relationAdd struct {
	Type  string
	To    int
	Delay any // nil when there is no delay
}

type upload struct {
	Filename    string
	ContentType string
	Data        []byte
}

type timeEntry struct {
	Hours      float64
	ActivityID float64
	Comments   string
}

// planEdit reads the change the API passed (its names are the store's) into what Redmine takes. A value that is not
// allowed is a provider.Error with 400.
func planEdit(ch provider.Changes) (editPlan, error) {
	p := editPlan{body: map[string]any{}}
	// Taken as the form gives them: a null clears a date, a text or a number.
	for name, key := range map[string]string{
		"title": "subject", "body": "description", "dueDate": "due_date", "startDate": "start_date",
		"tracker": "tracker_id", "priority": "priority_id", "doneRatio": "done_ratio",
		"estimatedHours": "estimated_hours", "parent": "parent_issue_id",
	} {
		if ch.Has(name) {
			var v any
			ch.Get(name, &v)
			p.body[key] = v
		}
	}
	// A cleared assignee, version or category is an empty string: Redmine ignores a null.
	for name, key := range map[string]string{"assignee": "assigned_to_id", "version": "fixed_version_id", "category": "category_id"} {
		if ch.Has(name) {
			var v any
			ch.Get(name, &v)
			if v == nil {
				v = ""
			}
			p.body[key] = v
		}
	}
	if ch.Has("isPrivate") {
		var v any
		ch.Get("isPrivate", &v)
		b, _ := v.(bool)
		p.body["is_private"] = b
	}
	var comment string
	if ch.Get("comment", &comment) && comment != "" {
		p.body["notes"] = comment
		var priv any
		ch.Get("privateNote", &priv)
		if truthy(priv) {
			p.body["private_notes"] = true
		}
	}
	if err := planCustomFields(ch, p.body); err != nil {
		return p, err
	}
	if ch.Has("state") {
		var s any
		ch.Get("state", &s)
		str, _ := s.(string)
		if str != "OPEN" && str != "CLOSED" {
			return p, provider.Errorf(400, "state must be OPEN or CLOSED")
		}
		p.state = str
	}
	adds, err := listOf(ch, "addRelations")
	if err != nil {
		return p, err
	}
	for _, r := range adds {
		typ, _ := r["type"].(string)
		to, ok := toID(r["to"])
		if !slices.Contains(RelationTypes, typ) || !ok {
			return p, provider.Errorf(400, "a relation needs a type (%s) and the id of the other issue", strings.Join(RelationTypes, ", "))
		}
		var delay any
		if r["delay"] != nil {
			delay = r["delay"]
		}
		p.addRelations = append(p.addRelations, relationAdd{Type: typ, To: to, Delay: delay})
	}
	if p.removeRelations, err = idList(ch, "removeRelations"); err != nil {
		return p, err
	}
	if p.uploads, err = planAttachments(ch); err != nil {
		return p, err
	}
	if p.removeAttachments, err = idList(ch, "removeAttachments"); err != nil {
		return p, err
	}
	if p.addWatchers, err = idList(ch, "addWatchers"); err != nil {
		return p, err
	}
	if p.removeWatchers, err = idList(ch, "removeWatchers"); err != nil {
		return p, err
	}
	if ch.Has("timeEntry") && !ch.IsNull("timeEntry") {
		var t map[string]any
		if !ch.Get("timeEntry", &t) {
			return p, provider.Errorf(400, "timeEntry must be an object")
		}
		hours, _ := toNumber(t["hours"])
		activity, _ := toNumber(t["activityId"])
		comments, _ := t["comments"].(string)
		p.timeEntry = &timeEntry{Hours: hours, ActivityID: activity, Comments: comments}
	}
	return p, nil
}

// planCustomFields sends the custom fields as Redmine's list of {id, value}, in id order.
func planCustomFields(ch provider.Changes, body map[string]any) error {
	if !ch.Has("customFields") {
		return nil
	}
	var cf map[string]any
	ch.Get("customFields", &cf)
	if cf == nil {
		return nil
	}
	keys := make([]string, 0, len(cf))
	for k := range cf {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	list := []map[string]any{}
	for _, k := range keys {
		id, err := strconv.Atoi(k)
		if err != nil || id <= 0 {
			return provider.Errorf(400, "a custom field id is a number, not %q", k)
		}
		list = append(list, map[string]any{"id": id, "value": cf[k]})
	}
	body["custom_fields"] = list
	return nil
}

// planAttachments checks the files of an edit and decodes them: at most 5, each with a name and base64 data, at most
// 5 MB each.
func planAttachments(ch provider.Changes) ([]upload, error) {
	if !ch.Has("attachments") || ch.IsNull("attachments") {
		return nil, nil
	}
	var files []struct {
		Filename    *string `json:"filename"`
		Data        *string `json:"data"`
		ContentType string  `json:"contentType"`
	}
	if !ch.Get("attachments", &files) || len(files) > maxAttachments {
		return nil, provider.Errorf(400, "attachments must be a list of at most %d files", maxAttachments)
	}
	var out []upload
	for _, f := range files {
		if f.Filename == nil || f.Data == nil {
			return nil, provider.Errorf(400, "an attachment needs a filename and its data")
		}
		data, err := base64.StdEncoding.DecodeString(*f.Data)
		if err != nil {
			return nil, provider.Errorf(400, "%s is not base64 data", *f.Filename)
		}
		if len(data) > maxAttachmentSize {
			return nil, provider.Errorf(400, "%s is larger than 5 MB", *f.Filename)
		}
		ct := f.ContentType
		if ct == "" {
			ct = "application/octet-stream"
		}
		out = append(out, upload{Filename: *f.Filename, ContentType: ct, Data: data})
	}
	return out, nil
}

// listOf reads a list of objects from the change (absent or null is none).
func listOf(ch provider.Changes, name string) ([]map[string]any, error) {
	if !ch.Has(name) || ch.IsNull(name) {
		return nil, nil
	}
	var list []map[string]any
	if !ch.Get(name, &list) {
		return nil, provider.Errorf(400, "%s must be a list", name)
	}
	return list, nil
}

// idList reads a list of positive ids from the change (absent or null is none).
func idList(ch provider.Changes, name string) ([]int, error) {
	if !ch.Has(name) || ch.IsNull(name) {
		return nil, nil
	}
	var list []any
	if !ch.Get(name, &list) {
		return nil, provider.Errorf(400, "%s must be a list of ids", name)
	}
	var ids []int
	for _, v := range list {
		id, ok := toID(v)
		if !ok {
			return nil, provider.Errorf(400, "%s must be a list of ids", name)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// EditIssue changes the issue in Redmine, then reads it back: the record it returns is what Redmine says now, with the
// version's due date kept from old.
func (r *Repo) EditIssue(ctx context.Context, cred provider.Cred, old *model.Record, ch provider.Changes) (*model.Record, []string, error) {
	id, err := pathIssueID(old.Key)
	if err != nil {
		return nil, nil, err
	}
	plan, err := planEdit(ch)
	if err != nil {
		return nil, nil, err
	}
	c, err := r.clientFor(ctx, cred)
	if err != nil {
		return nil, nil, err
	}
	closed, err := statuses(ctx, c)
	if err != nil {
		return nil, nil, err
	}
	if plan.state != "" {
		statusID, err := statusFor(closed, plan.state)
		if err != nil {
			return nil, nil, err
		}
		plan.body["status_id"] = statusID
	}
	if err := applyEdit(ctx, c, id, plan); err != nil {
		return nil, nil, err
	}
	fresh, err := fetchIssue(ctx, c, id, fullInclude)
	if err != nil {
		return nil, nil, err
	}
	defs := customFieldDefs(ctx, c)
	rec := normalizeIssue(fresh, normCtx{closed: closed, origin: r.origin, defs: defs})
	rec.Milestone = withDueOf(old.Milestone, rec.Milestone)
	return rec, nil, nil
}

// applyEdit sends the plan to Redmine in order: the issue's own fields and status, relations, files, the files
// attached, watchers, then the time entry. Each change is its own request, as Redmine takes them.
func applyEdit(ctx context.Context, c client, id int, p editPlan) error {
	issue := "/issues/" + strconv.Itoa(id)
	if len(p.body) > 0 {
		if err := c.write(ctx, http.MethodPut, issue+".json", map[string]any{"issue": p.body}, nil); err != nil {
			return err
		}
	}
	for _, a := range p.addRelations {
		rel := map[string]any{"issue_to_id": a.To, "relation_type": a.Type}
		if a.Delay != nil {
			rel["delay"] = a.Delay
		}
		if err := c.write(ctx, http.MethodPost, issue+"/relations.json", map[string]any{"relation": rel}, nil); err != nil {
			return err
		}
	}
	for _, rid := range p.removeRelations {
		if err := c.del(ctx, "/relations/"+strconv.Itoa(rid)+".json"); err != nil {
			return err
		}
	}
	if len(p.uploads) > 0 {
		var list []map[string]any
		for _, u := range p.uploads {
			token, err := c.upload(ctx, u)
			if err != nil {
				return err
			}
			list = append(list, map[string]any{"token": token, "filename": u.Filename, "content_type": u.ContentType})
		}
		if err := c.write(ctx, http.MethodPut, issue+".json", map[string]any{"issue": map[string]any{"uploads": list}}, nil); err != nil {
			return err
		}
	}
	for _, aid := range p.removeAttachments {
		if err := c.del(ctx, "/attachments/"+strconv.Itoa(aid)+".json"); err != nil {
			return err
		}
	}
	for _, uid := range p.addWatchers {
		if err := c.write(ctx, http.MethodPost, issue+"/watchers.json", map[string]any{"user_id": uid}, nil); err != nil {
			return err
		}
	}
	for _, uid := range p.removeWatchers {
		if err := c.del(ctx, issue+"/watchers/"+strconv.Itoa(uid)+".json"); err != nil {
			return err
		}
	}
	if t := p.timeEntry; t != nil {
		entry := map[string]any{"issue_id": id, "hours": t.Hours, "activity_id": t.ActivityID, "comments": t.Comments}
		if err := c.write(ctx, http.MethodPost, "/time_entries.json", map[string]any{"time_entry": entry}, nil); err != nil {
			return err
		}
	}
	return nil
}

// upload sends one file and returns the token Redmine gives it, to attach with the issue's change.
func (c client) upload(ctx context.Context, u upload) (string, error) {
	var d struct {
		Upload struct {
			Token string `json:"token"`
		} `json:"upload"`
	}
	q := url.Values{"filename": {u.Filename}}
	if err := c.do(ctx, http.MethodPost, "/uploads.json", q, "application/octet-stream", u.Data, &d); err != nil {
		return "", fmt.Errorf("Redmine refused the file %s: %w", u.Filename, writeError(err))
	}
	if d.Upload.Token == "" {
		return "", fmt.Errorf("Redmine gave no token for the file %s", u.Filename)
	}
	return d.Upload.Token, nil
}

// CreateIssue creates an issue in the project (a subtask too, with its parent) and returns it as Redmine now has it.
// A Redmine answer without an issue id is an error: there is no record to keep.
func (r *Repo) CreateIssue(ctx context.Context, cred provider.Cred, ch provider.Changes) (*model.Record, error) {
	var title string
	if !ch.Get("title", &title) || strings.TrimSpace(title) == "" {
		return nil, provider.Errorf(400, "a title is needed")
	}
	c, err := r.clientFor(ctx, cred)
	if err != nil {
		return nil, err
	}
	proj, err := r.project(ctx, c)
	if err != nil {
		return nil, fmt.Errorf("reading project %s: %w", r.identifier, err)
	}
	var description string
	ch.Get("body", &description)
	body := map[string]any{"project_id": proj.ID, "subject": strings.TrimSpace(title), "description": description}
	// Only the fields given, and only when truthy (a 0 or an empty text is not a choice), as the form sends them.
	for name, key := range map[string]string{
		"tracker": "tracker_id", "priority": "priority_id", "assignee": "assigned_to_id", "version": "fixed_version_id",
		"category": "category_id", "parent": "parent_issue_id", "startDate": "start_date", "dueDate": "due_date",
	} {
		var v any
		if ch.Get(name, &v) && truthy(v) {
			body[key] = v
		}
	}
	var priv any
	if ch.Get("isPrivate", &priv) && truthy(priv) {
		body["is_private"] = true
	}
	var created struct {
		Issue *struct {
			ID int `json:"id"`
		} `json:"issue"`
	}
	if err := c.write(ctx, http.MethodPost, "/issues.json", map[string]any{"issue": body}, &created); err != nil {
		return nil, err
	}
	if created.Issue == nil || created.Issue.ID <= 0 {
		return nil, errors.New("Redmine did not say which issue it created")
	}
	id := created.Issue.ID
	fresh, err := fetchIssue(ctx, c, id, fullInclude)
	if err != nil {
		return nil, err
	}
	closed, err := statuses(ctx, c)
	if err != nil {
		return nil, err
	}
	return normalizeIssue(fresh, normCtx{closed: closed, origin: r.origin, defs: customFieldDefs(ctx, c)}), nil
}

// EditOptions is what Redmine's issue form offers for the project: trackers, priorities, members, versions, categories,
// time activities and custom fields. A list the key may not read comes back empty rather than failing the form.
func (r *Repo) EditOptions(ctx context.Context, cred provider.Cred) (provider.EditOptions, error) {
	c, err := r.clientFor(ctx, cred)
	if err != nil {
		return provider.EditOptions{}, err
	}
	proj, err := r.project(ctx, c)
	if err != nil {
		return provider.EditOptions{}, fmt.Errorf("reading project %s: %w", r.identifier, err)
	}
	var trackers struct {
		Trackers []rmRef `json:"trackers"`
	}
	var priorities struct {
		IssuePriorities []struct {
			ID        int    `json:"id"`
			Name      string `json:"name"`
			IsDefault bool   `json:"is_default"`
		} `json:"issue_priorities"`
	}
	var members struct {
		Memberships []struct {
			User *rmRef `json:"user"`
		} `json:"memberships"`
	}
	var versions struct {
		Versions []rmRef `json:"versions"`
	}
	var categories struct {
		IssueCategories []rmRef `json:"issue_categories"`
	}
	var activities struct {
		TimeEntryActivities []rmRef `json:"time_entry_activities"`
	}
	_ = c.do(ctx, http.MethodGet, "/trackers.json", nil, "", nil, &trackers)
	_ = c.do(ctx, http.MethodGet, "/enumerations/issue_priorities.json", nil, "", nil, &priorities)
	_ = c.do(ctx, http.MethodGet, "/projects/"+strconv.Itoa(proj.ID)+"/memberships.json", nil, "", nil, &members)
	_ = c.do(ctx, http.MethodGet, "/projects/"+strconv.Itoa(proj.ID)+"/versions.json", nil, "", nil, &versions)
	_ = c.do(ctx, http.MethodGet, "/projects/"+strconv.Itoa(proj.ID)+"/issue_categories.json", nil, "", nil, &categories)
	_ = c.do(ctx, http.MethodGet, "/enumerations/time_entry_activities.json", nil, "", nil, &activities)

	out := provider.EditOptions{
		Trackers: pairs(trackers.Trackers), Versions: pairs(versions.Versions), Categories: pairs(categories.IssueCategories),
		Activities: pairs(activities.TimeEntryActivities), RelationTypes: RelationTypes,
		Members: []map[string]any{}, Priorities: []map[string]any{}, CustomFields: []map[string]any{},
		Labels: []map[string]any{}, Milestones: []map[string]any{}, IssueTypes: []map[string]any{},
	}
	for _, p := range priorities.IssuePriorities {
		out.Priorities = append(out.Priorities, map[string]any{"id": p.ID, "name": p.Name, "isDefault": p.IsDefault})
	}
	for _, m := range members.Memberships {
		if m.User != nil {
			out.Members = append(out.Members, map[string]any{"id": m.User.ID, "name": m.User.Name})
		}
	}
	defs := customFieldDefs(ctx, c)
	for _, id := range sortedKeys(defs) {
		d := defs[id]
		var format any
		if d.Format != nil {
			format = *d.Format
		}
		out.CustomFields = append(out.CustomFields, map[string]any{"id": id, "name": d.Name, "format": format, "multiple": d.Multiple, "values": d.Values})
	}
	return out, nil
}

// pairs is a list of Redmine objects as the form takes them: {id, name}.
func pairs(list []rmRef) []map[string]any {
	out := []map[string]any{}
	for _, x := range list {
		out = append(out, map[string]any{"id": x.ID, "name": x.Name})
	}
	return out
}

// truthy is JavaScript's truth for a decoded JSON value: null, false, 0 and "" are not.
func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case float64:
		return x != 0
	case string:
		return x != ""
	}
	return true
}

// toNumber reads a number the way the form may send it: a JSON number, or text with one.
func toNumber(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return f, err == nil
	}
	return 0, false
}

// toID reads a positive whole number (an issue, relation, user or attachment id).
func toID(v any) (int, bool) {
	f, ok := toNumber(v)
	if !ok || f != math.Trunc(f) || f <= 0 || f > 1e15 {
		return 0, false
	}
	return int(f), true
}
