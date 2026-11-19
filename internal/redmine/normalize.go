package redmine

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"

	"gitlab.com/system4us/issue-timeline/internal/model"
)

// rmRef is a Redmine object by its id and name (a tracker, a user, a version, a status, a group).
type rmRef struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// rmIssue is one issue as Redmine's REST API gives it, with the includes the reads ask for.
type rmIssue struct {
	ID             int             `json:"id"`
	Subject        string          `json:"subject"`
	Description    string          `json:"description"`
	Status         *rmRef          `json:"status"`
	Tracker        *rmRef          `json:"tracker"`
	Priority       *rmRef          `json:"priority"`
	Author         *rmRef          `json:"author"`
	AssignedTo     *rmRef          `json:"assigned_to"`
	Category       *rmRef          `json:"category"`
	FixedVersion   *rmRef          `json:"fixed_version"`
	Parent         *rmRef          `json:"parent"`
	CreatedOn      string          `json:"created_on"`
	UpdatedOn      string          `json:"updated_on"`
	ClosedOn       *string         `json:"closed_on"`
	DueDate        *string         `json:"due_date"`
	StartDate      *string         `json:"start_date"`
	IsPrivate      bool            `json:"is_private"`
	DoneRatio      *int            `json:"done_ratio"`
	EstimatedHours *float64        `json:"estimated_hours"`
	Journals       []rmJournal     `json:"journals"`
	Relations      []rmRelation    `json:"relations"`
	Children       []rmRef         `json:"children"`
	Watchers       []rmRef         `json:"watchers"`
	Attachments    []rmAttachment  `json:"attachments"`
	CustomFields   []rmCustomField `json:"custom_fields"`
}

type rmJournal struct {
	ID           int        `json:"id"`
	User         *rmRef     `json:"user"`
	Notes        string     `json:"notes"`
	CreatedOn    string     `json:"created_on"`
	PrivateNotes bool       `json:"private_notes"`
	Details      []rmDetail `json:"details"`
}

type rmDetail struct {
	Property string          `json:"property"`
	Name     string          `json:"name"`
	OldValue json.RawMessage `json:"old_value"`
	NewValue json.RawMessage `json:"new_value"`
}

type rmRelation struct {
	ID           int    `json:"id"`
	IssueID      int    `json:"issue_id"`
	IssueToID    int    `json:"issue_to_id"`
	RelationType string `json:"relation_type"`
	Delay        *int   `json:"delay"`
}

type rmAttachment struct {
	ID          int     `json:"id"`
	Filename    string  `json:"filename"`
	Filesize    *int64  `json:"filesize"`
	ContentType *string `json:"content_type"`
	Author      *rmRef  `json:"author"`
}

type rmCustomField struct {
	ID    int             `json:"id"`
	Name  string          `json:"name"`
	Value json.RawMessage `json:"value"`
}

// normCtx is what a normalization needs besides the issue: the closed statuses, the site's origin (the issue's link),
// the due dates of the versions read so far, and the custom field definitions.
type normCtx struct {
	closed     map[int]bool
	origin     string
	versionDue map[int]*string
	defs       map[int]fieldDef
}

// normalizeIssue is the store's record for one issue. Nothing else reaches the store: the journals keep their notes
// (a note marked private_notes keeps Private, so the visibility check can hide it), their status changes become events,
// and the relations become blocking and blocked-by.
func normalizeIssue(raw rmIssue, n normCtx) *model.Record {
	id := raw.ID
	comments := []model.Comment{}
	var events []model.Event
	for _, j := range raw.Journals {
		if j.Notes != "" {
			comments = append(comments, model.Comment{
				ID: strconv.Itoa(j.ID), Author: userName(j.User), CreatedAt: j.CreatedOn, Body: j.Notes, Private: j.PrivateNotes,
			})
		}
		for _, d := range j.Details {
			if d.Property == "attr" {
				events = append(events, journalEvents(j, d, n.closed)...)
			}
		}
	}
	sort.SliceStable(events, func(a, b int) bool { return parseTime(events[a].At).Before(parseTime(events[b].At)) })

	var blockedBy, blocking []model.Ref
	var relations []model.Relation
	for _, r := range raw.Relations {
		other := r.IssueToID
		if r.IssueID != id {
			other = r.IssueID
		}
		ref := model.RefOf(other)
		switch r.RelationType {
		case "blocks":
			if r.IssueID == id {
				blocking = model.AddRef(blocking, ref)
			} else {
				blockedBy = model.AddRef(blockedBy, ref)
			}
		case "blocked":
			if r.IssueID == id {
				blockedBy = model.AddRef(blockedBy, ref)
			} else {
				blocking = model.AddRef(blocking, ref)
			}
		}
		relations = append(relations, model.Relation{ID: r.ID, Type: r.RelationType, From: r.IssueID, To: r.IssueToID, Delay: r.Delay})
	}

	closedNow := raw.Status != nil && n.closed[raw.Status.ID]
	state := "OPEN"
	var closedAt *string
	if closedNow {
		state = "CLOSED"
		closedAt = raw.ClosedOn
	}

	labels := []model.Label{}
	if raw.Tracker != nil {
		labels = append(labels, model.Label{Name: raw.Tracker.Name, Color: "b6c5d2"})
	}
	assignees := []string{}
	if raw.AssignedTo != nil {
		assignees = append(assignees, raw.AssignedTo.Name)
	}
	var milestone *model.Milestone
	if raw.FixedVersion != nil {
		milestone = &model.Milestone{Title: raw.FixedVersion.Name, DueOn: n.versionDue[raw.FixedVersion.ID]}
	}

	watchers := []model.IDName{}
	for _, w := range raw.Watchers {
		watchers = append(watchers, model.IDName{ID: w.ID, Name: w.Name})
	}
	attachments := []model.Attachment{}
	for _, a := range raw.Attachments {
		attachments = append(attachments, model.Attachment{ID: a.ID, Filename: a.Filename, Filesize: a.Filesize, ContentType: a.ContentType, Author: userName(a.Author)})
	}
	// Custom fields keep their values as Redmine gives them. The format and the multiple flag come from the definition
	// when the key may read it; otherwise a list value still says it takes several.
	var customFields []model.CustomField
	for _, c := range raw.CustomFields {
		cf := model.CustomField{ID: c.ID, Name: c.Name, Value: nullIfEmpty(c.Value)}
		if def, ok := n.defs[c.ID]; ok {
			cf.Format = def.Format
			cf.Multiple = def.Multiple
		} else {
			cf.Multiple = isJSONArray(c.Value)
		}
		customFields = append(customFields, cf)
	}

	var subIssues []model.Ref
	for _, c := range raw.Children {
		subIssues = append(subIssues, model.RefOf(c.ID))
	}
	var parent *model.Ref
	var parentID *int
	if raw.Parent != nil {
		p := model.RefOf(raw.Parent.ID)
		parent = &p
		parentID = model.Ptr(raw.Parent.ID)
	}
	var tracker, priority *model.IDName
	if raw.Tracker != nil {
		tracker = &model.IDName{ID: raw.Tracker.ID, Name: raw.Tracker.Name}
	}
	if raw.Priority != nil {
		priority = &model.IDName{ID: raw.Priority.ID, Name: raw.Priority.Name}
	}
	var assigneeID, versionID, categoryID *int
	if raw.AssignedTo != nil {
		assigneeID = model.Ptr(raw.AssignedTo.ID)
	}
	if raw.FixedVersion != nil {
		versionID = model.Ptr(raw.FixedVersion.ID)
	}
	if raw.Category != nil {
		categoryID = model.Ptr(raw.Category.ID)
	}
	doneRatio := 0
	if raw.DoneRatio != nil {
		doneRatio = *raw.DoneRatio
	}

	return &model.Record{
		Kind:           "issue",
		ID:             strconv.Itoa(id),
		Key:            strconv.Itoa(id),
		Number:         model.Ptr(id),
		Title:          raw.Subject,
		Body:           raw.Description,
		URL:            n.origin + "/issues/" + strconv.Itoa(id),
		Author:         userName(raw.Author),
		CreatedAt:      raw.CreatedOn,
		UpdatedAt:      raw.UpdatedOn,
		ClosedAt:       closedAt,
		State:          state,
		Labels:         labels,
		Assignees:      assignees,
		Milestone:      milestone,
		CommentsCount:  len(comments),
		Comments:       comments,
		Events:         nonNilEvents(events),
		IsPrivate:      model.Ptr(raw.IsPrivate),
		Watchers:       watchers,
		Attachments:    attachments,
		DueDate:        raw.DueDate,
		StartDate:      raw.StartDate,
		Parent:         parent,
		SubIssues:      subIssues,
		BlockedBy:      blockedBy,
		Blocking:       blocking,
		Tracker:        tracker,
		Priority:       priority,
		AssigneeID:     assigneeID,
		VersionID:      versionID,
		CategoryID:     categoryID,
		DoneRatio:      model.Ptr(doneRatio),
		EstimatedHours: raw.EstimatedHours,
		ParentID:       parentID,
		Relations:      relations,
		CustomFields:   customFields,
	}
}

// journalEvents is the history event a journal detail makes, if any: a status moving to closed or back, an assignee or
// a version changed. The journal names the id only, so the assignee and the milestone are left null.
func journalEvents(j rmJournal, d rmDetail, closed map[int]bool) []model.Event {
	base := model.Event{At: j.CreatedOn, Actor: userName(j.User)}
	with := func(typ, field string, v any) model.Event {
		e := base
		e.Type = typ
		if field != "" {
			e.SetField(field, v)
		}
		return e
	}
	switch d.Name {
	case "status_id":
		from, to := closed[jsonInt(d.OldValue)], closed[jsonInt(d.NewValue)]
		switch {
		case !from && to:
			return []model.Event{with("Closed", "reason", nil)}
		case from && !to:
			return []model.Event{with("Reopened", "", nil)}
		}
	case "assigned_to_id":
		return []model.Event{with("Assigned", "assignee", nil)}
	case "fixed_version_id":
		return []model.Event{with("Milestoned", "milestone", nil)}
	}
	return nil
}

func nonNilEvents(e []model.Event) []model.Event {
	if e == nil {
		return []model.Event{}
	}
	return e
}

// userName is a user's name for an author or actor field, null when there is no user.
func userName(u *rmRef) *string {
	if u == nil {
		return nil
	}
	name := u.Name
	return &name
}

// jsonInt reads a number that Redmine sends as text (a status id in a journal). Anything else is 0, which no status has.
func jsonInt(raw json.RawMessage) int {
	s := strings.Trim(strings.TrimSpace(string(raw)), `"`)
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}

func parseTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}

func nullIfEmpty(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage("null")
	}
	return raw
}

func isJSONArray(raw json.RawMessage) bool {
	return strings.HasPrefix(strings.TrimSpace(string(raw)), "[")
}
