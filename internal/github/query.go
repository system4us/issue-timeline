package github

import (
	"fmt"
	"strconv"
	"strings"

	"gitlab.com/system4us/issue-timeline/internal/model"
)

// What is asked of GitHub (GraphQL selections) and the typed answers. The selections are the Node server's, word for
// word, so the same answers come back.

const (
	commentSel = "id author{login} createdAt body"
	reviewSel  = "author{login} state submittedAt body"
	commitSel  = "commit{oid messageHeadline committedDate authoredDate author{user{login}}}"

	// An issue's timeline union and a pull request's differ: a fragment on a type the union lacks is an error.
	issueEventSel = `__typename
  ... on LabeledEvent{createdAt actor{login} label{name}}
  ... on UnlabeledEvent{createdAt actor{login} label{name}}
  ... on ClosedEvent{createdAt actor{login} stateReason}
  ... on ReopenedEvent{createdAt actor{login}}
  ... on AssignedEvent{createdAt actor{login} assignee{... on User{login}}}
  ... on MilestonedEvent{createdAt milestoneTitle}
  ... on RenamedTitleEvent{createdAt currentTitle previousTitle}
  ... on CrossReferencedEvent{createdAt isCrossRepository source{__typename ... on Issue{number} ... on PullRequest{number}}}`
	pullEventSel = issueEventSel + `
  ... on MergedEvent{createdAt actor{login}}
  ... on ReadyForReviewEvent{createdAt actor{login}}
  ... on ConvertToDraftEvent{createdAt actor{login}}
  ... on ReviewRequestedEvent{createdAt requestedReviewer{... on User{login}}}`

	commonFields = `title body url state createdAt closedAt author{login}
  labels(first:50){nodes{name color}}
  assignees(first:20){nodes{login}}
  milestone{title dueOn}`
	issueFields = commonFields + ` stateReason issueType{name} parent{number} subIssues(first:100){nodes{number}}
  blockedBy(first:50){nodes{number}} blocking(first:50){nodes{number}}`
	pullFields = commonFields + ` isDraft merged mergedAt additions deletions changedFiles baseRefName headRefName
  mergedBy{login} reviewDecision closingIssuesReferences(first:50){nodes{number}}`
)

var (
	issueEventTypes = []string{
		"LABELED_EVENT", "UNLABELED_EVENT", "CLOSED_EVENT", "REOPENED_EVENT", "ASSIGNED_EVENT",
		"MILESTONED_EVENT", "RENAMED_TITLE_EVENT", "CROSS_REFERENCED_EVENT",
	}
	pullEventTypes = []string{
		"LABELED_EVENT", "UNLABELED_EVENT", "CLOSED_EVENT", "REOPENED_EVENT", "MERGED_EVENT",
		"READY_FOR_REVIEW_EVENT", "CONVERT_TO_DRAFT_EVENT", "REVIEW_REQUESTED_EVENT", "CROSS_REFERENCED_EVENT",
	}
	issueItemArgs = "itemTypes:[" + strings.Join(issueEventTypes, ",") + "]"
	pullItemArgs  = "itemTypes:[" + strings.Join(pullEventTypes, ",") + "]"
)

// connection is one `name(first:100, args){ totalCount pageInfo nodes{ sel } }` of a node.
func connection(name, args, sel string) string {
	return fmt.Sprintf("%s(first:100%s){ totalCount pageInfo{hasNextPage endCursor} nodes{ %s } }", name, argsSuffix(args), sel)
}

// argsSuffix is ",args" for a connection that has arguments, "" for one that has none.
func argsSuffix(args string) string {
	if args == "" {
		return ""
	}
	return "," + args
}

var (
	issueConns = connection("comments", "", commentSel) + "\n" + connection("timelineItems", issueItemArgs, issueEventSel)
	pullConns  = connection("comments", "", commentSel) + "\n" + connection("timelineItems", pullItemArgs, pullEventSel) + "\n" +
		connection("reviews", "", reviewSel) + "\n" + connection("commits", "", commitSel)

	issuesQuery = fmt.Sprintf(`query($owner:String!,$name:String!,$after:String,$states:[IssueState!]){
  repository(owner:$owner,name:$name){
    issues(first:30,after:$after,states:$states,orderBy:{field:UPDATED_AT,direction:DESC}){
      totalCount
      pageInfo{hasNextPage endCursor}
      nodes{ id number updatedAt %s %s }
    }
  }
}`, issueFields, issueConns)

	pullsQuery = fmt.Sprintf(`query($owner:String!,$name:String!,$after:String,$states:[PullRequestState!]){
  repository(owner:$owner,name:$name){
    pullRequests(first:20,after:$after,states:$states,orderBy:{field:UPDATED_AT,direction:DESC}){
      totalCount
      pageInfo{hasNextPage endCursor}
      nodes{ id number updatedAt %s %s }
    }
  }
}`, pullFields, pullConns)

	// the rest of one node's connection, when the first page had more
	drainQuery = `query($id:ID!,$after:String){ node(id:$id){ ... on %s{
      %s(first:100,after:$after%s){ pageInfo{hasNextPage endCursor} nodes{ %s } } } } }`
)

// ---- the answers, as JSON

type pageInfo struct {
	HasNextPage bool   `json:"hasNextPage"`
	EndCursor   string `json:"endCursor"`
}

// conn is a connection of GitHub: its total, its page info and its nodes (a null node is a nil pointer).
type conn[T any] struct {
	TotalCount int      `json:"totalCount"`
	PageInfo   pageInfo `json:"pageInfo"`
	Nodes      []*T     `json:"nodes"`
}

type loginNode struct {
	Login string `json:"login"`
}

type numberNode struct {
	Number int `json:"number"`
}

type labelNode struct {
	Name  string `json:"name"`
	Color string `json:"color"`
}

type commentNode struct {
	ID        string     `json:"id"`
	Author    *loginNode `json:"author"`
	CreatedAt string     `json:"createdAt"`
	Body      string     `json:"body"`
}

// eventNode is any event of a timeline: the fields of every event type the query asks for, in one object (GitHub
// returns them flat, and a field only the other types have stays nil).
type eventNode struct {
	Typename  string     `json:"__typename"`
	CreatedAt string     `json:"createdAt"`
	Actor     *loginNode `json:"actor"`
	Label     *struct {
		Name string `json:"name"`
	} `json:"label"`
	StateReason       *string    `json:"stateReason"`
	Assignee          *loginNode `json:"assignee"`
	MilestoneTitle    *string    `json:"milestoneTitle"`
	CurrentTitle      *string    `json:"currentTitle"`
	PreviousTitle     *string    `json:"previousTitle"`
	IsCrossRepository bool       `json:"isCrossRepository"`
	Source            *struct {
		Typename string `json:"__typename"`
		Number   *int   `json:"number"`
	} `json:"source"`
	RequestedReviewer *loginNode `json:"requestedReviewer"`
}

type reviewNode struct {
	Author      *loginNode `json:"author"`
	State       string     `json:"state"`
	SubmittedAt *string    `json:"submittedAt"`
	Body        string     `json:"body"`
}

type prCommitNode struct {
	Commit *struct {
		Oid             string `json:"oid"`
		MessageHeadline string `json:"messageHeadline"`
		CommittedDate   string `json:"committedDate"`
		Author          *struct {
			User *loginNode `json:"user"`
		} `json:"author"`
	} `json:"commit"`
}

// commonNode is what an issue and a pull request both have.
type commonNode struct {
	ID        string             `json:"id"`
	Number    int                `json:"number"`
	UpdatedAt string             `json:"updatedAt"`
	Title     string             `json:"title"`
	Body      string             `json:"body"`
	URL       string             `json:"url"`
	State     string             `json:"state"`
	CreatedAt string             `json:"createdAt"`
	ClosedAt  *string            `json:"closedAt"`
	Author    *loginNode         `json:"author"`
	Labels    *conn[labelNode]   `json:"labels"`
	Assignees *conn[loginNode]   `json:"assignees"`
	Milestone *milestoneNode     `json:"milestone"`
	Comments  *conn[commentNode] `json:"comments"`
	Timeline  *conn[eventNode]   `json:"timelineItems"`
}

type milestoneNode struct {
	Title string  `json:"title"`
	DueOn *string `json:"dueOn"`
}

type issueNode struct {
	commonNode
	StateReason *string `json:"stateReason"`
	IssueType   *struct {
		Name string `json:"name"`
	} `json:"issueType"`
	Parent    *numberNode       `json:"parent"`
	SubIssues *conn[numberNode] `json:"subIssues"`
	BlockedBy *conn[numberNode] `json:"blockedBy"`
	Blocking  *conn[numberNode] `json:"blocking"`
}

type pullNode struct {
	commonNode
	IsDraft                 bool                `json:"isDraft"`
	Merged                  bool                `json:"merged"`
	MergedAt                *string             `json:"mergedAt"`
	Additions               int                 `json:"additions"`
	Deletions               int                 `json:"deletions"`
	ChangedFiles            int                 `json:"changedFiles"`
	BaseRefName             string              `json:"baseRefName"`
	HeadRefName             string              `json:"headRefName"`
	MergedBy                *loginNode          `json:"mergedBy"`
	ReviewDecision          *string             `json:"reviewDecision"`
	ClosingIssuesReferences *conn[numberNode]   `json:"closingIssuesReferences"`
	Reviews                 *conn[reviewNode]   `json:"reviews"`
	Commits                 *conn[prCommitNode] `json:"commits"`
}

// ---- answers become records

func who(u *loginNode) *string {
	if u == nil {
		return nil
	}
	s := u.Login
	return &s
}

func labelsOf(c *conn[labelNode]) []model.Label {
	out := []model.Label{}
	if c == nil {
		return out
	}
	for _, l := range c.Nodes {
		if l != nil {
			out = append(out, model.Label{Name: l.Name, Color: l.Color})
		}
	}
	return out
}

func loginsOf(c *conn[loginNode]) []string {
	out := []string{}
	if c == nil {
		return out
	}
	for _, l := range c.Nodes {
		if l != nil {
			out = append(out, l.Login)
		}
	}
	return out
}

func refsOf(c *conn[numberNode]) []model.Ref {
	out := []model.Ref{}
	if c == nil {
		return out
	}
	for _, n := range c.Nodes {
		if n != nil {
			out = append(out, model.RefOf(n.Number))
		}
	}
	return out
}

func totalOf[T any](c *conn[T]) int {
	if c == nil {
		return 0
	}
	return c.TotalCount
}

func milestoneOf(m *milestoneNode) *model.Milestone {
	if m == nil {
		return nil
	}
	return &model.Milestone{Title: m.Title, DueOn: m.DueOn}
}

// record is what an issue and a pull request share, as the store keeps it.
func (n *commonNode) record(kind string) *model.Record {
	r := &model.Record{
		Kind:          kind,
		ID:            n.ID,
		Key:           strconv.Itoa(n.Number), // the key is the number as text
		Number:        model.Ptr(n.Number),
		Title:         n.Title,
		Body:          n.Body,
		URL:           n.URL,
		Author:        who(n.Author),
		CreatedAt:     n.CreatedAt,
		UpdatedAt:     n.UpdatedAt,
		ClosedAt:      n.ClosedAt,
		State:         n.State,
		Labels:        labelsOf(n.Labels),
		Assignees:     loginsOf(n.Assignees),
		Milestone:     milestoneOf(n.Milestone),
		CommentsCount: totalOf(n.Comments),
		Comments:      []model.Comment{},
		Events:        []model.Event{},
	}
	if n.Comments != nil {
		for _, c := range n.Comments.Nodes {
			if c != nil {
				r.Comments = append(r.Comments, model.Comment{ID: c.ID, Author: who(c.Author), CreatedAt: c.CreatedAt, Body: c.Body})
			}
		}
	}
	if n.Timeline != nil {
		for _, e := range n.Timeline.Nodes {
			if e != nil {
				r.Events = append(r.Events, normalizeEvent(e))
			}
		}
	}
	return r
}

// normalizeEvent is one timeline entry: its type (the typename without "Event"), when, who, and the fields its type has.
func normalizeEvent(e *eventNode) model.Event {
	ev := model.Event{Type: strings.TrimSuffix(e.Typename, "Event"), At: e.CreatedAt, Actor: who(e.Actor)}
	switch e.Typename {
	case "LabeledEvent", "UnlabeledEvent":
		var label *string
		if e.Label != nil {
			label = &e.Label.Name
		}
		ev.SetField("label", label)
	case "ClosedEvent":
		ev.SetField("reason", e.StateReason)
	case "AssignedEvent":
		ev.SetField("assignee", who(e.Assignee))
	case "MilestonedEvent":
		ev.SetField("milestone", e.MilestoneTitle)
	case "RenamedTitleEvent":
		ev.SetField("from", e.PreviousTitle)
		ev.SetField("to", e.CurrentTitle)
	case "ReviewRequestedEvent":
		ev.SetField("reviewer", who(e.RequestedReviewer))
	case "CrossReferencedEvent":
		var number *int
		var kind *string
		if e.Source != nil {
			number = e.Source.Number
			kind = &e.Source.Typename
		}
		ev.SetField("source", number)
		ev.SetField("sourceKind", kind)
		ev.SetField("external", e.IsCrossRepository)
	}
	return ev
}

// normalizeIssue is an issue of the sync, as the store keeps it.
func normalizeIssue(n *issueNode) *model.Record {
	r := n.record("issue")
	r.StateReason = n.StateReason
	if n.IssueType != nil {
		name := n.IssueType.Name
		r.IssueType = &name
	}
	// GitHub issues have no due date. Its own sub-issue and dependency relations are read here; the checklists and the
	// blocker words of a body are read apart (derive.js).
	if n.Parent != nil {
		ref := model.RefOf(n.Parent.Number)
		r.Parent = &ref
	}
	r.SubIssues = refsOf(n.SubIssues)
	r.BlockedBy = refsOf(n.BlockedBy)
	r.Blocking = refsOf(n.Blocking)
	return r
}

// normalizePull is a pull request of the sync, as the store keeps it. A merged one is MERGED whatever its state says.
func normalizePull(n *pullNode) *model.Record {
	r := n.record("pull")
	if n.Merged {
		r.State = "MERGED"
	}
	r.IsDraft = model.Ptr(n.IsDraft)
	r.MergedAt = n.MergedAt
	r.MergedBy = who(n.MergedBy)
	r.ReviewDecision = n.ReviewDecision
	r.Base = n.BaseRefName
	r.Head = n.HeadRefName
	r.Additions = model.Ptr(n.Additions)
	r.Deletions = model.Ptr(n.Deletions)
	r.ChangedFiles = model.Ptr(n.ChangedFiles)
	r.Closes = refsOf(n.ClosingIssuesReferences)
	r.CommitsCount = model.Ptr(totalOf(n.Commits))
	r.Commits = []model.PullCommit{}
	if n.Commits != nil {
		for _, c := range n.Commits.Nodes {
			if c == nil || c.Commit == nil {
				continue
			}
			var author *string
			if c.Commit.Author != nil {
				author = who(c.Commit.Author.User)
			}
			r.Commits = append(r.Commits, model.PullCommit{Oid: c.Commit.Oid, Message: c.Commit.MessageHeadline, At: c.Commit.CommittedDate, Author: author})
		}
	}
	r.Reviews = []model.Review{}
	if n.Reviews != nil {
		for _, rv := range n.Reviews.Nodes {
			if rv != nil {
				r.Reviews = append(r.Reviews, model.Review{Author: who(rv.Author), State: rv.State, At: rv.SubmittedAt, Body: rv.Body})
			}
		}
	}
	return r
}
