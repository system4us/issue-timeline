package jira

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"time"

	"gitlab.com/system4us/issue-timeline/internal/model"
)

// jiraIssue is one issue as the REST API gives it with fields and expand=changelog. The fields stay raw: the start date
// is a field of the site's own, read by its id.
type jiraIssue struct {
	ID        string          `json:"id"`
	Key       string          `json:"key"`
	Fields    json.RawMessage `json:"fields"`
	Changelog *struct {
		Histories []jiraHistory `json:"histories"`
	} `json:"changelog"`
}

type jiraFields struct {
	Duedate        *string         `json:"duedate"`
	Summary        string          `json:"summary"`
	Description    json.RawMessage `json:"description"`
	Status         *jiraStatus     `json:"status"`
	Labels         []string        `json:"labels"`
	Assignee       *jiraUser       `json:"assignee"`
	Reporter       *jiraUser       `json:"reporter"`
	Creator        *jiraUser       `json:"creator"`
	Created        *string         `json:"created"`
	Updated        *string         `json:"updated"`
	Resolutiondate *string         `json:"resolutiondate"`
	FixVersions    []jiraVersion   `json:"fixVersions"`
	Parent         *struct {
		Key string `json:"key"`
	} `json:"parent"`
	Subtasks []struct {
		Key string `json:"key"`
	} `json:"subtasks"`
	Issuelinks []jiraLink `json:"issuelinks"`
	Comment    *struct {
		Total    *int          `json:"total"`
		Comments []jiraComment `json:"comments"`
	} `json:"comment"`
}

type jiraStatus struct {
	Name           string `json:"name"`
	StatusCategory *struct {
		Key string `json:"key"`
	} `json:"statusCategory"`
}

type jiraUser struct {
	DisplayName *string `json:"displayName"`
}

type jiraVersion struct {
	Name        string  `json:"name"`
	ReleaseDate *string `json:"releaseDate"`
}

type jiraLink struct {
	Type *struct {
		Name string `json:"name"`
	} `json:"type"`
	Outward *struct {
		Key string `json:"key"`
	} `json:"outwardIssue"`
	Inward *struct {
		Key string `json:"key"`
	} `json:"inwardIssue"`
}

type jiraComment struct {
	ID      string          `json:"id"`
	Author  *jiraUser       `json:"author"`
	Created *string         `json:"created"`
	Body    json.RawMessage `json:"body"`
}

type jiraHistory struct {
	Author  *jiraUser  `json:"author"`
	Created *string    `json:"created"`
	Items   []jiraItem `json:"items"`
}

type jiraItem struct {
	Field      string  `json:"field"`
	FromString *string `json:"fromString"`
	ToString   *string `json:"toString"`
}

// jiraNorm is what a normalization needs besides the issue: the names of the statuses that are done, the site's origin
// (the issue's link), and the id of the site's "Start date" field ("" when the site has none).
type jiraNorm struct {
	done       map[string]bool
	origin     string
	startField string
}

// zoneRE matches a zone offset written without its colon ("+0000"): Jira writes it so, and the dates are kept as ISO.
var zoneRE = regexp.MustCompile(`([+-]\d{2})(\d{2})$`)

// isoJira is a Jira timestamp in ISO form ("+0000" becomes "+00:00"). An empty timestamp stays empty.
func isoJira(s string) string {
	return zoneRE.ReplaceAllString(s, "$1:$2")
}

// parseJiraTime reads a Jira timestamp; the zero time when it is not one.
func parseJiraTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, isoJira(s))
	return t
}

// normalizeIssue is the store's record for one issue: its status (closed when its category is done, or its status name
// is one of the done ones), its changelog as events, comments, the "Blocks" links as blocking and blocked-by, and the
// parent and subtasks as the tree.
func normalizeIssue(raw jiraIssue, n jiraNorm) (*model.Record, error) {
	var f jiraFields
	if len(raw.Fields) > 0 {
		if err := json.Unmarshal(raw.Fields, &f); err != nil {
			return nil, err
		}
	}
	state := "OPEN"
	if (f.Status != nil && f.Status.StatusCategory != nil && f.Status.StatusCategory.Key == "done") || (f.Status != nil && n.done[f.Status.Name]) {
		state = "CLOSED"
	}

	comments := []model.Comment{}
	commentsCount := 0
	if f.Comment != nil {
		for _, c := range f.Comment.Comments {
			comments = append(comments, model.Comment{
				ID: c.ID, Author: userName(c.Author), CreatedAt: isoJira(deref(c.Created)), Body: adfOf(c.Body),
			})
		}
		commentsCount = len(comments)
		if f.Comment.Total != nil {
			commentsCount = *f.Comment.Total
		}
	}

	var events []model.Event
	if raw.Changelog != nil {
		for _, h := range raw.Changelog.Histories {
			for _, item := range h.Items {
				events = append(events, historyEvents(h, item, n.done)...)
			}
		}
	}
	sort.SliceStable(events, func(a, b int) bool { return parseJiraTime(events[a].At).Before(parseJiraTime(events[b].At)) })

	var blockedBy, blocking []model.Ref
	for _, l := range f.Issuelinks {
		if l.Type == nil || l.Type.Name != "Blocks" {
			continue // Jira's own "Blocks" link; other link types are not dependencies
		}
		if l.Outward != nil {
			blocking = model.AddRef(blocking, model.Ref(l.Outward.Key)) // this issue blocks it
		}
		if l.Inward != nil {
			blockedBy = model.AddRef(blockedBy, model.Ref(l.Inward.Key)) // it blocks this issue
		}
	}

	labels := []model.Label{}
	for _, name := range f.Labels {
		labels = append(labels, model.Label{Name: name, Color: "b6c5d2"})
	}
	assignees := []string{}
	if f.Assignee != nil {
		assignees = append(assignees, deref(f.Assignee.DisplayName))
	}
	var milestone *model.Milestone
	if len(f.FixVersions) > 0 {
		v := f.FixVersions[0]
		milestone = &model.Milestone{Title: v.Name, DueOn: v.ReleaseDate}
	}
	var parent *model.Ref
	if f.Parent != nil {
		p := model.Ref(f.Parent.Key)
		parent = &p
	}
	var subIssues []model.Ref
	for _, s := range f.Subtasks {
		subIssues = append(subIssues, model.Ref(s.Key))
	}
	var closedAt *string
	if state == "CLOSED" {
		closedAt = isoOrNil(f.Resolutiondate)
	}
	author := f.Creator
	if author == nil {
		author = f.Reporter
	}

	return &model.Record{
		Kind:          "issue",
		ID:            raw.ID,
		Key:           raw.Key,
		Title:         f.Summary,
		Body:          adfOf(f.Description),
		URL:           n.origin + "/browse/" + raw.Key,
		Author:        userName(author),
		CreatedAt:     isoJira(deref(f.Created)),
		UpdatedAt:     isoJira(deref(f.Updated)),
		ClosedAt:      closedAt,
		State:         state,
		Labels:        labels,
		Assignees:     assignees,
		Milestone:     milestone,
		CommentsCount: commentsCount,
		Comments:      comments,
		Events:        nonNilEvents(events),
		DueDate:       f.Duedate,
		StartDate:     startDate(raw.Fields, n.startField),
		Parent:        parent,
		SubIssues:     subIssues,
		BlockedBy:     blockedBy,
		Blocking:      blocking,
	}, nil
}

// historyEvents is the event one changelog item makes, if any: a status moving into or out of done, an assignee
// change, a fix version change.
func historyEvents(h jiraHistory, item jiraItem, done map[string]bool) []model.Event {
	base := model.Event{At: isoJira(deref(h.Created)), Actor: userName(h.Author)}
	with := func(typ, field string, v any) model.Event {
		e := base
		e.Type = typ
		if field != "" {
			e.SetField(field, v)
		}
		return e
	}
	switch item.Field {
	case "status":
		from, to := done[deref(item.FromString)], done[deref(item.ToString)]
		switch {
		case !from && to:
			return []model.Event{with("Closed", "reason", nil)}
		case from && !to:
			return []model.Event{with("Reopened", "", nil)}
		}
	case "assignee":
		return []model.Event{with("Assigned", "assignee", item.ToString)}
	case "Fix Version":
		var milestone any
		if item.ToString != nil && *item.ToString != "" {
			milestone = *item.ToString
		}
		return []model.Event{with("Milestoned", "milestone", milestone)}
	}
	return nil
}

// startDate is the site's "Start date" field of the issue, when the site has one (its id is found by name, see
// startFieldID); nil otherwise.
func startDate(fields json.RawMessage, startField string) *string {
	if startField == "" || len(fields) == 0 {
		return nil
	}
	var all map[string]json.RawMessage
	if err := json.Unmarshal(fields, &all); err != nil {
		return nil
	}
	var v *string
	_ = json.Unmarshal(all[startField], &v)
	return v
}

// adfOf is the plain text of a field in Atlassian Document Format (descriptions and comments), trimmed.
func adfOf(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return ""
	}
	return strings.TrimSpace(adfText(doc))
}

// adfText is the text of an Atlassian Document Format node: its text, its hard breaks, and a line after each paragraph,
// heading and list item.
func adfText(node any) string {
	switch n := node.(type) {
	case nil:
		return ""
	case string:
		return n
	case map[string]any:
		typ, _ := n["type"].(string)
		switch typ {
		case "text":
			s, _ := n["text"].(string)
			return s
		case "hardBreak":
			return "\n"
		}
		var b strings.Builder
		if kids, ok := n["content"].([]any); ok {
			for _, k := range kids {
				b.WriteString(adfText(k))
			}
		}
		if typ == "paragraph" || typ == "heading" || typ == "listItem" {
			b.WriteString("\n")
		}
		return b.String()
	}
	return ""
}

// userName is a user's display name, nil when there is no user.
func userName(u *jiraUser) *string {
	if u == nil {
		return nil
	}
	return u.DisplayName
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func isoOrNil(s *string) *string {
	if s == nil {
		return nil
	}
	v := isoJira(*s)
	return &v
}

func nonNilEvents(e []model.Event) []model.Event {
	if e == nil {
		return []model.Event{}
	}
	return e
}
