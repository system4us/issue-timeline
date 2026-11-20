package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"gitlab.com/system4us/issue-timeline/internal/auth"
	"gitlab.com/system4us/issue-timeline/internal/ci"
	"gitlab.com/system4us/issue-timeline/internal/commits"
	"gitlab.com/system4us/issue-timeline/internal/credentials"
	"gitlab.com/system4us/issue-timeline/internal/derive"
	"gitlab.com/system4us/issue-timeline/internal/github"
	"gitlab.com/system4us/issue-timeline/internal/gitlab"
	"gitlab.com/system4us/issue-timeline/internal/jira"
	"gitlab.com/system4us/issue-timeline/internal/model"
	"gitlab.com/system4us/issue-timeline/internal/provider"
	"gitlab.com/system4us/issue-timeline/internal/redmine"
	"gitlab.com/system4us/issue-timeline/internal/store"
)

// state is the server without HTTP. The repo list, the entries and the links are guarded by mu; each entry's sync
// status by its own mutex; the caches by their own mutexes. Store locks are taken only around in-memory work.
type state struct {
	cfg        Config
	env        func(string) string
	storeDir   string
	reposFile  string
	linksFile  string
	vocab      derive.Vocabulary
	roles      auth.RoleMapping
	modes      *repoModes
	views      *viewStore
	secrets    *credentials.Store
	interval   time.Duration
	throttle   time.Duration
	metaFloor  int
	ghToken    func(context.Context) (string, error)
	glToken    func(context.Context, string) (string, error)
	categorize func(model.CIRun) string

	mu      sync.RWMutex
	order   []string
	entries map[string]*entryState
	linked  map[string][]string // tracker key -> code repo keys
	root    context.Context     // syncs run until it ends
	events  hub                 // what changed, for the pages open on each repo (GET /api/events)
	stopped bool                // Stop was called: no sync starts any more
	// soonWait replaces soonDelay when set (tests).
	soonWait time.Duration

	saveMu      sync.Mutex // repos.json and links.json: one write at a time
	mergeMu     sync.Mutex
	merges      map[string]string // "codeRepo#number" -> how the PR was merged
	optsMu      sync.Mutex
	opts        map[string]editOptsHit
	fallbackMu  sync.RWMutex
	fallbackTok func(ctx context.Context, providerName, origin string) (string, error)
}

// entryState is one configured repo at run time: its stores and host (Entry) and its sync status.
type entryState struct {
	Entry
	mu       sync.Mutex
	status   Status
	metaSkip time.Time   // GitHub's quota is low: no metadata reads before this
	soon     *time.Timer // a sync asked for by an edit or a webhook, waiting to start (syncSoon)
	again    bool        // a sync was asked for while one ran: another one follows it
	lastHook time.Time   // the last webhook delivery for this repo (the poll leaves it alone for a while after one)
}

type editOptsHit struct {
	at    time.Time
	value provider.EditOptions
}

var githubRepoKey = regexp.MustCompile(`^[\w.-]+/[\w.-]+$`)

func newApp(cfg Config) (*App, error) {
	if cfg.Env == nil {
		cfg.Env = os.Getenv
	}
	if cfg.Log == nil {
		cfg.Log = func(string, ...any) {}
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 5 * time.Minute
	}
	vocab, err := derive.LoadVocabulary(cfg.Env("VOCABULARY_FILE"))
	if err != nil {
		return nil, fmt.Errorf("vocabulary: %w", err)
	}
	s := &state{
		cfg:        cfg,
		env:        cfg.Env,
		storeDir:   cfg.StoreDir,
		reposFile:  filepath.Join(cfg.StoreDir, "repos.json"),
		linksFile:  filepath.Join(cfg.StoreDir, "links.json"),
		vocab:      vocab,
		roles:      auth.LoadRoleMapping(filepath.Join(cfg.StoreDir, "roles.json")),
		modes:      loadModes(filepath.Join(cfg.StoreDir, "repo-modes.json")),
		views:      loadViews(filepath.Join(cfg.StoreDir, "views.json")),
		secrets:    cfg.Secrets,
		interval:   cfg.PollInterval,
		throttle:   time.Duration(envNumber(cfg.Env, "REREAD_THROTTLE_MS", 60000)) * time.Millisecond,
		metaFloor:  envNumber(cfg.Env, "META_RATE_FLOOR", 300),
		ghToken:    github.TokenSource(),
		glToken:    gitlab.TokenSource(),
		categorize: categorizer(vocab),
		entries:    map[string]*entryState{},
		linked:     map[string][]string{},
		root:       context.Background(),
		merges:     map[string]string{},
		opts:       map[string]editOptsHit{},
	}
	s.order = s.loadRepos()
	s.loadLinks()
	for _, repo := range s.order {
		host, err := s.buildHost(repo)
		if err != nil {
			return nil, fmt.Errorf("repo %s: %w", repo, err)
		}
		// STORE_FILE names the store of the only repo (as the Node server did)
		file := s.storeFileFor(repo)
		if s.env("STORE_FILE") != "" && len(s.order) == 1 {
			file = s.env("STORE_FILE")
		}
		es, err := openEntry(repo, host, file)
		if err != nil {
			return nil, fmt.Errorf("repo %s: %w", repo, err)
		}
		s.entries[repo] = es
	}
	return &App{impl: s}, nil
}

// envNumber reads a positive whole number from the environment, or def.
func envNumber(env func(string) string, name string, def int) int {
	if n, err := strconv.Atoi(env(name)); err == nil && n > 0 {
		return n
	}
	return def
}

// openEntry opens a repo's store, and for a code repo its commits and CI files next to it.
func openEntry(repo string, host provider.Host, file string) (*entryState, error) {
	st, err := store.Open(file)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", file, err)
	}
	es := &entryState{Entry: Entry{Repo: repo, Host: host, Store: st}}
	if isTracker(repo) {
		return es, nil
	}
	cs, err := commits.Open(commitsFileOf(file))
	if err != nil {
		return nil, fmt.Errorf("reading commits of %s: %w", repo, err)
	}
	cz, err := ci.Open(ciFileOf(file))
	if err != nil {
		return nil, fmt.Errorf("reading CI of %s: %w", repo, err)
	}
	es.Commits = cs
	es.CI = cz
	return es, nil
}

// storeFileFor is the store file of a repo, as the Node server named it: the repo's name with "/" replaced, or the
// provider prefix and the first 16 hex digits of the key's sha256 for the other hosts.
func (s *state) storeFileFor(repo string) string {
	if isTracker(repo) || providerOf(repo) == "gitlab" {
		sum := sha256.Sum256([]byte(repo))
		return filepath.Join(s.storeDir, providerOf(repo)+"-"+hex.EncodeToString(sum[:])[:16]+".json")
	}
	return filepath.Join(s.storeDir, strings.Replace(repo, "/", "__", 1)+".json")
}

// commitsFileOf is <store>.commits.json; ciFileOf is <store>.ci.json.
func commitsFileOf(storeFile string) string {
	return strings.TrimSuffix(storeFile, ".json") + ".commits.json"
}

func ciFileOf(storeFile string) string {
	return strings.TrimSuffix(storeFile, ".json") + ".ci.json"
}

// providerOf is the host a repo key belongs to. A GitHub repo key is owner/name; the others carry their prefix.
func providerOf(repo string) string {
	switch {
	case strings.HasPrefix(repo, "redmine:"):
		return "redmine"
	case strings.HasPrefix(repo, "jira:"):
		return "jira"
	case strings.HasPrefix(repo, "gitlab:"):
		return "gitlab"
	}
	return "github"
}

// isTracker is true for the trackers (Redmine, Jira): no commits, no CI, no pull requests of their own.
func isTracker(repo string) bool {
	p := providerOf(repo)
	return p == "redmine" || p == "jira"
}

func isCodeRepo(repo string) bool { return !isTracker(repo) }

// validRepoKey is true for a key the host packages can parse.
func validRepoKey(r string) bool {
	switch providerOf(r) {
	case "gitlab":
		_, _, _, _, err := gitlab.ParseRepo(r)
		return err == nil
	case "redmine":
		_, _, err := redmine.ParseRepo(r)
		return err == nil
	case "jira":
		_, _, err := jira.ParseRepo(r)
		return err == nil
	}
	return githubRepoKey.MatchString(r)
}

// codeLabel is the name a code repo has in a tracker's view: its own name ("group/project" for GitLab), which
// prefixes its pulls ("label#N").
func codeLabel(repo string) string {
	if strings.HasPrefix(repo, "gitlab:") {
		u, err := url.Parse(strings.TrimPrefix(repo, "gitlab:"))
		if err != nil {
			return repo
		}
		return strings.TrimPrefix(u.EscapedPath(), "/")
	}
	return repo
}

// loadRepos reads repos.json: the valid keys, once each, in the file's order.
func (s *state) loadRepos() []string {
	var raw []any
	_ = readJSONFile(s.reposFile, &raw)
	seen := map[string]bool{}
	out := []string{}
	for _, x := range raw {
		r, ok := x.(string)
		if !ok || seen[r] || !validRepoKey(r) {
			continue
		}
		seen[r] = true
		out = append(out, r)
	}
	return out
}

// loadLinks reads links.json: a tracker's code repos, when both are configured and of the right kind.
func (s *state) loadLinks() {
	var raw map[string]any
	_ = readJSONFile(s.linksFile, &raw)
	configured := map[string]bool{}
	for _, r := range s.order {
		configured[r] = true
	}
	for tracker, v := range raw {
		list, _ := v.([]any)
		if !configured[tracker] || isCodeRepo(tracker) {
			continue
		}
		codes := []string{}
		for _, x := range list {
			if c, ok := x.(string); ok && configured[c] && isCodeRepo(c) {
				codes = append(codes, c)
			}
		}
		s.linked[tracker] = codes
	}
}

func readJSONFile(file string, v any) error {
	b, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// buildHost is the host of a repo key: the one Config.Hosts gives, or the real one with the server's own credentials.
// A plain secret may be bound to the site by this host's reads (the sync of a configured repo).
func (s *state) buildHost(repo string) (provider.Host, error) {
	return s.buildHostFor(repo, false)
}

// buildHostFor is buildHost; with siteOnly the Redmine key and the Jira credentials are only the ones set for the site
// (see credentials.Store.SiteSecret), which is how a new site is checked when a repo is added.
func (s *state) buildHostFor(repo string, siteOnly bool) (provider.Host, error) {
	if s.cfg.Hosts != nil {
		return s.cfg.Hosts(repo)
	}
	switch providerOf(repo) {
	case "gitlab":
		origin, _, _, _, err := gitlab.ParseRepo(repo)
		if err != nil {
			return nil, err
		}
		r, err := gitlab.New(repo, gitlab.Options{Token: s.glTokenForOrigin(origin), Categorize: s.categorize})
		if err != nil {
			return nil, err
		}
		return r, nil
	case "redmine":
		origin, _, err := redmine.ParseRepo(repo)
		if err != nil {
			return nil, err
		}
		r, err := redmine.New(repo, redmine.Options{Key: s.redmineKeyFunc(origin, siteOnly), EditorRoles: s.roles.Editor})
		if err != nil {
			return nil, err
		}
		return r, nil
	case "jira":
		origin, _, err := jira.ParseRepo(repo)
		if err != nil {
			return nil, err
		}
		r, err := jira.New(repo, jira.Options{Creds: s.jiraCredsFunc(origin, siteOnly)})
		if err != nil {
			return nil, err
		}
		return r, nil
	}
	owner, name, ok := strings.Cut(repo, "/")
	if !ok {
		return nil, fmt.Errorf("not a GitHub repo: %s", repo)
	}
	return github.New(owner, name, github.Options{
		Token:         s.ghTokenWithFallback(),
		Endpoints:     github.EndpointsFromEnv(),
		ClassifyMerge: classifyMerge,
		Categorize:    s.categorize,
	}), nil
}

// classifyMerge adapts commits.ClassifyGitHubMerge to the GitHub host's option.
func classifyMerge(parents int, title, prTitle string, number int, prCommitTitles []string) string {
	return commits.ClassifyGitHubMerge(commits.MergeFacts{
		Parents: parents, Title: title, PRTitle: prTitle, Number: number, PRCommitTitles: prCommitTitles,
	})
}

// categorizer is ci.Categorize, with the vocabulary's other workflows when it names some.
func categorizer(v derive.Vocabulary) func(model.CIRun) string {
	if len(v.CIOtherWorkflows) == 0 {
		return ci.Categorize
	}
	re := ci.OtherWorkflowsRe(v.CIOtherWorkflows)
	return func(r model.CIRun) string { return ci.CategorizeWith(r, re) }
}

// secretOf is the secret named for a site; "" when it is not set for it. With siteOnly only the per-site secret counts
// and nothing is bound (credentials.Store.SiteSecret).
func (s *state) secretOf(name, origin string, siteOnly bool) (string, error) {
	if s.secrets == nil {
		return "", nil
	}
	var value string
	var err error
	if siteOnly {
		value, err = s.secrets.SiteSecret(name, origin)
	} else {
		value, _, err = s.secrets.SecretFor(name, origin)
	}
	if errors.Is(err, credentials.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return value, nil
}

// redmineKeyFunc is the server's Redmine key for one site (bound to it: credentials.SecretFor), or the site's own key
// only when siteOnly.
func (s *state) redmineKeyFunc(origin string, siteOnly bool) func(context.Context) (string, error) {
	return func(context.Context) (string, error) {
		key, err := s.secretOf("redmine-api-key", origin, siteOnly)
		if err != nil {
			return "", err
		}
		if key == "" {
			msg := fmt.Sprintf("No Redmine API key for %s: run `issue-timeline credentials set redmine-api-key %s`", origin, origin)
			if siteOnly {
				msg += s.plainBelongsElsewhere("redmine-api-key", origin)
			}
			return "", errors.New(msg)
		}
		return key, nil
	}
}

// plainBelongsElsewhere says where a plain secret is bound when that is another site than origin, so the message that
// asks for the site's own secret can also say why the plain one was not used ("" when there is nothing to say).
func (s *state) plainBelongsElsewhere(name, origin string) string {
	if s.secrets == nil {
		return ""
	}
	bound, err := s.secrets.BoundOrigins()
	if err != nil || bound[name] == "" || bound[name] == origin {
		return ""
	}
	return fmt.Sprintf(" The plain %s that is set belongs to %s, not to %s, and is not used for a new site.", name, bound[name], origin)
}

// jiraCredsFunc is the server's Jira email and token for one site.
func (s *state) jiraCredsFunc(origin string, siteOnly bool) func(context.Context) (string, string, error) {
	return func(context.Context) (string, string, error) {
		email, err := s.secretOf("jira-email", origin, siteOnly)
		if err != nil {
			return "", "", err
		}
		token, err := s.secretOf("jira-api-token", origin, siteOnly)
		if err != nil {
			return "", "", err
		}
		if email == "" || token == "" {
			return "", "", fmt.Errorf("No Jira credentials for %s: run `issue-timeline credentials set jira-email %s` and `issue-timeline credentials set jira-api-token %s`", origin, origin, origin)
		}
		return email, token, nil
	}
}

// ownToken is the server's own token for a repo's host (the identity of a request from this machine).
func (s *state) ownToken(ctx context.Context, repo string) (string, error) {
	if s.cfg.OwnToken != nil {
		return s.cfg.OwnToken(ctx, repo)
	}
	switch providerOf(repo) {
	case "github":
		return s.ghToken(ctx)
	case "gitlab":
		_, host, _, _, err := gitlab.ParseRepo(repo)
		if err != nil {
			return "", err
		}
		return s.glToken(ctx, host)
	case "redmine":
		origin, _, err := redmine.ParseRepo(repo)
		if err != nil {
			return "", err
		}
		return s.redmineKeyFunc(origin, false)(ctx)
	}
	return "", provider.Errorf(403, "editing Jira issues is not built yet")
}

// get is a configured repo's entry.
func (s *state) get(repo string) (*entryState, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	es, ok := s.entries[repo]
	return es, ok
}

// configured reports whether a repo key is in the list.
func (s *state) configured(repo string) bool {
	_, ok := s.get(repo)
	return ok
}

// statusCopy is a copy of the entry's sync status.
func (es *entryState) statusCopy() Status {
	es.mu.Lock()
	defer es.mu.Unlock()
	return es.status
}

// update changes the entry's sync status under its mutex.
func (es *entryState) update(fn func(*Status)) {
	es.mu.Lock()
	defer es.mu.Unlock()
	fn(&es.status)
}

func (s *state) hostClient(providerName, origin string) (any, error) {
	if s.cfg.Hosts != nil {
		key := "owner/repo"
		if providerName == "gitlab" {
			key = "gitlab:" + origin + "/project"
		}
		if h, err := s.cfg.Hosts(key); err == nil && h.Provider() == providerName && h.Origin() == origin {
			return h, nil
		}
	}
	switch providerName {
	case "github":
		return github.New("", "", github.Options{Endpoints: github.EndpointsFrom(s.env)}), nil
	case "gitlab":
		return gitlab.New("gitlab:"+origin+"/project", gitlab.Options{Categorize: s.categorize})
	}
	return nil, fmt.Errorf("not supported for %s", providerName)
}

func (s *state) whoAmI(ctx context.Context, providerName, origin, token string) (provider.User, error) {
	if acc, ok := s.accounts(providerName, origin); ok {
		return acc.WhoAmI(ctx, token)
	}
	h, err := s.hostClient(providerName, origin)
	if err != nil {
		return provider.User{}, fmt.Errorf("sign-in is not available for %s without a configured repo", providerName)
	}
	if acc, ok := h.(provider.Accounts); ok {
		return acc.WhoAmI(ctx, token)
	}
	return provider.User{}, fmt.Errorf("sign-in is not available for %s without a configured repo", providerName)
}

func (s *state) listAvailableRepos(ctx context.Context, providerName, origin, token string) ([]provider.AvailableRepo, error) {
	if acc, ok := s.accounts(providerName, origin); ok {
		if lister, ok := acc.(provider.RepoLister); ok {
			return lister.ListRepos(ctx, token)
		}
	}
	h, err := s.hostClient(providerName, origin)
	if err != nil {
		return nil, fmt.Errorf("listing repositories is not available for %s", providerName)
	}
	if lister, ok := h.(provider.RepoLister); ok {
		return lister.ListRepos(ctx, token)
	}
	return nil, fmt.Errorf("listing repositories is not available for %s", providerName)
}

func (s *state) serverToken(ctx context.Context, providerName, origin string) (string, error) {
	if s.cfg.OwnToken != nil {
		key := "owner/repo"
		if providerName == "gitlab" {
			key = "gitlab:" + origin + "/project"
		}
		return s.cfg.OwnToken(ctx, key)
	}
	switch providerName {
	case "github":
		return s.ghToken(ctx)
	case "gitlab":
		_, host, _, _, err := gitlab.ParseRepo("gitlab:" + origin + "/project")
		if err != nil {
			return "", err
		}
		return s.glToken(ctx, host)
	}
	return "", fmt.Errorf("no server token for %s", providerName)
}

type sessionTokenKey struct{}

func withSessionToken(ctx context.Context, token string) context.Context {
	if token == "" {
		return ctx
	}
	return context.WithValue(ctx, sessionTokenKey{}, token)
}

func sessionTokenFrom(ctx context.Context) string {
	tok, _ := ctx.Value(sessionTokenKey{}).(string)
	return tok
}

func (s *state) setFallbackToken(fn func(ctx context.Context, providerName, origin string) (string, error)) {
	s.fallbackMu.Lock()
	defer s.fallbackMu.Unlock()
	s.fallbackTok = fn
}

func (s *state) getFallbackToken() func(ctx context.Context, providerName, origin string) (string, error) {
	s.fallbackMu.RLock()
	defer s.fallbackMu.RUnlock()
	return s.fallbackTok
}

func (s *state) resolveToken(ctx context.Context, providerName, origin string, primary func() (string, error)) (string, error) {
	tok, err := primary()
	if err == nil {
		return tok, nil
	}
	if ctxTok := sessionTokenFrom(ctx); ctxTok != "" {
		return ctxTok, nil
	}
	if fb := s.getFallbackToken(); fb != nil {
		if fbTok, fbErr := fb(ctx, providerName, origin); fbErr == nil && fbTok != "" {
			return fbTok, nil
		}
	}
	return "", err
}

func (s *state) ghTokenWithFallback() func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		return s.resolveToken(ctx, "github", "github", func() (string, error) { return s.ghToken(ctx) })
	}
}

func (s *state) glTokenForOrigin(origin string) func(context.Context, string) (string, error) {
	return func(ctx context.Context, host string) (string, error) {
		return s.resolveToken(ctx, "gitlab", origin, func() (string, error) { return s.glToken(ctx, host) })
	}
}

func repoOrigin(providerName, input string) (string, error) {
	switch providerName {
	case "", "github":
		return "github", nil
	case "gitlab":
		origin, _, _, _, err := gitlab.ParseRepo(strings.TrimSpace(input))
		return origin, err
	}
	return "", fmt.Errorf("Unsupported provider")
}

func strPtr(v string) *string { return &v }
