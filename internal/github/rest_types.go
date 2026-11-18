package github

import (
	"gitlab.com/system4us/issue-timeline/internal/model"
	"strings"
)

// The REST answers this package reads, as GitHub gives them (snake_case names).

type restUser struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
	Name  string `json:"name"`
}

type restLabel struct {
	Name  string  `json:"name"`
	Color *string `json:"color"`
}

type restMilestone struct {
	Number int     `json:"number"`
	Title  string  `json:"title"`
	DueOn  *string `json:"due_on"`
}

type restIssueType struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	IsEnabled *bool  `json:"is_enabled"`
}

// restComment is one comment of an issue in REST, as the comment list and a POST of one answer it.
type restComment struct {
	ID        int64     `json:"id"`
	NodeID    string    `json:"node_id"`
	User      *restUser `json:"user"`
	CreatedAt string    `json:"created_at"`
	Body      string    `json:"body"`
}

type restIssue struct {
	ID          int64   `json:"id"`
	NodeID      string  `json:"node_id"`
	Number      int     `json:"number"`
	Title       string  `json:"title"`
	Body        *string `json:"body"`
	State       string  `json:"state"`
	StateReason *string `json:"state_reason"`
	Locked      bool    `json:"locked"`
	Type        *struct {
		Name string `json:"name"`
	} `json:"type"`
	Labels    []restLabel    `json:"labels"`
	Assignees []restUser     `json:"assignees"`
	Milestone *restMilestone `json:"milestone"`
	ClosedAt  *string        `json:"closed_at"`
	CreatedAt string         `json:"created_at"`
	UpdatedAt string         `json:"updated_at"`
	HTMLURL   string         `json:"html_url"`
	User      *restUser      `json:"user"`
}

type restPull struct {
	Title              string         `json:"title"`
	Body               *string        `json:"body"`
	State              string         `json:"state"`
	Draft              bool           `json:"draft"`
	MergedAt           *string        `json:"merged_at"`
	NodeID             string         `json:"node_id"`
	Labels             []restLabel    `json:"labels"`
	Assignees          []restUser     `json:"assignees"`
	Milestone          *restMilestone `json:"milestone"`
	RequestedReviewers []restUser     `json:"requested_reviewers"`
	UpdatedAt          string         `json:"updated_at"`
	MergeCommitSHA     *string        `json:"merge_commit_sha"`
}

// issueState is what an issue read gives the record: the fields GitHub's issue answer carries.
type issueState struct {
	Title       string
	Body        string
	State       string
	StateReason *string
	Locked      bool
	IssueType   *string
	Labels      []model.Label
	Assignees   []string
	Milestone   *model.Milestone
	ClosedAt    *string
	UpdatedAt   string
}

// issueStateOf reads an issue answer the way the store keeps it.
func issueStateOf(i restIssue) issueState {
	return issueState{
		Title:       i.Title,
		Body:        derefOr(i.Body),
		State:       stateOf(i.State),
		StateReason: upperReason(i.StateReason),
		Locked:      i.Locked,
		IssueType:   issueTypeName(i),
		Labels:      labelsFrom(i.Labels),
		Assignees:   assigneesFrom(i.Assignees),
		Milestone:   milestoneFrom(i.Milestone),
		ClosedAt:    i.ClosedAt,
		UpdatedAt:   i.UpdatedAt,
	}
}

// applyIssueState puts an issue read on a record (the record is a copy the caller owns).
func applyIssueState(r *model.Record, s issueState) {
	r.Title = s.Title
	r.Body = s.Body
	r.State = s.State
	r.StateReason = s.StateReason
	r.Locked = model.Ptr(s.Locked)
	r.IssueType = s.IssueType
	r.Labels = s.Labels
	r.Assignees = s.Assignees
	r.Milestone = s.Milestone
	r.ClosedAt = s.ClosedAt
	r.UpdatedAt = s.UpdatedAt
}

// stateOf maps GitHub's state to the store's: OPEN or CLOSED.
func stateOf(s string) string {
	if s == "closed" {
		return "CLOSED"
	}
	return "OPEN"
}

// pullStateOf is a pull request's state: MERGED when it has merged, else CLOSED or OPEN.
func pullStateOf(p restPull) string {
	if p.MergedAt != nil {
		return "MERGED"
	}
	return stateOf(p.State)
}

// issueTypeName is the issue's type name, or nil.
func issueTypeName(i restIssue) *string {
	if i.Type == nil {
		return nil
	}
	name := i.Type.Name
	return &name
}

// labelsFrom is the store's labels; GitHub's color is kept, a missing one is the Node server's default.
func labelsFrom(ls []restLabel) []model.Label {
	out := []model.Label{}
	for _, l := range ls {
		color := "b6c5d2"
		if l.Color != nil {
			color = *l.Color
		}
		out = append(out, model.Label{Name: l.Name, Color: color})
	}
	return out
}

func assigneesFrom(us []restUser) []string {
	out := []string{}
	for _, u := range us {
		out = append(out, u.Login)
	}
	return out
}

func loginsOfUsers(us []restUser) []string {
	return assigneesFrom(us)
}

func milestoneFrom(m *restMilestone) *model.Milestone {
	if m == nil {
		return nil
	}
	return &model.Milestone{Title: m.Title, DueOn: m.DueOn}
}

// derefOr is the string a pointer holds, or "".
func derefOr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// sameText reports whether two optional texts are the same (nil only equals nil).
func sameText(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// upperReason is a REST state_reason ("not_planned") the way the GraphQL sync keeps it ("NOT_PLANNED"), so a record
// read back after an edit or a refresh says the same as one read by the sync.
func upperReason(r *string) *string {
	if r == nil {
		return nil
	}
	u := strings.ToUpper(*r)
	return &u
}
