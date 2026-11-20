package gitlab

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"

	"gitlab.com/system4us/issue-timeline/internal/model"
)

// The shapes of GitLab's API that the sync and the reads use. Only the fields read are named.

type glUser struct {
	ID       int    `json:"id"`
	Username string `json:"username"`
	Name     string `json:"name"`
	IsAdmin  bool   `json:"is_admin"`
}

type glMilestone struct {
	Title   string  `json:"title"`
	DueDate *string `json:"due_date"`
}

type glIssue struct {
	ID             int               `json:"id"`
	IID            int               `json:"iid"`
	Title          string            `json:"title"`
	Description    *string           `json:"description"`
	WebURL         string            `json:"web_url"`
	Author         *glUser           `json:"author"`
	CreatedAt      string            `json:"created_at"`
	UpdatedAt      string            `json:"updated_at"`
	ClosedAt       *string           `json:"closed_at"`
	DueDate        *string           `json:"due_date"`
	Labels         []json.RawMessage `json:"labels"`
	Assignees      []glUser          `json:"assignees"`
	Assignee       *glUser           `json:"assignee"`
	Milestone      *glMilestone      `json:"milestone"`
	UserNotesCount *int              `json:"user_notes_count"`
	State          string            `json:"state"`
	Confidential   bool              `json:"confidential"`
	Weight         *float64          `json:"weight"`
}

type glMergeRequest struct {
	ID              int               `json:"id"`
	IID             int               `json:"iid"`
	Title           string            `json:"title"`
	Description     *string           `json:"description"`
	WebURL          string            `json:"web_url"`
	Author          *glUser           `json:"author"`
	CreatedAt       string            `json:"created_at"`
	UpdatedAt       string            `json:"updated_at"`
	ClosedAt        *string           `json:"closed_at"`
	Labels          []json.RawMessage `json:"labels"`
	Assignees       []glUser          `json:"assignees"`
	Assignee        *glUser           `json:"assignee"`
	Reviewers       []glUser          `json:"reviewers"`
	Milestone       *glMilestone      `json:"milestone"`
	UserNotesCount  *int              `json:"user_notes_count"`
	State           string            `json:"state"`
	Draft           *bool             `json:"draft"`
	WorkInProgress  *bool             `json:"work_in_progress"`
	MergedAt        *string           `json:"merged_at"`
	MergeUser       *glUser           `json:"merge_user"`
	MergedBy        *glUser           `json:"merged_by"`
	TargetBranch    string            `json:"target_branch"`
	SourceBranch    string            `json:"source_branch"`
	MergeCommitSHA  *string           `json:"merge_commit_sha"`
	SquashCommitSHA *string           `json:"squash_commit_sha"`
}

type glNote struct {
	ID           int64   `json:"id"`
	System       bool    `json:"system"`
	Body         string  `json:"body"`
	Author       *glUser `json:"author"`
	CreatedAt    string  `json:"created_at"`
	Internal     bool    `json:"internal"`
	Confidential bool    `json:"confidential"`
}

type glStateEvent struct {
	State     string  `json:"state"`
	CreatedAt string  `json:"created_at"`
	User      *glUser `json:"user"`
}

type glLabelEvent struct {
	Action    string  `json:"action"`
	CreatedAt string  `json:"created_at"`
	User      *glUser `json:"user"`
	Label     *struct {
		Name  string `json:"name"`
		Title string `json:"title"`
	} `json:"label"`
}

type glMilestoneEvent struct {
	Action    string  `json:"action"`
	CreatedAt string  `json:"created_at"`
	User      *glUser `json:"user"`
	Milestone *struct {
		Title string `json:"title"`
		Name  string `json:"name"`
	} `json:"milestone"`
}

type glLink struct {
	IID      int    `json:"iid"`
	LinkType string `json:"link_type"`
	LinkID   int    `json:"issue_link_id"`
}

type glCommit struct {
	ID            string  `json:"id"`
	Title         *string `json:"title"`
	Message       *string `json:"message"`
	AuthorName    *string `json:"author_name"`
	Author        *glUser `json:"author"`
	CommittedDate *string `json:"committed_date"`
	CreatedAt     *string `json:"created_at"`
	WebURL        *string `json:"web_url"`
}

// firstOf is the first value that is not nil, as JavaScript's ?? reads.
func firstOf(vals ...*string) *string {
	for _, v := range vals {
		if v != nil {
			return v
		}
	}
	return nil
}

// userName is the username of a user, or else the name; nil when there is no user.
func userName(u *glUser) *string {
	if u == nil {
		return nil
	}
	if u.Username != "" {
		s := u.Username
		return &s
	}
	if u.Name != "" {
		s := u.Name
		return &s
	}
	return nil
}

// userNames is the usernames of a list of users, leaving out the ones with no name.
func userNames(users []glUser) []string {
	out := []string{}
	for i := range users {
		if n := userName(&users[i]); n != nil && *n != "" {
			out = append(out, *n)
		}
	}
	return out
}

// labelName is a label as the API gives it: a name, or an object with a name (or a title).
func labelName(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var o struct {
		Name  string `json:"name"`
		Title string `json:"title"`
	}
	if json.Unmarshal(raw, &o) == nil {
		if o.Name != "" {
			return o.Name
		}
		return o.Title
	}
	return ""
}

// labelColors maps a project's label names to their colors (without the "#").
func labelColors(labels []glLabel) map[string]string {
	out := make(map[string]string, len(labels))
	for _, l := range labels {
		c := defaultColor
		if l.Color != nil {
			c = *l.Color
		}
		out[l.Name] = c
	}
	return out
}

// stripHash removes the "#" of a color.
func stripHash(c string) string { return strings.TrimPrefix(c, "#") }

// normalizeLabels keeps the labels with the color the project gave them (or the default).
func normalizeLabels(raw []json.RawMessage, colors map[string]string) []model.Label {
	out := []model.Label{}
	for _, r := range raw {
		name := labelName(r)
		c, ok := colors[name]
		if !ok {
			c = defaultColor
		}
		out = append(out, model.Label{Name: name, Color: stripHash(c)})
	}
	return out
}

// normalizeNotes keeps the notes that have a text and are not system notes. An internal note (and, on older GitLab, a
// confidential one) is Private: only someone the host lets read it may see it.
func normalizeNotes(notes []glNote) []model.Comment {
	out := []model.Comment{}
	for _, n := range notes {
		if n.System || n.Body == "" {
			continue
		}
		out = append(out, model.Comment{
			ID: strconv.FormatInt(n.ID, 10), Author: userName(n.Author), CreatedAt: n.CreatedAt, Body: n.Body,
			Private: n.Internal || n.Confidential,
		})
	}
	return out
}

// parseTime reads an ISO time; an unreadable one is the zero time, which sorts first.
func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// isoMillis is a time as the API takes it and the stores keep it: ISO 8601 with milliseconds and UTC.
func isoMillis(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

// mappedEvents is the history of a record: state changes, label and milestone changes, oldest first.
func mappedEvents(states []glStateEvent, labels []glLabelEvent, milestones []glMilestoneEvent) []model.Event {
	var out []model.Event
	for _, e := range states {
		typ := "Reopened"
		switch e.State {
		case "closed":
			typ = "Closed"
		case "merged":
			typ = "Merged"
		}
		ev := model.Event{Type: typ, At: e.CreatedAt, Actor: userName(e.User)}
		ev.SetField("reason", nil)
		out = append(out, ev)
	}
	for _, e := range labels {
		typ := "Labeled"
		if e.Action == "remove" {
			typ = "Unlabeled"
		}
		ev := model.Event{Type: typ, At: e.CreatedAt, Actor: userName(e.User)}
		if e.Label != nil {
			name := e.Label.Name
			if name == "" {
				name = e.Label.Title
			}
			ev.SetField("label", name)
		}
		out = append(out, ev)
	}
	for _, e := range milestones {
		typ := "Milestoned"
		if e.Action == "remove" {
			typ = "Demilestoned"
		}
		ev := model.Event{Type: typ, At: e.CreatedAt, Actor: userName(e.User)}
		if e.Milestone != nil {
			title := e.Milestone.Title
			if title == "" {
				title = e.Milestone.Name
			}
			ev.SetField("milestone", title)
		}
		out = append(out, ev)
	}
	kept := []model.Event{}
	for _, e := range out {
		if e.At != "" {
			kept = append(kept, e)
		}
	}
	sort.SliceStable(kept, func(i, j int) bool { return parseTime(kept[i].At).Before(parseTime(kept[j].At)) })
	return kept
}

// relations is an issue's blockers, from its links: "is_blocked_by" makes the other issue a blocker of this one, and
// "blocks" makes it blocked. "relates_to" is not a dependency and is left out.
type relations struct {
	blockedBy []model.Ref
	blocking  []model.Ref
}

func relationsOf(links []glLink) relations {
	var r relations
	for _, l := range links {
		if l.IID <= 0 {
			continue
		}
		switch l.LinkType {
		case "is_blocked_by":
			r.blockedBy = append(r.blockedBy, model.RefOf(l.IID))
		case "blocks":
			r.blocking = append(r.blocking, model.RefOf(l.IID))
		}
	}
	return r
}

// countLines counts the lines that start with prefix and not with skip. It is the JavaScript line counts of a diff
// (/^\+(?!\+\+)/gm and /^-(?!--)/gm) without the lookahead RE2 does not have.
func countLines(diff string, prefix, skip string) int {
	n := 0
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, prefix) && !strings.HasPrefix(line, skip) {
			n++
		}
	}
	return n
}

// glState maps GitLab's state to the stores' ("OPEN", "CLOSED", "MERGED").
func glState(s string) string {
	switch s {
	case "merged":
		return "MERGED"
	case "opened":
		return "OPEN"
	}
	return "CLOSED"
}

// isDraft is a merge request's draft flag: its draft field, or else its work-in-progress one.
func isDraft(m *glMergeRequest) bool {
	if m.Draft != nil {
		return *m.Draft
	}
	return m.WorkInProgress != nil && *m.WorkInProgress
}

// milestoneOf is a record's milestone, or nil.
func milestoneOf(m *glMilestone) *model.Milestone {
	if m == nil {
		return nil
	}
	return &model.Milestone{Title: m.Title, DueOn: m.DueDate}
}

// assigneesOf is the assignees of an issue or a merge request: the list, or the single assignee when there is no list.
func assigneesOf(list []glUser, single *glUser) []string {
	if list != nil {
		return userNames(list)
	}
	if single != nil {
		return userNames([]glUser{*single})
	}
	return []string{}
}
