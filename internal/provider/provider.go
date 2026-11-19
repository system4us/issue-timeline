// Package provider is the contract between the hosts (github, gitlab, redmine, jira) and the server. Each host package
// returns a value that implements Host, and the optional interfaces it supports; the server asks with a type assertion
// (h.(provider.Editor)) and answers 501 when a host does not support something.
//
// Rules every host follows:
//   - Records in a store are never changed in place once stored: a change builds a new *model.Record (a copy) and
//     replaces the map entry under the store's Lock. Readers may then keep a pointer after RUnlock.
//   - No network call is made while holding a store lock.
//   - Every HTTP call has a context and a timeout, and retries 429 and 5xx a few times (Retry-After, at most 15 s).
//   - A path segment taken from a request (an issue number, a note id) is checked first (PathID).
package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"gitlab.com/system4us/issue-timeline/internal/model"
	"gitlab.com/system4us/issue-timeline/internal/store"
)

// ErrNotSupported is returned by an operation a host does not have.
var ErrNotSupported = errors.New("not supported by this host")

// ErrNotFound is returned when the host does not show the thing asked for (deleted, or the credential cannot see it).
var ErrNotFound = errors.New("not found on the host")

// Error is an error with the HTTP status the API should answer with (401 refused token, 403 refused change, 422
// rejected change, 502 anything else from the host).
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string { return e.Message }

// Errorf builds an *Error.
func Errorf(status int, format string, a ...any) error {
	return &Error{Status: status, Message: fmt.Sprintf(format, a...)}
}

var pathID = regexp.MustCompile(`^[1-9][0-9]{0,15}$`)

// PathID checks that v is a positive whole number before it goes in a URL path.
func PathID(v string, what string) (string, error) {
	if !pathID.MatchString(v) {
		return "", Errorf(400, "%s must be a positive number, not %q", what, v)
	}
	return v, nil
}

// SameOriginRedirects is the CheckRedirect of every client that sends a credential. A redirect to another scheme, host
// or port is refused, so the credential never reaches another site; a 307 or 308 would also replay a POST body there.
// The refusal is a *provider.Error with status 502 (it is wrapped in the *url.Error that Client.Do returns). Redirects
// on the same origin are followed as before, up to ten.
func SameOriginRedirects(req *http.Request, via []*http.Request) error {
	if len(via) == 0 {
		return nil
	}
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	from := via[0].URL
	if req.URL.Scheme != from.Scheme || !strings.EqualFold(req.URL.Host, from.Host) {
		return Errorf(502, "redirected to another site (%s); the request was not sent there", req.URL.Host)
	}
	return nil
}

// Progress is what a sync is doing, for the page.
type Progress struct {
	Phase string `json:"phase"`
	Done  int    `json:"done"`
	Total *int   `json:"total"`
}

type SyncOptions struct {
	Full     bool
	Log      func(string)
	Progress func(Progress)
	Now      func() time.Time
}

// Cred is who acts on the host: the server's own identity (from gh, glab or the stored secrets) or a signed-in person.
// Email is only for Jira (Basic auth with Email and Token).
type Cred struct {
	Token string
	Email string
}

// User is the account a credential belongs to.
type User struct {
	ID    int    `json:"id"`
	Login string `json:"login"`
	Name  string `json:"name"`
	Admin bool   `json:"admin"`
}

// Role is a person's role in a repo. "" is no role (not a member): the repo is not shown to them.
type Role string

const (
	RoleNone   Role = ""
	RoleViewer Role = "viewer"
	RoleEditor Role = "editor"
	RoleAdmin  Role = "admin"
)

// Changes is an edit as the API received it: the allowed field names, each with its JSON value. A host decodes the
// ones it supports and ignores the rest. See api/openapi.yaml (IssueChanges, PullChanges) for each name.
type Changes map[string]json.RawMessage

// Has reports whether the change carries name.
func (c Changes) Has(name string) bool { _, ok := c[name]; return ok }

// Get decodes one change into v; false when it is not there.
func (c Changes) Get(name string, v any) bool {
	raw, ok := c[name]
	return ok && json.Unmarshal(raw, v) == nil
}

// IsNull reports whether the change is there and is null (a field cleared).
func (c Changes) IsNull(name string) bool {
	raw, ok := c[name]
	return ok && string(raw) == "null"
}

// EditOptions is what a host's edit form offers. Every list is present (empty when the host has none).
type EditOptions struct {
	Trackers      []map[string]any `json:"trackers"`
	Priorities    []map[string]any `json:"priorities"`
	Members       []map[string]any `json:"members"`
	Versions      []map[string]any `json:"versions"`
	Categories    []map[string]any `json:"categories"`
	Activities    []map[string]any `json:"activities"`
	CustomFields  []map[string]any `json:"customFields"`
	RelationTypes []string         `json:"relationTypes"`
	Labels        []map[string]any `json:"labels"`
	Milestones    []map[string]any `json:"milestones"`
	IssueTypes    []map[string]any `json:"issueTypes"`
}

// Visibility is what one signed-in person may see of a repo, beyond having a role in it. The zero value hides nothing.
type Visibility struct {
	// Issues is the set of issue keys the person may see; nil means all of them.
	Issues map[string]bool
	// HiddenComments are the comment ids, per issue key, the person may not read (private or internal notes).
	HiddenComments map[string]map[string]bool
	// HideCode hides pulls, commits, diffs and CI (a GitLab Guest of a private project cannot read them).
	HideCode bool
}

// Host is every repo of every host.
type Host interface {
	// Key is the repo key ("owner/name", "gitlab:https://host/group/project", "redmine:https://host/ident",
	// "jira:https://site/PROJ").
	Key() string
	// Provider is "github", "gitlab", "redmine" or "jira".
	Provider() string
	// Origin is the site sessions are matched against: "github" for GitHub (one host per run), the site's origin otherwise.
	Origin() string
	// Exists reports whether the repo is there and the server's own credential can read it.
	Exists(ctx context.Context) error
	// Sync reads the repo into the store: in full when opt.Full or when the store was never read in full, otherwise what
	// changed since meta.lastSync. It updates meta (repo, lastSync, fullSyncAt, totals, syncs) and saves the store.
	Sync(ctx context.Context, st *store.Store, opt SyncOptions) (store.SyncEntry, error)
	// Refresh reads one record again ("issue" or "pull") and returns the updated copy (the fields the host gives now
	// merged onto rec). ErrNotFound when the host no longer shows it.
	Refresh(ctx context.Context, kind string, rec *model.Record) (*model.Record, error)
}

// MetaRefresher reads again, a batch per sync, the metadata that changes without a record's updatedAt (GitHub,
// Redmine). It returns how many were read and, when the host has a quota, what is left and when it resets.
type MetaRefresher interface {
	RefreshMeta(ctx context.Context, st *store.Store) (read int, remaining *int, resetAt *time.Time, err error)
}

// CodeHost is a host with code: commits, diffs, merge methods and CI (GitHub, GitLab).
type CodeHost interface {
	CommitPage(ctx context.Context, cursor string) (model.CommitPage, error)
	CommitFiles(ctx context.Context, sha string) (*model.CommitInfo, []model.HostFile, error)
	// MergeMethod is "squash", "merge commit" or "rebase" for a merged pull, "" when it cannot be told.
	MergeMethod(ctx context.Context, rec *model.Record) (string, error)
	CIRuns(ctx context.Context, since time.Time) ([]model.CIRun, error)
	WorkflowChanges(ctx context.Context, since time.Time) ([]model.WorkflowChange, error)
}

// Editor changes records on the host. Each returns the record the store should now keep (read back from the host and
// merged onto old), and warnings about values the host did not keep.
type Editor interface {
	EditIssue(ctx context.Context, cred Cred, old *model.Record, ch Changes) (*model.Record, []string, error)
	CreateIssue(ctx context.Context, cred Cred, ch Changes) (*model.Record, error)
	EditOptions(ctx context.Context, cred Cred) (EditOptions, error)
}

// PullEditor changes pull/merge requests (GitHub, GitLab).
type PullEditor interface {
	EditPull(ctx context.Context, cred Cred, old *model.Record, ch Changes) (*model.Record, error)
}

// Accounts is a host people sign in to (GitHub, GitLab, Redmine).
type Accounts interface {
	// WhoAmI is the account a token belongs to; a *Error with Status 401 when the host refuses it.
	WhoAmI(ctx context.Context, token string) (User, error)
	// RoleOf is the person's role in this repo, read with their own credential. RoleNone when not a member.
	RoleOf(ctx context.Context, cred Cred, user User) (Role, error)
	// VisibilityOf is what the person may see of the records (passed read-only), read with their own credential.
	VisibilityOf(ctx context.Context, cred Cred, user User, role Role, issues []*model.Record) (Visibility, error)
}

// AvailableRepo is a repository a signed-in user or local credential can add to the app.
type AvailableRepo struct {
	Provider string `json:"provider"`
	Origin   string `json:"origin"`
	Repo     string `json:"repo"`
	Name     string `json:"name"`
	Private  bool   `json:"private"`
}

// RepoLister is optionally implemented by hosts that can list repositories accessible to a token.
type RepoLister interface {
	ListRepos(ctx context.Context, token string) ([]AvailableRepo, error)
}
