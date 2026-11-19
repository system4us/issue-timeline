package github

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"gitlab.com/system4us/issue-timeline/internal/model"
	"gitlab.com/system4us/issue-timeline/internal/store"
)

// The metadata that changes without the issue's own updated_at (a lock, an issue type, a blocker) is read again on a
// schedule: a small batch per sync, the least recently read first, one GraphQL query per batch.

// metaBatch is how many issues one refresh reads (META_BATCH, 20).
var metaBatch = envInt("META_BATCH", 20)

// pickStale is the issues to read again: the least recently read first (never read comes first), at most n.
func pickStale(issues map[string]*model.Record, n int) []*model.Record {
	var recs []*model.Record
	for _, r := range issues {
		if r != nil && r.Kind == "issue" {
			recs = append(recs, r)
		}
	}
	sort.SliceStable(recs, func(i, j int) bool {
		if recs[i].ReadAt != recs[j].ReadAt {
			return recs[i].ReadAt < recs[j].ReadAt
		}
		return recs[i].Key < recs[j].Key
	})
	if len(recs) > n {
		recs = recs[:n]
	}
	return recs
}

// githubBatchQuery is one GraphQL query for several issues of the repo, by alias, and the quota it cost.
func githubBatchQuery(numbers []int) string {
	parts := make([]string, 0, len(numbers))
	for k, n := range numbers {
		parts = append(parts, fmt.Sprintf("i%d: issue(number: %d) { number state stateReason locked issueType { name } labels(first: 50) { nodes { name color } } assignees(first: 20) { nodes { login } } milestone { title dueOn } parent { number } subIssues(first: 100) { nodes { number } } blockedBy(first: 50) { nodes { number } } blocking(first: 50) { nodes { number } } }", k, n))
	}
	return fmt.Sprintf("query($owner: String!, $name: String!) { repository(owner: $owner, name: $name) { %s } rateLimit { remaining resetAt cost } }", strings.Join(parts, "\n"))
}

// metaNode is one issue of the batch query.
type metaNode struct {
	State       string  `json:"state"`
	StateReason *string `json:"stateReason"`
	Locked      bool    `json:"locked"`
	IssueType   *struct {
		Name string `json:"name"`
	} `json:"issueType"`
	Labels    *conn[labelNode]  `json:"labels"`
	Assignees *conn[loginNode]  `json:"assignees"`
	Milestone *milestoneNode    `json:"milestone"`
	Parent    *numberNode       `json:"parent"`
	SubIssues *conn[numberNode] `json:"subIssues"`
	BlockedBy *conn[numberNode] `json:"blockedBy"`
	Blocking  *conn[numberNode] `json:"blocking"`
}

// metaFields is the metadata a batch read gives a record.
type metaFields struct {
	State       string
	StateReason *string
	Locked      bool
	IssueType   *string
	Labels      []model.Label
	Assignees   []string
	Milestone   *model.Milestone
	Parent      *model.Ref
	SubIssues   []model.Ref
	BlockedBy   []model.Ref
	Blocking    []model.Ref
}

// metaFromNode is the metadata of one batch node, shaped as the store keeps it.
func metaFromNode(n *metaNode) metaFields {
	m := metaFields{
		State:       "OPEN",
		StateReason: n.StateReason,
		Locked:      n.Locked,
		Labels:      labelsOf(n.Labels),
		Assignees:   loginsOf(n.Assignees),
		Milestone:   milestoneOf(n.Milestone),
		SubIssues:   refsOf(n.SubIssues),
		BlockedBy:   refsOf(n.BlockedBy),
		Blocking:    refsOf(n.Blocking),
	}
	if n.State == "CLOSED" {
		m.State = "CLOSED"
	}
	if n.IssueType != nil {
		name := n.IssueType.Name
		m.IssueType = &name
	}
	if n.Parent != nil {
		ref := model.RefOf(n.Parent.Number)
		m.Parent = &ref
	}
	return m
}

// applyMeta puts the metadata on a record (a copy the caller owns).
func applyMeta(r *model.Record, m metaFields) {
	r.State = m.State
	r.StateReason = m.StateReason
	r.Locked = model.Ptr(m.Locked)
	r.IssueType = m.IssueType
	r.Labels = m.Labels
	r.Assignees = m.Assignees
	r.Milestone = m.Milestone
	r.Parent = m.Parent
	r.SubIssues = m.SubIssues
	r.BlockedBy = m.BlockedBy
	r.Blocking = m.Blocking
}

// RefreshMeta reads the next batch of stale issues in one query and stores what it reads. It returns how many were read
// and the quota left (and when it resets) when GitHub says.
func (r *Repo) RefreshMeta(ctx context.Context, st *store.Store) (int, *int, *time.Time, error) {
	read, remaining, resetAt, err := r.refreshMeta(ctx, st)
	return read, remaining, resetAt, asProvider(err)
}

func (r *Repo) refreshMeta(ctx context.Context, st *store.Store) (int, *int, *time.Time, error) {
	var batch []*model.Record
	st.View(func(d *store.Data) { batch = pickStale(d.Issues, metaBatch) })
	var numbers []int
	var keep []*model.Record
	for _, rec := range batch {
		n, err := recordNumber(rec)
		if err != nil {
			continue
		}
		numbers = append(numbers, n)
		keep = append(keep, rec)
	}
	if len(keep) == 0 {
		return 0, nil, nil, nil
	}
	tok, err := r.serverToken(ctx)
	if err != nil {
		return 0, nil, nil, err
	}
	var data struct {
		Repository map[string]*metaNode `json:"repository"`
		RateLimit  *struct {
			Remaining *int    `json:"remaining"`
			ResetAt   *string `json:"resetAt"`
		} `json:"rateLimit"`
	}
	if err := r.api.graphqlInto(ctx, r.o.Endpoints.GraphQL, tok, githubBatchQuery(numbers), r.vars(), &data); err != nil {
		return 0, nil, nil, err
	}
	fresh := map[string]metaFields{}
	for k, rec := range keep {
		node := data.Repository[fmt.Sprintf("i%d", k)]
		if node == nil {
			continue
		}
		fresh[rec.Key] = metaFromNode(node)
	}
	if len(fresh) > 0 {
		now := time.Now().UTC().Format(isoMillis)
		st.Update(func(d *store.Data) {
			for key, m := range fresh {
				cur := d.Issues[key]
				if cur == nil {
					continue
				}
				c := *cur // copy: a stored record is never changed in place
				applyMeta(&c, m)
				c.ReadAt = now
				d.Issues[key] = &c
			}
		})
		if err := st.Save(); err != nil {
			return len(fresh), nil, nil, fmt.Errorf("saving %s: %w", r.Key(), err)
		}
	}
	var remaining *int
	var resetAt *time.Time
	if data.RateLimit != nil {
		remaining = data.RateLimit.Remaining
		if data.RateLimit.ResetAt != nil {
			if t, err := time.Parse(time.RFC3339, *data.RateLimit.ResetAt); err == nil {
				resetAt = &t
			}
		}
	}
	return len(fresh), remaining, resetAt, nil
}

// envInt is an integer from the environment, or def when it is missing or not a number.
func envInt(name string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(name)); err == nil && v > 0 {
		return v
	}
	return def
}
