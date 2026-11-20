// Package app is the server without HTTP: the configured repos, their hosts and stores, the links between trackers
// and code repos, the read/write modes, the sync scheduler, and every operation the routes need (read, edit, commits,
// access checks). internal/httpapi turns requests into calls of this package; the MCP server will call it too.
//
// This file is the contract between internal/app and internal/httpapi: the exported names and signatures here do not
// change without the coordinator. The bodies are implemented in the other files of the package.
package app

import (
	"context"
	"encoding/json"
	"time"

	"gitlab.com/system4us/issue-timeline/internal/auth"
	"gitlab.com/system4us/issue-timeline/internal/ci"
	"gitlab.com/system4us/issue-timeline/internal/commits"
	"gitlab.com/system4us/issue-timeline/internal/credentials"
	"gitlab.com/system4us/issue-timeline/internal/derive"
	"gitlab.com/system4us/issue-timeline/internal/model"
	"gitlab.com/system4us/issue-timeline/internal/provider"
	"gitlab.com/system4us/issue-timeline/internal/store"
)

// Config is what the server starts with.
type Config struct {
	StoreDir     string              // the data folder (cli.DefaultStoreDir)
	PollInterval time.Duration       // POLL_MINUTES (default 5 minutes)
	Env          func(string) string // os.Getenv in production
	Secrets      *credentials.Store  // the tracker secrets of StoreDir
	Log          func(format string, a ...any)
	// Hosts builds the host of a repo key. Nil means the real hosts (github, gitlab, redmine, jira) with the server's
	// own credentials; tests pass fakes.
	Hosts func(repoKey string) (provider.Host, error)
	// OwnToken is the server's own token for a repo's host (a request from this machine edits with it). Nil means the
	// real sources (GITHUB_TOKEN or gh, GITLAB_TOKEN or glab, the stored tracker secrets); tests pass a fake, so no test
	// runs gh or reads this machine's accounts.
	OwnToken func(ctx context.Context, repo string) (string, error)
}

// Status is a repo's sync state, as /api/data serves it.
type Status struct {
	Syncing      bool               `json:"syncing"`
	LastError    *string            `json:"lastError"`
	NextSyncAt   *string            `json:"nextSyncAt"`
	Progress     *provider.Progress `json:"progress"`
	CommitsError *string            `json:"commitsError"`
	CIError      *string            `json:"ciError"`
}

// Entry is one configured repo.
type Entry struct {
	Repo    string
	Host    provider.Host
	Store   *store.Store
	Commits *commits.Store // nil for a tracker
	CI      *ci.Store      // nil for a tracker
}

// Data is what /api/data serves for one repo, before visibility is applied by the caller (see ApplyVisibility).
type Data struct {
	derive.Payload
	Linked []string `json:"linked"`
	Mode   string   `json:"mode"`
	CI     CI       `json:"ci"`
	Status Status   `json:"status"`
}

// CI is the CI summary of a repo with the commits that changed its definition.
type CI struct {
	ci.Summary
	Changes []model.WorkflowChange `json:"changes"`
}

// CommitQuery is the filter of the commit list (all optional).
type CommitQuery struct {
	Q, Author, PR, Issue string
	Limit, Offset        int
}

// CommitRow is one commit of the list: the stored commit, its pull and issues, and whether it is on the default branch.
type CommitRow struct {
	model.Commit
	Repo      string   `json:"repo,omitempty"`
	PR        *string  `json:"pr"`
	Issues    []string `json:"issues"`
	InHistory bool     `json:"inHistory"`
}

type CommitList struct {
	Total    int         `json:"total"`
	Synced   int         `json:"synced"`
	LastSync *string     `json:"lastSync"`
	Items    []CommitRow `json:"items"`
}

// CommitDetail is one commit with its files (the diff cache, filled from the host the first time).
type CommitDetail struct {
	CommitRow
	Files     []commits.File `json:"files"`
	Truncated bool           `json:"truncated"`
}

// Refreshed is the answer of a one-record refresh.
type Refreshed struct {
	Record    *model.Record `json:"record"`
	Changed   bool          `json:"changed"`
	Fields    []string      `json:"fields,omitempty"`
	Throttled bool          `json:"throttled,omitempty"`
}

// Who is the person asking: nil Session is this machine's own identity (a local request without a session).
type Who struct {
	Session *auth.Session
	Local   bool
}

// App is the running server's state. Every method is safe for concurrent use.
type App struct {
	impl *state
}

// New loads repos.json, links.json, repo-modes.json, roles.json and the vocabulary (VOCABULARY_FILE) from the data
// folder, builds each repo's host and opens its stores. It does not start syncing (see Start).
func New(cfg Config) (*App, error) { return newApp(cfg) }

// Start syncs every repo that is stale, then every PollInterval, until ctx ends.
func (a *App) Start(ctx context.Context) { a.impl.start(ctx) }

// ---- repos, modes, links

func (a *App) Repos() []string                              { return a.impl.repos() }
func (a *App) Entry(repo string) (*Entry, bool)             { return a.impl.entry(repo) }
func (a *App) Mode(repo string) string                      { return a.impl.mode(repo) }
func (a *App) SetMode(repo, mode string) error              { return a.impl.setMode(repo, mode) }
func (a *App) Links() map[string][]string                   { return a.impl.links() }
func (a *App) SetLinks(tracker string, code []string) error { return a.impl.setLinks(tracker, code) }

// AddRepo checks the repo on its host with the server's own credentials and adds it (provider "github", "gitlab",
// "redmine", "jira"; input as the web form sends it). added is false when it was already configured (its mode is then
// set when mode is not "").
func (a *App) AddRepo(ctx context.Context, providerName, input, mode string) (repo string, added bool, err error) {
	return a.impl.addRepo(ctx, providerName, input, mode)
}

// RemoveRepo unconfigures repo and cleans any links referencing it, returning the remaining configured repos.
func (a *App) RemoveRepo(repo string) ([]string, error) {
	return a.impl.removeRepo(repo)
}

// RunSync starts a sync of repo in the background; false when one is already running.
func (a *App) RunSync(repo string, full bool) bool { return a.impl.runSync(repo, full) }

// Stop cancels the syncs waiting to start (asked for by edits and webhooks) and ends the event streams. Call it when the
// server stops; syncs already running finish on their own, and none starts after it.
func (a *App) Stop() { a.impl.stop() }

// Subscribe follows what changes in repo (edits, syncs), for one open page; the function ends it. The channel closes when
// the server stops.
func (a *App) Subscribe(repo string) (<-chan Event, func()) { return a.impl.events.subscribe(repo) }

// HookReceived takes a webhook delivery about the repos it names: each one configured here is synced soon, and the
// periodic poll leaves it alone for a while. It returns the repos it matched.
func (a *App) HookReceived(repos []string) []string { return a.impl.hookReceived(repos) }

// ---- who may do what

// SignInOrigins are the sites people may sign in to for a provider: those of the configured repos of that provider.
func (a *App) SignInOrigins(providerName string) []string { return a.impl.signInOrigins(providerName) }

// Accounts is a host to check a sign-in against (WhoAmI), for a provider and one of its SignInOrigins.
func (a *App) Accounts(providerName, origin string) (provider.Accounts, bool) {
	return a.impl.accounts(providerName, origin)
}

// WhoAmI checks a token against a provider and origin: using a configured repo's host when one exists, or a host
// client for GitHub or GitLab when no repo on that origin is configured yet.
func (a *App) WhoAmI(ctx context.Context, providerName, origin, token string) (provider.User, error) {
	return a.impl.whoAmI(ctx, providerName, origin, token)
}

// ListAvailableRepos lists repositories accessible to token on (providerName, origin).
func (a *App) ListAvailableRepos(ctx context.Context, providerName, origin, token string) ([]provider.AvailableRepo, error) {
	return a.impl.listAvailableRepos(ctx, providerName, origin, token)
}

// ServerToken returns the server's own token for (providerName, origin), used for local callers without a session.
func (a *App) ServerToken(ctx context.Context, providerName, origin string) (string, error) {
	return a.impl.serverToken(ctx, providerName, origin)
}

// RepoOrigin returns the host origin of a repository input for providerName ("github", or the GitLab instance URL).
func RepoOrigin(providerName, input string) (string, error) {
	return repoOrigin(providerName, input)
}

// WithToken attaches a caller's token to ctx for AddRepo when the server has no own token configured for that host.
func WithToken(ctx context.Context, token string) context.Context {
	return withSessionToken(ctx, token)
}

// SetFallbackToken registers a callback that returns an in-memory session token for (providerName, origin) when the
// server has no own token configured.
func (a *App) SetFallbackToken(fn func(ctx context.Context, providerName, origin string) (string, error)) {
	a.impl.setFallbackToken(fn)
}

// RoleIn is the role of who in repo: RoleAdmin for this machine's identity; otherwise read from the host with the
// session's token (cached in the session).
func (a *App) RoleIn(ctx context.Context, repo string, who Who) (provider.Role, error) {
	return a.impl.roleIn(ctx, repo, who)
}

// VisibleRepos are the repos who may see.
func (a *App) VisibleRepos(ctx context.Context, who Who) []string {
	return a.impl.visibleRepos(ctx, who)
}

// VisibleLinked are the code repos linked to a tracker that who may see.
func (a *App) VisibleLinked(ctx context.Context, repo string, who Who) []string {
	return a.impl.visibleLinked(ctx, repo, who)
}

// Visibility is what who may see inside repo (cached in the session 5 minutes); the zero value for this machine.
func (a *App) Visibility(ctx context.Context, repo string, who Who) (provider.Visibility, error) {
	return a.impl.visibility(ctx, repo, who)
}

// Writer is the credential an edit of repo is made with: the session's token when its role is editor or admin there,
// the server's own credential for this machine. A non-empty refusal is the reason it may not (a 403).
func (a *App) Writer(ctx context.Context, repo string, who Who) (cred provider.Cred, refusal string, err error) {
	return a.impl.writer(ctx, repo, who)
}

// ---- reading

// Data is the payload of repo for /api/data, with the linked code repos given (VisibleLinked).
func (a *App) Data(repo string, linked []string) (Data, error) { return a.impl.data(repo, linked) }

// Search searches repo's issues and pulls (and its linked pulls), with what v allows applied to the records BEFORE
// searching, so neither a hidden issue nor a hidden note can match or show up in a snippet. Pass the zero Visibility
// for this machine's identity.
func (a *App) Search(repo string, linked []string, q string, v provider.Visibility) []derive.SearchResult {
	return a.impl.search(repo, linked, q, v)
}

// Record is one issue or pull ("issue" | "pull"); a pull key may be "label#N" of a linked code repo.
func (a *App) Record(repo string, linked []string, kind, key string) (*model.Record, bool) {
	return a.impl.record(repo, linked, kind, key)
}

// Refresh reads one record again from its host (throttled per record).
func (a *App) Refresh(ctx context.Context, repo, kind, key string) (Refreshed, error) {
	return a.impl.refresh(ctx, repo, kind, key)
}

func (a *App) Commits(repo string, linked []string, q CommitQuery) (CommitList, error) {
	return a.impl.commits(repo, linked, q)
}

func (a *App) Commit(ctx context.Context, repo string, linked []string, sha string) (CommitDetail, error) {
	return a.impl.commit(ctx, repo, linked, sha)
}

// MergeMethod of a merged pull ("" when not merged or unknown), cached in memory.
func (a *App) MergeMethod(ctx context.Context, repo string, linked []string, key string) (string, error) {
	return a.impl.mergeMethod(ctx, repo, linked, key)
}

// EditOptions of repo's edit form, read with cred (cached 5 minutes when cached is true: the server's own identity).
func (a *App) EditOptions(ctx context.Context, repo string, cred provider.Cred, cached bool) (provider.EditOptions, error) {
	return a.impl.editOptions(ctx, repo, cred, cached)
}

// Views are a repo's saved views (named filters, grouping and layout), oldest first.
func (a *App) Views(repo string) []View { return a.impl.views.list(repo) }

// SaveView saves a view for owner (LocalOwner on the server's own machine): a new name adds one, the owner's own name
// replaces its state, and another person's name is a 409.
func (a *App) SaveView(repo, owner, name string, state json.RawMessage) (View, error) {
	return a.impl.views.put(repo, owner, name, state)
}

// DeleteView removes a view: its owner may, and so may the local machine.
func (a *App) DeleteView(repo, id, owner string, local bool) error {
	return a.impl.views.remove(repo, id, owner, local)
}

// EntityKinds are the kinds of thing a repo lets a person create (labels, milestones...), nil when none.
func (a *App) EntityKinds(repo string) []string { return a.impl.entityKinds(repo) }

// CreateEntity creates a label, milestone, version or category on the host (the caller has checked the mode and the Writer)
// and returns it the way EditOptions lists it.
func (a *App) CreateEntity(ctx context.Context, repo string, cred provider.Cred, e provider.NewEntity) (map[string]any, error) {
	return a.impl.createEntity(ctx, repo, cred, e)
}

// ---- writing (the caller has checked the mode and the Writer)

// EditIssue changes an issue on its host, applies the links and notes it changed to the stored records, keeps the
// version date when the milestone is the same, saves, and returns the record kept and the host's warnings.
func (a *App) EditIssue(ctx context.Context, repo string, cred provider.Cred, key string, ch provider.Changes) (*model.Record, []string, error) {
	return a.impl.editIssue(ctx, repo, cred, key, ch)
}

func (a *App) CreateIssue(ctx context.Context, repo string, cred provider.Cred, ch provider.Changes) (*model.Record, error) {
	return a.impl.createIssue(ctx, repo, cred, ch)
}

func (a *App) EditPull(ctx context.Context, repo string, cred provider.Cred, key string, ch provider.Changes) (*model.Record, error) {
	return a.impl.editPull(ctx, repo, cred, key, ch)
}

// ApplyVisibility filters data, search results and records for what v allows. It never changes stored records: it
// returns copies.
func ApplyVisibility(d Data, v provider.Visibility) Data { return applyVisibilityData(d, v) }

func VisibleRecord(r *model.Record, v provider.Visibility) (*model.Record, bool) {
	return visibleRecord(r, v)
}

func VisibleResults(rs []derive.SearchResult, v provider.Visibility) []derive.SearchResult {
	return visibleResults(rs, v)
}

func VisibleCommits(l CommitList, v provider.Visibility) CommitList { return visibleCommits(l, v) }

// VisibleCommit is one commit's detail as v allows: false when code is hidden (answer 404), and only visible issues.
func VisibleCommit(d CommitDetail, v provider.Visibility) (CommitDetail, bool) {
	return visibleCommit(d, v)
}
