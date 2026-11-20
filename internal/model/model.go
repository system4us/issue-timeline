// Package model holds the records the stores keep and the API serves: issues and pull/merge requests of every host
// (GitHub, GitLab, Redmine, Jira), in one shape.
//
// The JSON must stay what the Node server wrote, so the stores on disk are read and written by both: the field names
// are the same, and a field this struct does not know is kept as it is (Extra) and written back.
package model

import (
	"bytes"
	"encoding/json"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
)

// Ref names another record: an issue or pull number ("12") or a tracker key ("PROJ-12"). It is text in Go, and it is
// written the way the stores keep it: a number when it is all digits, text otherwise. Either form is read.
// Comparing two Refs is comparing text, so 12 and "12" are the same record.
type Ref string

func (r Ref) MarshalJSON() ([]byte, error) {
	s := string(r)
	if isDigits(s) {
		return []byte(strings.TrimLeft(s, "0") + zeroIfEmpty(s)), nil
	}
	return json.Marshal(s)
}

func (r *Ref) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*r = Ref(s)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	*r = Ref(n.String())
	return nil
}

func isDigits(s string) bool {
	if s == "" || len(s) > 15 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func zeroIfEmpty(s string) string {
	if strings.TrimLeft(s, "0") == "" {
		return "0"
	}
	return ""
}

// RefOf is a Ref from an int (a host's number).
func RefOf(n int) Ref { return Ref(strconv.Itoa(n)) }

type Label struct {
	Name  string `json:"name"`
	Color string `json:"color"`
}

type Milestone struct {
	Title string  `json:"title"`
	DueOn *string `json:"dueOn"`
}

// Comment is a note on a record. Private is true for a note only some people may read (a Redmine private note, a
// GitLab internal note): it is shown only to someone the host lets read it.
type Comment struct {
	ID        string  `json:"id"`
	Author    *string `json:"author"`
	CreatedAt string  `json:"createdAt"`
	Body      string  `json:"body"`
	Private   bool    `json:"private,omitempty"`
}

// Event is one entry of a record's history: its type, when, who, and fields that depend on the type (label,
// assignee, reason, from, to, milestone, reviewer, source, sourceKind, external...), kept in Fields and written flat.
type Event struct {
	Type   string
	At     string
	Actor  *string
	Fields map[string]json.RawMessage
}

func (e Event) MarshalJSON() ([]byte, error) {
	m := make(map[string]any, len(e.Fields)+3)
	for k, v := range e.Fields {
		m[k] = v
	}
	m["type"] = e.Type
	m["at"] = e.At
	m["actor"] = e.Actor
	return json.Marshal(m)
}

func (e *Event) UnmarshalJSON(b []byte) error {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return err
	}
	*e = Event{}
	if v, ok := m["type"]; ok {
		_ = json.Unmarshal(v, &e.Type)
	}
	if v, ok := m["at"]; ok {
		_ = json.Unmarshal(v, &e.At)
	}
	if v, ok := m["actor"]; ok {
		_ = json.Unmarshal(v, &e.Actor)
	}
	delete(m, "type")
	delete(m, "at")
	delete(m, "actor")
	if len(m) > 0 {
		e.Fields = m
	}
	return nil
}

// Field is one event field, decoded into v; false when the event has none.
func (e Event) Field(name string, v any) bool {
	raw, ok := e.Fields[name]
	return ok && json.Unmarshal(raw, v) == nil
}

// SetField sets one event field.
func (e *Event) SetField(name string, v any) {
	if e.Fields == nil {
		e.Fields = map[string]json.RawMessage{}
	}
	b, _ := json.Marshal(v)
	e.Fields[name] = b
}

type PullCommit struct {
	Oid     string  `json:"oid"`
	Message string  `json:"message"`
	At      string  `json:"at"`
	Author  *string `json:"author"`
}

type Review struct {
	Author *string `json:"author"`
	State  string  `json:"state"`
	At     *string `json:"at"`
	Body   string  `json:"body"`
}

// IDName is an entry of a tracker's own lists (a Redmine tracker, priority, watcher).
type IDName struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// ParentEpic is GitLab's parent of an issue when it is not an issue of the project (an epic of the group).
type ParentEpic struct {
	IID   int     `json:"iid"`
	Title string  `json:"title"`
	URL   string  `json:"url"`
	Type  *string `json:"type"`
}

// CustomField is a tracker's custom field on a record (Redmine). Value is what the tracker gives: a string, a list of
// strings, or null.
type CustomField struct {
	ID       int             `json:"id"`
	Name     string          `json:"name"`
	Format   *string         `json:"format"`
	Multiple bool            `json:"multiple"`
	Value    json.RawMessage `json:"value"`
}

type Attachment struct {
	ID          int     `json:"id"`
	Filename    string  `json:"filename"`
	Filesize    *int64  `json:"filesize"`
	ContentType *string `json:"contentType"`
	Author      *string `json:"author"`
}

// Relation is a Redmine relation, by its own id.
type Relation struct {
	ID    int    `json:"id"`
	Type  string `json:"type"`
	From  int    `json:"from"`
	To    int    `json:"to"`
	Delay *int   `json:"delay"`
}

// Record is an issue (Kind "issue") or a pull/merge request (Kind "pull") of any host. The fields every record has
// come first and are always written; the others are written when they are set (nil or empty is left out), as the
// Node server did for the hosts that do not have them.
type Record struct {
	Kind          string     `json:"kind"`
	ID            string     `json:"id"`
	Key           string     `json:"key"`
	Number        *int       `json:"number"`
	Title         string     `json:"title"`
	Body          string     `json:"body"`
	URL           string     `json:"url"`
	Author        *string    `json:"author"`
	CreatedAt     string     `json:"createdAt"`
	UpdatedAt     string     `json:"updatedAt"`
	ClosedAt      *string    `json:"closedAt"`
	State         string     `json:"state"`
	Labels        []Label    `json:"labels"`
	Assignees     []string   `json:"assignees"`
	Milestone     *Milestone `json:"milestone"`
	CommentsCount int        `json:"commentsCount"`
	Comments      []Comment  `json:"comments"`
	Events        []Event    `json:"events"`
	// ReadAt is when the host last said what this record is (the sync, a refresh, an edit's read-back).
	ReadAt string `json:"readAt,omitempty"`

	// issues
	StateReason  *string     `json:"stateReason,omitempty"`
	IssueType    *string     `json:"issueType,omitempty"`
	DueDate      *string     `json:"dueDate,omitempty"`
	StartDate    *string     `json:"startDate,omitempty"`
	Parent       *Ref        `json:"parent,omitempty"`
	SubIssues    []Ref       `json:"subIssues,omitempty"`
	BlockedBy    []Ref       `json:"blockedBy,omitempty"`
	Blocking     []Ref       `json:"blocking,omitempty"`
	ParentEpic   *ParentEpic `json:"parentEpic,omitempty"`
	Locked       *bool       `json:"locked,omitempty"`
	Confidential *bool       `json:"confidential,omitempty"` // GitLab
	Weight       *float64    `json:"weight,omitempty"`       // GitLab
	IsPrivate    *bool       `json:"isPrivate,omitempty"`    // Redmine
	LabelEvents  int         `json:"labelEvents,omitempty"`

	// Redmine's own form fields (ids, so an edit can send them back)
	CustomFields   []CustomField `json:"customFields,omitempty"`
	Watchers       []IDName      `json:"watchers,omitempty"`
	Attachments    []Attachment  `json:"attachments,omitempty"`
	Tracker        *IDName       `json:"tracker,omitempty"`
	Priority       *IDName       `json:"priority,omitempty"`
	AssigneeID     *int          `json:"assigneeId,omitempty"`
	VersionID      *int          `json:"versionId,omitempty"`
	CategoryID     *int          `json:"categoryId,omitempty"`
	DoneRatio      *int          `json:"doneRatio,omitempty"`
	EstimatedHours *float64      `json:"estimatedHours,omitempty"`
	ParentID       *int          `json:"parentId,omitempty"`
	Relations      []Relation    `json:"relations,omitempty"`

	// pulls / merge requests
	IsDraft        *bool        `json:"isDraft,omitempty"`
	MergedAt       *string      `json:"mergedAt,omitempty"`
	MergedBy       *string      `json:"mergedBy,omitempty"`
	ReviewDecision *string      `json:"reviewDecision,omitempty"`
	Base           string       `json:"base,omitempty"`
	Head           string       `json:"head,omitempty"`
	Additions      *int         `json:"additions,omitempty"`
	Deletions      *int         `json:"deletions,omitempty"`
	ChangedFiles   *int         `json:"changedFiles,omitempty"`
	Closes         []Ref        `json:"closes,omitempty"`
	CommitsCount   *int         `json:"commitsCount,omitempty"`
	Commits        []PullCommit `json:"commits,omitempty"`
	Reviews        []Review     `json:"reviews,omitempty"`
	Reviewers      []string     `json:"reviewers,omitempty"`
	MergeCommitSha *string      `json:"mergeCommitSha,omitempty"`

	// Repo is the code repo a linked pull belongs to (a tracker's view of it); never stored.
	Repo string `json:"repo,omitempty"`

	// Extra keeps every field of the stored JSON this struct does not name, so nothing is lost on a rewrite.
	Extra map[string]json.RawMessage `json:"-"`
}

type recordAlias Record

var knownFields = sync.OnceValue(func() map[string]bool {
	out := map[string]bool{}
	t := reflect.TypeOf(recordAlias{})
	for i := 0; i < t.NumField(); i++ {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		if name != "" && name != "-" {
			out[name] = true
		}
	}
	return out
})

// kindDefaults are the fields every record of a kind carries, written with these values when the struct leaves them
// out (nil or empty): the Node server always wrote them, and the web app's types require them.
var kindDefaults = map[string]map[string]json.RawMessage{
	"issue": {"stateReason": json.RawMessage("null")},
	"pull": {
		"isDraft": json.RawMessage("false"), "mergedAt": json.RawMessage("null"), "mergedBy": json.RawMessage("null"),
		"reviewDecision": json.RawMessage("null"), "base": json.RawMessage(`""`), "head": json.RawMessage(`""`),
		"additions": json.RawMessage("0"), "deletions": json.RawMessage("0"), "changedFiles": json.RawMessage("0"),
		"closes": json.RawMessage("[]"), "commitsCount": json.RawMessage("0"),
	},
}

func (r Record) MarshalJSON() ([]byte, error) {
	b, err := json.Marshal(recordAlias(r))
	defaults := kindDefaults[r.Kind]
	if err != nil || (len(r.Extra) == 0 && len(defaults) == 0) {
		return b, err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	for k, v := range r.Extra {
		if _, taken := m[k]; !taken && !knownFields()[k] {
			m[k] = v
		}
	}
	for k, v := range defaults {
		if _, ok := m[k]; !ok {
			m[k] = v
		}
	}
	return json.Marshal(m)
}

func (r *Record) UnmarshalJSON(b []byte) error {
	var a recordAlias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return err
	}
	for k := range m {
		if knownFields()[k] {
			delete(m, k)
		}
	}
	if len(m) > 0 {
		a.Extra = m
	} else {
		a.Extra = nil
	}
	*r = Record(a)
	return nil
}

// Ptr is a pointer to v, for the optional fields.
func Ptr[T any](v T) *T { return &v }

// HasRef reports whether list names ref (compared as text).
func HasRef(list []Ref, ref Ref) bool {
	for _, x := range list {
		if x == ref {
			return true
		}
	}
	return false
}

// AddRef adds ref to list unless it is there; DropRef removes it.
func AddRef(list []Ref, ref Ref) []Ref {
	if HasRef(list, ref) {
		return list
	}
	return append(list, ref)
}

func DropRef(list []Ref, ref Ref) []Ref {
	out := list[:0:0]
	for _, x := range list {
		if x != ref {
			out = append(out, x)
		}
	}
	return out
}

// AddComment appends c to rec's comments: the comment a host confirmed it created, so the record shows it before the
// next sync reads it. Once only: a comment with the same id already there (a sync brought it) is not doubled. rec is a
// copy of a stored record whose slices may be shared, so the list is copied, never appended to in place.
func AddComment(rec *Record, c Comment) {
	for _, x := range rec.Comments {
		if x.ID == c.ID {
			return
		}
	}
	rec.Comments = append(slices.Clip(rec.Comments), c)
	if rec.CommentsCount < len(rec.Comments) {
		rec.CommentsCount = len(rec.Comments)
	} else {
		rec.CommentsCount++
	}
}
