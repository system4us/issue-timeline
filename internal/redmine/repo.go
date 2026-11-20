package redmine

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"time"

	"gitlab.com/system4us/issue-timeline/internal/provider"
)

// RelationTypes are the relation types Redmine knows, from its own relation form.
var RelationTypes = []string{"relates", "duplicates", "duplicated", "blocks", "blocked", "precedes", "follows", "copied_to", "copied_from"}

// Options is what the server gives a Repo. Key is the server's own API key (the sync and the metadata reads use it, and
// so do edits with no person behind them). EditorRoles are the Redmine role names that may edit (default Manager and
// Developer).
type Options struct {
	Key         func(ctx context.Context) (string, error)
	HTTP        *http.Client
	EditorRoles []string
}

// Repo is one Redmine project. It implements provider.Host, MetaRefresher, Editor and Accounts.
type Repo struct {
	key        string
	origin     string
	identifier string
	opt        Options
	http       *http.Client
	editors    map[string]bool
	sleep      func(context.Context, time.Duration) error
}

var (
	_ provider.Host          = (*Repo)(nil)
	_ provider.MetaRefresher = (*Repo)(nil)
	_ provider.Editor        = (*Repo)(nil)
	_ provider.Accounts      = (*Repo)(nil)
)

// New is the Repo for a repo key.
func New(key string, o Options) (*Repo, error) {
	origin, identifier, err := ParseRepo(key)
	if err != nil {
		return nil, err
	}
	h := sameOrigin(o.HTTP)
	if o.HTTP == nil {
		h.Timeout = requestTimeout
	}
	roles := o.EditorRoles
	if roles == nil {
		roles = []string{"Manager", "Developer"}
	}
	editors := make(map[string]bool, len(roles))
	for _, n := range roles {
		editors[n] = true
	}
	return &Repo{key: key, origin: origin, identifier: identifier, opt: o, http: h, editors: editors, sleep: sleep}, nil
}

// Key is the repo key.
func (r *Repo) Key() string { return r.key }

// Provider is "redmine".
func (r *Repo) Provider() string { return "redmine" }

// Origin is the site's origin, which sessions are matched against.
func (r *Repo) Origin() string { return r.origin }

// newClient is a client of this site with the given key.
func (r *Repo) newClient(key string) client {
	return client{origin: r.origin, key: key, http: r.http, sleep: r.sleep}
}

// serverClient is the server's own identity, for reads that are not for a person (the sync, the metadata refresh).
func (r *Repo) serverClient(ctx context.Context) (client, error) {
	return r.clientWith(ctx, "")
}

// clientFor is the credential an edit or a create uses: the person's, or the server's own when none is given (an edit
// from this machine, with no session).
func (r *Repo) clientFor(ctx context.Context, cred provider.Cred) (client, error) {
	return r.clientWith(ctx, cred.Token)
}

func (r *Repo) clientWith(ctx context.Context, token string) (client, error) {
	if token == "" {
		if r.opt.Key == nil {
			return client{}, errors.New("no Redmine API key")
		}
		k, err := r.opt.Key(ctx)
		if err != nil {
			return client{}, err
		}
		if k == "" {
			return client{}, errors.New("no Redmine API key: set redmine-api-key for this site")
		}
		token = k
	}
	return r.newClient(token), nil
}

// personClient is a signed-in person's credential. It is never replaced by the server's own key: a read made for a
// person must answer for that person, or fail.
func (r *Repo) personClient(cred provider.Cred) (client, error) {
	if cred.Token == "" {
		return client{}, errors.New("no API key for this person")
	}
	return r.newClient(cred.Token), nil
}

// Exists reports whether the project is there and the server's own key can read it.
func (r *Repo) Exists(ctx context.Context) error {
	c, err := r.serverClient(ctx)
	if err != nil {
		return err
	}
	if _, err := r.project(ctx, c); err != nil {
		if isNotFound(err) {
			return fmt.Errorf("Redmine project not found, or the API key cannot see it: %s: %w", r.key, provider.ErrNotFound)
		}
		return err
	}
	return nil
}

// rmProject is the project behind a repo key: its numeric id is what the lists filter by.
type rmProject struct {
	ID         int    `json:"id"`
	Name       string `json:"name"`
	Identifier string `json:"identifier"`
}

func (r *Repo) project(ctx context.Context, c client) (rmProject, error) {
	var d struct {
		Project rmProject `json:"project"`
	}
	err := c.do(ctx, http.MethodGet, "/projects/"+url.PathEscape(r.identifier)+".json", nil, "", nil, &d)
	return d.Project, err
}

// statuses maps each status id to whether it is a closed one.
func statuses(ctx context.Context, c client) (map[int]bool, error) {
	var d struct {
		IssueStatuses []struct {
			ID       int  `json:"id"`
			IsClosed bool `json:"is_closed"`
		} `json:"issue_statuses"`
	}
	if err := c.do(ctx, http.MethodGet, "/issue_statuses.json", nil, "", nil, &d); err != nil {
		return nil, err
	}
	closed := make(map[int]bool, len(d.IssueStatuses))
	for _, s := range d.IssueStatuses {
		closed[s.ID] = s.IsClosed
	}
	return closed, nil
}

// statusFor is the status id Redmine uses for "open" or "closed": the lowest id of that kind (New, and Closed, on a
// stock instance).
func statusFor(closed map[int]bool, state string) (int, error) {
	want := state == "CLOSED"
	best, found := 0, false
	for id, isClosed := range closed {
		if isClosed == want && (!found || id < best) {
			best, found = id, true
		}
	}
	if !found {
		if want {
			return 0, errors.New("Redmine has no closed status")
		}
		return 0, errors.New("Redmine has no open status")
	}
	return best, nil
}

// fieldDef is a custom field's definition: its format and whether it takes several values. Redmine lists the
// definitions to administrators only; for other keys the map is empty.
type fieldDef struct {
	Name     string
	Format   *string
	Multiple bool
	Values   []string
}

// customFieldDefs reads the instance's custom field definitions. A key that may not read them gets an empty map: each
// value still comes with its issue.
func customFieldDefs(ctx context.Context, c client) map[int]fieldDef {
	defs := map[int]fieldDef{}
	var d struct {
		CustomFields []struct {
			ID             int    `json:"id"`
			Name           string `json:"name"`
			FieldFormat    string `json:"field_format"`
			Multiple       bool   `json:"multiple"`
			PossibleValues []struct {
				Value string `json:"value"`
			} `json:"possible_values"`
		} `json:"custom_fields"`
	}
	if err := c.do(ctx, http.MethodGet, "/custom_fields.json", nil, "", nil, &d); err != nil {
		return defs
	}
	for _, f := range d.CustomFields {
		fd := fieldDef{Name: f.Name, Multiple: f.Multiple, Values: []string{}}
		if f.FieldFormat != "" {
			format := f.FieldFormat
			fd.Format = &format
		}
		for _, v := range f.PossibleValues {
			fd.Values = append(fd.Values, v.Value)
		}
		defs[f.ID] = fd
	}
	return defs
}

// listIssues reads the ids of the issues a key lists in a project, any status, newest-updated first, a page at a time.
// A page that is short, or that reaches total_count, is the last one. updatedSince, when set, is Redmine's updated_on
// filter. progress, when set, is told how many ids there are so far.
func listIssues(ctx context.Context, c client, projectID int, updatedSince string, progress func(done int, total *int)) ([]int, error) {
	var ids []int
	seen := map[int]bool{}
	for offset := 0; ; offset += pageSize {
		q := url.Values{}
		q.Set("project_id", strconv.Itoa(projectID))
		q.Set("status_id", "*")
		q.Set("sort", "updated_on:desc")
		q.Set("limit", strconv.Itoa(pageSize))
		q.Set("offset", strconv.Itoa(offset))
		if updatedSince != "" {
			q.Set("updated_on", updatedSince)
		}
		var page struct {
			Issues []struct {
				ID int `json:"id"`
			} `json:"issues"`
			TotalCount *int `json:"total_count"`
		}
		if err := c.do(ctx, http.MethodGet, "/issues.json", q, "", nil, &page); err != nil {
			return nil, err
		}
		// an issue updated while the list is read moves up, and the next page shows it again: listed once
		for _, it := range page.Issues {
			if !seen[it.ID] {
				seen[it.ID] = true
				ids = append(ids, it.ID)
			}
		}
		if progress != nil {
			progress(len(ids), page.TotalCount)
		}
		if len(page.Issues) < pageSize || (page.TotalCount != nil && offset+pageSize >= *page.TotalCount) {
			return ids, nil
		}
	}
}

// Include lists for a single issue read: the full record, and the lighter read the metadata refresh and a person's
// private-note check use.
const (
	fullInclude = "journals,relations,children,attachments,watchers"
	metaInclude = "relations,children,watchers"
)

// fetchIssue reads one issue with the given includes.
func fetchIssue(ctx context.Context, c client, id int, include string) (rmIssue, error) {
	var d struct {
		Issue rmIssue `json:"issue"`
	}
	err := c.do(ctx, http.MethodGet, "/issues/"+strconv.Itoa(id)+".json", url.Values{"include": {include}}, "", nil, &d)
	return d.Issue, err
}

// sortedKeys is the keys of a map, ascending.
func sortedKeys[V any](m map[int]V) []int {
	keys := make([]int, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	return keys
}

// rmTime is a timestamp in the form Redmine's filters take (UTC, to the second).
func rmTime(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05") + "Z"
}

// timeLayout is the form the stores keep sync times in (UTC, to the millisecond).
const timeLayout = "2006-01-02T15:04:05.000Z"

// sameOrigin is the client a request goes through: a copy of c (or a plain one) that refuses a redirect to another site,
// since the request carries a credential. The copy keeps c's transport and timeout.
func sameOrigin(c *http.Client) *http.Client {
	if c == nil {
		c = &http.Client{}
	}
	cp := *c
	cp.CheckRedirect = provider.SameOriginRedirects
	return &cp
}
