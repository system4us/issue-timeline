package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"gitlab.com/system4us/issue-timeline/api"
	"gitlab.com/system4us/issue-timeline/internal/app"
	"gitlab.com/system4us/issue-timeline/internal/auth"
	"gitlab.com/system4us/issue-timeline/internal/model"
	"gitlab.com/system4us/issue-timeline/internal/provider"
)

// The fields each edit takes. A host reads the ones it supports and ignores the rest (see api/openapi.yaml).
var (
	issueFields    = []string{"title", "body", "dueDate", "startDate", "state", "comment", "customFields", "tracker", "priority", "assignee", "version", "category", "doneRatio", "estimatedHours", "parent", "addRelations", "removeRelations", "labels", "assignees", "milestone", "addLinks", "removeLinks", "confidential", "weight", "timeEstimate", "addSpent", "editComments", "deleteComments", "stateReason", "locked", "type", "privateNote", "isPrivate", "addWatchers", "removeWatchers", "timeEntry", "attachments", "removeAttachments"}
	pullFields     = []string{"title", "body", "state", "labels", "assignees", "milestone", "reviewers", "isDraft", "comment"}
	newIssueFields = []string{"title", "body", "labels", "assignees", "milestone", "dueDate", "startDate", "tracker", "priority", "assignee", "version", "category", "parent", "isPrivate"}
	// relationTypes are the relations Redmine takes (RelationInput in the spec).
	relationTypes = []string{"relates", "duplicates", "duplicated", "blocks", "blocked", "precedes", "follows", "copied_to", "copied_from"}
)

const (
	msgReadOnly   = "this repo is read-only: switch it to write first"
	msgNotConfig  = "repo not configured"
	msgNotFound   = "not found"
	msgCodeOnly   = "pull requests are GitHub and GitLab only"
	msgNoCommits  = "a tracker has no commits until a code repo is linked to it"
	msgStateCheck = "state must be OPEN or CLOSED"
)

// routes is the mux of the API and the web app. Each route is the method and the path of api/openapi.yaml.
func (s *server) routes() *http.ServeMux {
	m := http.NewServeMux()
	m.HandleFunc("GET /api/openapi.yaml", s.openAPI)
	m.HandleFunc("GET /api/docs", s.docs)
	m.HandleFunc("GET /api/me", s.getMe)
	m.HandleFunc("POST /api/session", s.signIn)
	m.HandleFunc("DELETE /api/session", s.signOut)
	m.HandleFunc("GET /api/sign-in-options", s.signInOptions)
	m.HandleFunc("GET /api/oauth/{provider}/start", s.oauthStart)
	m.HandleFunc("GET /api/oauth/{provider}/callback", s.oauthCallback)
	m.HandleFunc("GET /api/repos", s.listRepos)
	m.HandleFunc("GET /api/repos/available", s.listAvailableRepos)
	m.HandleFunc("POST /api/repos", s.addRepo)
	m.HandleFunc("DELETE /api/repos", s.removeRepo)
	m.HandleFunc("PUT /api/repo-mode", s.setRepoMode)
	m.HandleFunc("POST /api/links", s.saveLinks)
	m.HandleFunc("GET /api/data", s.getData)
	m.HandleFunc("GET /api/search", s.search)
	m.HandleFunc("GET /api/issues/{key}", s.getRecord("issue"))
	m.HandleFunc("PATCH /api/issues/{key}", s.editIssue)
	m.HandleFunc("GET /api/pulls/{key}", s.getRecord("pull"))
	m.HandleFunc("PATCH /api/pulls/{key}", s.editPull)
	m.HandleFunc("POST /api/issues", s.createIssue)
	m.HandleFunc("GET /api/edit-options", s.editOptions)
	m.HandleFunc("POST /api/refresh-record", s.refreshRecord)
	m.HandleFunc("GET /api/commits", s.listCommits)
	m.HandleFunc("GET /api/commit", s.getCommit)
	m.HandleFunc("GET /api/merge", s.mergeMethod)
	m.HandleFunc("POST /api/sync", s.startSync)
	m.HandleFunc("GET /api/events", s.events)
	m.HandleFunc("POST "+webhookPath, s.receiveWebhook)
	// Any other /api/ path (or method) is a 404 in JSON, as the Node server answers.
	m.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { writeError(w, http.StatusNotFound, msgNotFound) })
	m.HandleFunc("/", s.static)
	return m
}

// repoRoute is the repo a per-repo route is about: the repo, the code repos of it the caller may see, and the caller.
type repoRoute struct {
	repo   string
	linked []string
	who    app.Who
}

// route finds the repo a request asks for. Without the repo parameter it is the first repo the caller may see. A repo
// that is not configured, or that the caller has no role in, answers 404.
func (s *server) route(w http.ResponseWriter, r *http.Request) (repoRoute, bool) {
	c := callerOf(r)
	ctx := r.Context()
	who := c.who()
	repo := ""
	if asked, ok := r.URL.Query()["repo"]; ok {
		repo = asked[0]
	} else if visible := s.app.VisibleRepos(ctx, who); len(visible) > 0 {
		repo = visible[0]
	}
	if _, ok := s.app.Entry(repo); !ok {
		writeError(w, http.StatusNotFound, msgNotConfig)
		return repoRoute{}, false
	}
	if c.sess != nil {
		role, err := s.app.RoleIn(ctx, repo, who)
		if err != nil {
			fail(w, err, http.StatusBadGateway)
			return repoRoute{}, false
		}
		if role == provider.RoleNone {
			writeError(w, http.StatusNotFound, msgNotConfig)
			return repoRoute{}, false
		}
	}
	return repoRoute{repo: repo, linked: s.app.VisibleLinked(ctx, repo, who), who: who}, true
}

// visibility is what the caller may see inside the repo.
func (s *server) visibility(w http.ResponseWriter, r *http.Request, rr repoRoute) (provider.Visibility, bool) {
	v, err := s.app.Visibility(r.Context(), rr.repo, rr.who)
	if err != nil {
		fail(w, err, http.StatusBadGateway)
		return provider.Visibility{}, false
	}
	return v, true
}

// recordFor is one record the caller may see: 404 when it is not in the repo or is hidden from the caller.
func (s *server) recordFor(w http.ResponseWriter, r *http.Request, rr repoRoute, kind, key string) (*model.Record, bool) {
	rec, ok := s.app.Record(rr.repo, rr.linked, kind, key)
	if !ok {
		writeError(w, http.StatusNotFound, msgNotFound)
		return nil, false
	}
	v, ok := s.visibility(w, r, rr)
	if !ok {
		return nil, false
	}
	visible, ok := app.VisibleRecord(rec, v)
	if !ok {
		writeError(w, http.StatusNotFound, msgNotFound)
		return nil, false
	}
	return visible, true
}

// writerFor is the credential an edit of the repo is made with. A refusal (the caller may not edit there) is a 403.
func (s *server) writerFor(w http.ResponseWriter, r *http.Request, rr repoRoute) (provider.Cred, bool) {
	cred, refusal, err := s.app.Writer(r.Context(), rr.repo, rr.who)
	if err != nil {
		fail(w, err, http.StatusBadGateway)
		return provider.Cred{}, false
	}
	if refusal != "" {
		writeError(w, http.StatusForbidden, refusal)
		return provider.Cred{}, false
	}
	return cred, true
}

// writeAllowed answers 403 unless the repo is in write mode.
func (s *server) writeAllowed(w http.ResponseWriter, rr repoRoute) bool {
	if s.app.Mode(rr.repo) != "write" {
		writeError(w, http.StatusForbidden, msgReadOnly)
		return false
	}
	return true
}

// pick keeps the changes of the named fields.
func pick(ch provider.Changes, names []string) provider.Changes {
	out := provider.Changes{}
	for k, v := range ch {
		if slices.Contains(names, k) {
			out[k] = v
		}
	}
	return out
}

// stateOK is whether a change's state is OPEN or CLOSED.
func stateOK(raw json.RawMessage) bool {
	var s string
	return json.Unmarshal(raw, &s) == nil && (s == "OPEN" || s == "CLOSED")
}

// positiveID is whether a relation's other issue is a positive id, as a number or as text.
func positiveID(raw json.RawMessage) bool {
	var f float64
	if json.Unmarshal(raw, &f) == nil {
		return f > 0
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		n, err := strconv.Atoi(s)
		return err == nil && n > 0
	}
	return false
}

// checkRelations validates the relations to add (Redmine). It answers and returns false when one is wrong.
func checkRelations(w http.ResponseWriter, changes provider.Changes) bool {
	raw, ok := changes["addRelations"]
	if !ok {
		return true
	}
	var rels []struct {
		Type string          `json:"type"`
		To   json.RawMessage `json:"to"`
	}
	if err := json.Unmarshal(raw, &rels); err != nil {
		writeError(w, http.StatusBadRequest, "addRelations must be a list of relations")
		return false
	}
	for _, rel := range rels {
		if !slices.Contains(relationTypes, rel.Type) || !positiveID(rel.To) {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("a relation needs a type (%s) and the id of the other issue", strings.Join(relationTypes, ", ")))
			return false
		}
	}
	return true
}

// checkAttachments validates the files of an edit: at most 5, each with a name and base64 data of at most 5 MB.
func checkAttachments(w http.ResponseWriter, changes provider.Changes) bool {
	raw, ok := changes["attachments"]
	if !ok {
		return true
	}
	var files []struct {
		Filename *string `json:"filename"`
		Data     *string `json:"data"`
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &files) != nil || len(files) > maxAttachments {
		writeError(w, http.StatusBadRequest, "attachments must be a list of at most 5 files")
		return false
	}
	for _, f := range files {
		if f.Filename == nil || f.Data == nil {
			writeError(w, http.StatusBadRequest, "an attachment needs a filename and its data")
			return false
		}
		if base64Size(*f.Data) > maxAttachment {
			writeError(w, http.StatusBadRequest, *f.Filename+" is larger than 5 MB")
			return false
		}
	}
	return true
}

// digits is whether s is a positive whole number as text (a Redmine issue id).
func digits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// withWarnings answers a record with the host's warnings added to its JSON object (GitHub and GitLab edits).
func withWarnings(w http.ResponseWriter, rec *model.Record, warnings []string) {
	b, err := json.Marshal(rec)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if m == nil {
		m = map[string]json.RawMessage{}
	}
	wb, _ := json.Marshal(nonNil(warnings))
	m["warnings"] = wb
	writeJSON(w, http.StatusOK, m)
}

// signInRefusal says which sites a sign-in may use.
func signInRefusal(prov string, allowed []string) string {
	if len(allowed) == 0 {
		return "this server reads no " + prov + " repo"
	}
	return "sign in to one of the sites this server reads: " + strings.Join(allowed, ", ")
}

// meAccount is one connected host identity inside a browser session.
type meAccount struct {
	User     provider.User `json:"user"`
	Instance string        `json:"instance"`
	Provider string        `json:"provider"`
}

// me is the person signed in, the latest host they connected, and all connected host accounts in this session.
type me struct {
	User     provider.User `json:"user"`
	Instance string        `json:"instance"`
	Provider string        `json:"provider"`
	Accounts []meAccount   `json:"accounts,omitempty"`
}

func meFor(sess *auth.Session) me {
	accts := make([]meAccount, 0, len(sess.Accounts))
	for _, a := range sess.AllAccounts() {
		accts = append(accts, meAccount{User: a.User, Instance: a.Origin, Provider: a.Provider})
	}
	return me{User: sess.User, Instance: sess.Origin, Provider: sess.Provider, Accounts: accts}
}

func (s *server) openAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	_, _ = w.Write(api.OpenAPI)
}

func (s *server) getMe(w http.ResponseWriter, r *http.Request) {
	c := callerOf(r)
	if c.sess == nil {
		writeError(w, http.StatusUnauthorized, "not signed in")
		return
	}
	writeJSON(w, http.StatusOK, meFor(c.sess))
}

func (s *server) signIn(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Provider *string `json:"provider"`
		Key      *string `json:"key"`
		Instance *string `json:"instance"`
	}
	if !readBody(w, r, signInLimit, &in) {
		return
	}
	prov := "redmine"
	if in.Provider != nil {
		prov = *in.Provider
	}
	if prov != "redmine" && prov != "github" && prov != "gitlab" {
		writeError(w, http.StatusBadRequest, "provider must be redmine, github or gitlab")
		return
	}
	if in.Key == nil || *in.Key == "" {
		writeError(w, http.StatusBadRequest, "the access key or token is needed")
		return
	}
	// Only a site this server reads: the key is checked there, and nowhere else.
	allowed := s.app.SignInOrigins(prov)
	origin := "github" // one GitHub host per run (GITHUB_HOST)
	if prov != "github" {
		instance := ""
		switch {
		case in.Instance != nil:
			instance = *in.Instance
		case prov == "gitlab" && len(allowed) == 1:
			instance = allowed[0]
		}
		o, err := originOf(instance)
		if err != nil {
			writeError(w, http.StatusBadRequest, "instance must be the URL of the host, for example https://redmine.example.com")
			return
		}
		origin = o
	}
	acc, ok := s.app.Accounts(prov, origin)
	if !slices.Contains(allowed, origin) || !ok {
		writeError(w, http.StatusBadRequest, signInRefusal(prov, allowed))
		return
	}
	user, err := acc.WhoAmI(r.Context(), *in.Key)
	if err != nil {
		fail(w, err, http.StatusBadGateway)
		return
	}
	id := s.sessions.Attach(auth.SessionIDOf(r), prov, origin, *in.Key, user, nil)
	w.Header().Set("Set-Cookie", auth.CookieFor(id, auth.WantsSecureCookie(r)))
	writeJSON(w, http.StatusOK, meFor(s.sessions.Get(id)))
}

func (s *server) signOut(w http.ResponseWriter, r *http.Request) {
	id := auth.SessionIDOf(r)
	prov := r.URL.Query().Get("provider")
	instance := r.URL.Query().Get("instance")
	if prov != "" && instance != "" {
		if rest := s.sessions.DropAccount(id, prov, instance); rest != nil {
			writeJSON(w, http.StatusOK, map[string]bool{"signedOut": false})
			return
		}
	} else {
		s.sessions.Drop(id)
	}
	w.Header().Set("Set-Cookie", auth.ClearedCookie(auth.WantsSecureCookie(r)))
	writeJSON(w, http.StatusOK, map[string]bool{"signedOut": true})
}

func (s *server) listRepos(w http.ResponseWriter, r *http.Request) {
	c := callerOf(r)
	ctx := r.Context()
	mine := s.app.VisibleRepos(ctx, c.who())
	links := map[string][]string{}
	for tracker := range s.app.Links() {
		if slices.Contains(mine, tracker) {
			links[tracker] = nonNil(s.app.VisibleLinked(ctx, tracker, c.who()))
		}
	}
	modes := map[string]string{}
	for _, repo := range mine {
		modes[repo] = s.app.Mode(repo)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"repos":        nonNil(mine),
		"links":        links,
		"modes":        modes,
		"canConfigure": s.canConfigure(ctx, c),
	})
}

type availableRepoItem struct {
	provider.AvailableRepo
	Added bool `json:"added"`
}

func (s *server) listAvailableRepos(w http.ResponseWriter, r *http.Request) {
	c := callerOf(r)
	ctx := r.Context()
	if !s.canConfigure(ctx, c) {
		writeError(w, http.StatusForbidden, "repos are added and linked from the machine the server runs on")
		return
	}
	prov := strings.TrimSpace(r.URL.Query().Get("provider"))
	originFilter := strings.TrimSpace(r.URL.Query().Get("origin"))
	if prov != "" && prov != "github" && prov != "gitlab" {
		writeError(w, http.StatusBadRequest, "provider must be github or gitlab")
		return
	}

	type source struct{ prov, origin, token string }
	var sources []source
	seenSource := map[string]bool{}
	addSource := func(p, o, t string) {
		if t == "" || (prov != "" && p != prov) || (originFilter != "" && o != originFilter) {
			return
		}
		key := p + "\x00" + o
		if seenSource[key] {
			return
		}
		seenSource[key] = true
		sources = append(sources, source{p, o, t})
	}

	if sess := s.renewed(ctx, c.sess); sess != nil {
		for _, a := range sess.AllAccounts() {
			if a.Provider == "github" || a.Provider == "gitlab" {
				addSource(a.Provider, a.Origin, a.Token)
			}
		}
	}
	if c.local {
		if (prov == "" || prov == "github") && !seenSource["github\x00github"] {
			if tok, err := s.app.ServerToken(ctx, "github", "github"); err == nil && tok != "" {
				addSource("github", "github", tok)
			}
		}
		if prov == "" || prov == "gitlab" {
			glOrigins := s.app.SignInOrigins("gitlab")
			if originFilter != "" {
				glOrigins = []string{originFilter}
			} else if len(glOrigins) == 0 {
				glOrigins = []string{"https://gitlab.com"}
			}
			for _, o := range glOrigins {
				if !seenSource["gitlab\x00"+o] {
					if tok, err := s.app.ServerToken(ctx, "gitlab", o); err == nil && tok != "" {
						addSource("gitlab", o, tok)
					}
				}
			}
		}
	}

	configured := map[string]bool{}
	for _, repo := range s.app.Repos() {
		configured[repo] = true
	}

	repos := []availableRepoItem{}
	var warnings []string
	for _, src := range sources {
		items, err := s.app.ListAvailableRepos(ctx, src.prov, src.origin, src.token)
		if err != nil {
			warnings = append(warnings, err.Error())
			continue
		}
		for _, it := range items {
			repos = append(repos, availableRepoItem{
				AvailableRepo: it,
				Added:         configured[it.Repo],
			})
		}
	}
	resp := map[string]any{"repos": repos}
	if len(warnings) > 0 {
		resp["warnings"] = warnings
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *server) canConfigure(ctx context.Context, c caller) bool {
	if c.local {
		return true
	}
	if c.sess == nil {
		return false
	}
	for _, a := range c.sess.AllAccounts() {
		if a.Provider != "github" && a.Provider != "gitlab" {
			continue
		}
		if !slices.Contains(s.app.SignInOrigins(a.Provider), a.Origin) {
			return true
		}
	}
	for _, repo := range s.app.Repos() {
		if role, err := s.app.RoleIn(ctx, repo, c.who()); err == nil && role == provider.RoleAdmin {
			return true
		}
	}
	return false
}

func (s *server) isOriginAdmin(ctx context.Context, c caller, prov, origin string) bool {
	if c.local {
		return true
	}
	for _, existing := range s.app.Repos() {
		if es, ok := s.app.Entry(existing); ok && es.Host.Provider() == prov && es.Host.Origin() == origin {
			if role, err := s.app.RoleIn(ctx, existing, c.who()); err == nil && role == provider.RoleAdmin {
				return true
			}
		}
	}
	return false
}

// addRepo adds a repo: from this machine, or by a signed-in GitHub/GitLab user when no repos exist yet on that origin
// or they are admin of a configured repo on that host.
func (s *server) addRepo(w http.ResponseWriter, r *http.Request) {
	c := callerOf(r)
	ctx := r.Context()
	if !s.canConfigure(ctx, c) {
		writeError(w, http.StatusForbidden, "repos are added and linked from the machine the server runs on")
		return
	}
	var in struct {
		Provider *string `json:"provider"`
		Repo     *string `json:"repo"`
		Mode     *string `json:"mode"`
	}
	if !readBody(w, r, bodyLimit, &in) {
		return
	}
	prov := "github"
	if in.Provider != nil {
		prov = *in.Provider
	}
	// "" means no mode was asked for: a new repo is read, an existing one keeps its mode.
	mode := ""
	if in.Mode != nil {
		if *in.Mode != "read" && *in.Mode != "write" {
			writeError(w, http.StatusBadRequest, "mode must be one of: read, write")
			return
		}
		mode = *in.Mode
	}
	source := ""
	if in.Repo != nil {
		source = strings.TrimSpace(*in.Repo)
	}
	if !c.local {
		if prov != "github" && prov != "gitlab" {
			writeError(w, http.StatusForbidden, "repos are added and linked from the machine the server runs on")
			return
		}
		origin, err := app.RepoOrigin(prov, source)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		acct, ok := c.sess.AccountFor(prov, origin)
		if !ok {
			writeError(w, http.StatusForbidden, "sign in on "+origin+" to add a repo from there")
			return
		}
		if slices.Contains(s.app.SignInOrigins(prov), origin) {
			if !s.isOriginAdmin(ctx, c, prov, origin) {
				writeError(w, http.StatusForbidden, "only an admin on "+origin+" may add another repo from there")
				return
			}
		}
		ctx = app.WithToken(ctx, acct.Token)
	}
	repo, added, err := s.app.AddRepo(ctx, prov, source, mode)
	if err != nil {
		// Input the host does not accept is a 400; a host failure comes as a *provider.Error with its own status.
		fail(w, err, http.StatusBadRequest)
		return
	}
	status := http.StatusOK
	if added {
		status = http.StatusCreated
	}
	repos := s.app.Repos()
	if !c.local {
		repos = s.app.VisibleRepos(ctx, c.who())
	}
	writeJSON(w, status, map[string]any{
		"repos": nonNil(repos),
		"repo":  repo,
		"added": added,
		"mode":  s.app.Mode(repo),
	})
}

// removeRepo unconfigures a repo: from this machine, or by an admin on that repo's host origin.
func (s *server) removeRepo(w http.ResponseWriter, r *http.Request) {
	c := callerOf(r)
	ctx := r.Context()
	if !s.canConfigure(ctx, c) {
		writeError(w, http.StatusForbidden, "repos are added and linked from the machine the server runs on")
		return
	}
	target := strings.TrimSpace(r.URL.Query().Get("repo"))
	if target == "" {
		writeError(w, http.StatusBadRequest, "repo is required")
		return
	}
	es, ok := s.app.Entry(target)
	if !ok {
		writeError(w, http.StatusNotFound, msgNotConfig)
		return
	}
	if !c.local {
		prov, origin := es.Host.Provider(), es.Host.Origin()
		if (prov != "github" && prov != "gitlab") || !s.isOriginAdmin(ctx, c, prov, origin) {
			writeError(w, http.StatusForbidden, "only an admin on "+origin+" may remove a repo from there")
			return
		}
	}
	if _, err := s.app.RemoveRepo(target); err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	repos := s.app.Repos()
	if !c.local {
		repos = s.app.VisibleRepos(ctx, c.who())
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"repos":   nonNil(repos),
		"removed": target,
	})
}

// setRepoMode switches a repo between read and write: from this machine, or by an admin of the repo on its host.
func (s *server) setRepoMode(w http.ResponseWriter, r *http.Request) {
	c := callerOf(r)
	target := r.URL.Query().Get("repo")
	if _, ok := s.app.Entry(target); !ok {
		writeError(w, http.StatusNotFound, msgNotConfig)
		return
	}
	if !c.local {
		role, err := s.app.RoleIn(r.Context(), target, c.who())
		if err != nil {
			fail(w, err, http.StatusBadGateway)
			return
		}
		if role != provider.RoleAdmin {
			writeError(w, http.StatusForbidden, "only an admin of this repo, or this machine, may switch its mode")
			return
		}
	}
	var in struct {
		Mode *string `json:"mode"`
	}
	if !readBody(w, r, bodyLimit, &in) {
		return
	}
	if in.Mode == nil || (*in.Mode != "read" && *in.Mode != "write") {
		writeError(w, http.StatusBadRequest, "mode must be one of: read, write")
		return
	}
	if err := s.app.SetMode(target, *in.Mode); err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"repo": target, "mode": *in.Mode})
}

// saveLinks sets the code repos a tracker names by key. From this machine only.
func (s *server) saveLinks(w http.ResponseWriter, r *http.Request) {
	if !callerOf(r).local {
		writeError(w, http.StatusForbidden, "repos are added and linked from the machine the server runs on")
		return
	}
	var in struct {
		Tracker string   `json:"tracker"`
		Code    []string `json:"code"`
	}
	if !readBody(w, r, bodyLimit, &in) {
		return
	}
	if _, ok := s.app.Entry(in.Tracker); !ok || (providerOf(in.Tracker) != "redmine" && providerOf(in.Tracker) != "jira") {
		writeError(w, http.StatusBadRequest, "the tracker must be a configured Jira or Redmine repo")
		return
	}
	code := []string{}
	for _, c := range in.Code {
		if !slices.Contains(code, c) {
			code = append(code, c)
		}
	}
	labels := map[string]bool{}
	for _, c := range code {
		if _, ok := s.app.Entry(c); !ok || (providerOf(c) != "github" && providerOf(c) != "gitlab") {
			writeError(w, http.StatusBadRequest, "each code repo must be a configured GitHub or GitLab repo")
			return
		}
		label := codeLabel(c)
		if labels[label] {
			writeError(w, http.StatusBadRequest, "two linked code repos share a name")
			return
		}
		labels[label] = true
	}
	if err := s.app.SetLinks(in.Tracker, code); err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tracker": in.Tracker, "code": code})
}

// codeLabel is the name a code repo is known by in its links: the project path of a GitLab repo, else its key.
func codeLabel(repo string) string {
	if !strings.HasPrefix(repo, "gitlab:") {
		return repo
	}
	u, err := url.Parse(strings.TrimPrefix(repo, "gitlab:"))
	if err != nil {
		return repo
	}
	return strings.TrimPrefix(u.Path, "/")
}

func (s *server) getData(w http.ResponseWriter, r *http.Request) {
	rr, ok := s.route(w, r)
	if !ok {
		return
	}
	d, err := s.app.Data(rr.repo, rr.linked)
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	v, ok := s.visibility(w, r, rr)
	if !ok {
		return
	}
	d = app.ApplyVisibility(d, v)
	d.Linked = nonNil(rr.linked)
	writeJSON(w, http.StatusOK, d)
}

func (s *server) search(w http.ResponseWriter, r *http.Request) {
	rr, ok := s.route(w, r)
	if !ok {
		return
	}
	v, ok := s.visibility(w, r, rr)
	if !ok {
		return
	}
	// The app searches only what v allows (a hidden note is never a snippet).
	results := s.app.Search(rr.repo, rr.linked, r.URL.Query().Get("q"), v)
	writeJSON(w, http.StatusOK, map[string]any{"results": nonNil(results)})
}

// getRecord reads one issue or pull request in full.
func (s *server) getRecord(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rr, ok := s.route(w, r)
		if !ok {
			return
		}
		rec, ok := s.recordFor(w, r, rr, kind, r.PathValue("key"))
		if !ok {
			return
		}
		if rec.Labels == nil || rec.Assignees == nil || rec.Comments == nil || rec.Events == nil {
			c := *rec
			c.Labels = nonNil(c.Labels)
			c.Assignees = nonNil(c.Assignees)
			c.Comments = nonNil(c.Comments)
			c.Events = nonNil(c.Events)
			rec = &c
		}
		writeJSON(w, http.StatusOK, rec)
	}
}

func (s *server) editIssue(w http.ResponseWriter, r *http.Request) {
	rr, ok := s.route(w, r)
	if !ok || !s.writeAllowed(w, rr) {
		return
	}
	if providerOf(rr.repo) == "jira" {
		writeError(w, http.StatusNotImplemented, "editing Jira issues is not built yet")
		return
	}
	// Who may write is decided before the body is read: a body of up to 40 MB is only read for someone who may change
	// the issue, never for a viewer.
	cred, ok := s.writerFor(w, r, rr)
	if !ok {
		return
	}
	var ch provider.Changes
	if !readBody(w, r, issueEditLimit, &ch) {
		return
	}
	key := r.PathValue("key")
	if _, ok := s.recordFor(w, r, rr, "issue", key); !ok {
		return
	}
	changes := pick(ch, issueFields)
	if len(changes) == 0 {
		writeError(w, http.StatusBadRequest, "nothing to change")
		return
	}
	if !checkAttachments(w, changes) {
		return
	}
	if raw, ok := changes["state"]; ok && !stateOK(raw) {
		writeError(w, http.StatusBadRequest, msgStateCheck)
		return
	}
	isRedmine := providerOf(rr.repo) == "redmine"
	if isRedmine {
		if !checkRelations(w, changes) {
			return
		}
		if !digits(key) {
			writeError(w, http.StatusBadRequest, "a Redmine issue id is a number")
			return
		}
	}
	rec, warnings, err := s.app.EditIssue(r.Context(), rr.repo, cred, key, changes)
	if err != nil {
		fail(w, err, http.StatusBadGateway)
		return
	}
	if isRedmine {
		writeJSON(w, http.StatusOK, rec)
		return
	}
	withWarnings(w, rec, warnings)
}

func (s *server) editPull(w http.ResponseWriter, r *http.Request) {
	rr, ok := s.route(w, r)
	if !ok || !s.writeAllowed(w, rr) {
		return
	}
	if p := providerOf(rr.repo); p != "github" && p != "gitlab" {
		writeError(w, http.StatusNotImplemented, msgCodeOnly)
		return
	}
	var ch provider.Changes
	if !readBody(w, r, bodyLimit, &ch) {
		return
	}
	key := r.PathValue("key")
	if _, ok := s.recordFor(w, r, rr, "pull", key); !ok {
		return
	}
	changes := pick(ch, pullFields)
	if len(changes) == 0 {
		writeError(w, http.StatusBadRequest, "nothing to change")
		return
	}
	if raw, ok := changes["state"]; ok && !stateOK(raw) {
		writeError(w, http.StatusBadRequest, msgStateCheck+" (a merge is not an edit)")
		return
	}
	cred, ok := s.writerFor(w, r, rr)
	if !ok {
		return
	}
	rec, err := s.app.EditPull(r.Context(), rr.repo, cred, key, changes)
	if err != nil {
		fail(w, err, http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, rec)
}

func (s *server) createIssue(w http.ResponseWriter, r *http.Request) {
	rr, ok := s.route(w, r)
	if !ok || !s.writeAllowed(w, rr) {
		return
	}
	if providerOf(rr.repo) == "jira" {
		writeError(w, http.StatusNotImplemented, "creating Jira issues is not built yet")
		return
	}
	var ch provider.Changes
	if !readBody(w, r, bodyLimit, &ch) {
		return
	}
	var title string
	if raw, ok := ch["title"]; ok {
		_ = json.Unmarshal(raw, &title)
	}
	title = strings.TrimSpace(title)
	if title == "" {
		writeError(w, http.StatusBadRequest, "a title is needed")
		return
	}
	out := pick(ch, newIssueFields)
	out["title"], _ = json.Marshal(title)
	cred, ok := s.writerFor(w, r, rr)
	if !ok {
		return
	}
	rec, err := s.app.CreateIssue(r.Context(), rr.repo, cred, out)
	if err != nil {
		fail(w, err, http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusCreated, rec)
}

// editOptions are the lists an edit form offers. Redmine's are read with the caller's key when signed in; GitHub and
// GitLab's need an editor's role, so the write credential is used (a refusal is a 403).
func (s *server) editOptions(w http.ResponseWriter, r *http.Request) {
	rr, ok := s.route(w, r)
	if !ok {
		return
	}
	if providerOf(rr.repo) == "jira" {
		writeError(w, http.StatusNotImplemented, "edit options are for Redmine, GitHub and GitLab")
		return
	}
	c := callerOf(r)
	var cred provider.Cred
	if providerOf(rr.repo) == "redmine" && c.sess != nil {
		cred = provider.Cred{Token: c.sess.Token}
	} else if cred, ok = s.writerFor(w, r, rr); !ok {
		return
	}
	// Cached only for this machine's own identity: what a signed-in person sees in the form depends on who they are.
	opts, err := s.app.EditOptions(r.Context(), rr.repo, cred, c.sess == nil)
	if err != nil {
		fail(w, err, http.StatusBadGateway)
		return
	}
	opts.Trackers = nonNil(opts.Trackers)
	opts.Priorities = nonNil(opts.Priorities)
	opts.Members = nonNil(opts.Members)
	opts.Versions = nonNil(opts.Versions)
	opts.Categories = nonNil(opts.Categories)
	opts.Activities = nonNil(opts.Activities)
	opts.CustomFields = nonNil(opts.CustomFields)
	opts.RelationTypes = nonNil(opts.RelationTypes)
	opts.Labels = nonNil(opts.Labels)
	opts.Milestones = nonNil(opts.Milestones)
	opts.IssueTypes = nonNil(opts.IssueTypes)
	writeJSON(w, http.StatusOK, opts)
}

// refreshRecord reads one issue or pull request again from its host.
func (s *server) refreshRecord(w http.ResponseWriter, r *http.Request) {
	rr, ok := s.route(w, r)
	if !ok {
		return
	}
	kind := "issue"
	if r.URL.Query().Get("kind") == "pull" {
		kind = "pull"
	}
	key := r.URL.Query().Get("key")
	if key == "" {
		writeError(w, http.StatusBadRequest, "the key is needed")
		return
	}
	if _, ok := s.recordFor(w, r, rr, kind, key); !ok {
		return
	}
	res, err := s.app.Refresh(r.Context(), rr.repo, kind, key)
	if err != nil {
		fail(w, err, http.StatusBadGateway)
		return
	}
	if res.Record != nil {
		v, ok := s.visibility(w, r, rr)
		if !ok {
			return
		}
		visible, ok := app.VisibleRecord(res.Record, v)
		if !ok {
			writeError(w, http.StatusNotFound, msgNotFound)
			return
		}
		res.Record = visible
	}
	writeJSON(w, http.StatusOK, res)
}

// intParam reads an integer parameter: a value that is not a number, or is out of [lo, hi], falls back to def.
func intParam(r *http.Request, name string, def, lo, hi int) int {
	n, err := strconv.Atoi(r.URL.Query().Get(name))
	if err != nil || n < lo {
		return def
	}
	if n > hi {
		return hi
	}
	return n
}

func (s *server) listCommits(w http.ResponseWriter, r *http.Request) {
	rr, ok := s.route(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	list, err := s.app.Commits(rr.repo, rr.linked, app.CommitQuery{
		Q:      strings.ToLower(strings.TrimSpace(q.Get("q"))),
		Author: strings.ToLower(strings.TrimSpace(q.Get("author"))),
		PR:     q.Get("pr"),
		Issue:  q.Get("issue"),
		Limit:  intParam(r, "limit", 100, 1, 500),
		Offset: intParam(r, "offset", 0, 0, int(^uint(0)>>1)),
	})
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	v, ok := s.visibility(w, r, rr)
	if !ok {
		return
	}
	list = app.VisibleCommits(list, v)
	list.Items = nonNil(list.Items)
	writeJSON(w, http.StatusOK, list)
}

func (s *server) getCommit(w http.ResponseWriter, r *http.Request) {
	rr, ok := s.route(w, r)
	if !ok {
		return
	}
	if (providerOf(rr.repo) == "redmine" || providerOf(rr.repo) == "jira") && len(rr.linked) == 0 {
		writeError(w, http.StatusNotFound, msgNoCommits)
		return
	}
	// The visibility is checked before the commit is read: a caller who may not see code must not make the server read
	// the host or write the diff cache.
	v, ok := s.visibility(w, r, rr)
	if !ok {
		return
	}
	if v.HideCode {
		writeError(w, http.StatusNotFound, msgNotFound)
		return
	}
	d, err := s.app.Commit(r.Context(), rr.repo, rr.linked, strings.ToLower(r.URL.Query().Get("sha")))
	if err != nil {
		fail(w, err, http.StatusBadGateway)
		return
	}
	// A commit whose code the caller may not see is not there for them (VisibleCommit).
	visible, ok := app.VisibleCommit(d, v)
	if !ok {
		writeError(w, http.StatusNotFound, msgNotFound)
		return
	}
	visible.Files = nonNil(visible.Files)
	visible.Issues = nonNil(visible.Issues)
	writeJSON(w, http.StatusOK, visible)
}

// mergeMethod is how a merged pull request landed. A pull that is not merged has no method.
func (s *server) mergeMethod(w http.ResponseWriter, r *http.Request) {
	rr, ok := s.route(w, r)
	if !ok {
		return
	}
	number := r.URL.Query().Get("number")
	rec, ok := s.recordFor(w, r, rr, "pull", number)
	if !ok {
		return
	}
	if rec.MergedAt == nil {
		writeJSON(w, http.StatusOK, map[string]*string{"method": nil})
		return
	}
	method, err := s.app.MergeMethod(r.Context(), rr.repo, rr.linked, number)
	if err != nil {
		fail(w, err, http.StatusBadGateway)
		return
	}
	var out *string
	if method != "" {
		out = &method
	}
	writeJSON(w, http.StatusOK, map[string]*string{"method": out})
}

// startSync starts a sync of the repo in the background; the answer does not wait for it.
func (s *server) startSync(w http.ResponseWriter, r *http.Request) {
	rr, ok := s.route(w, r)
	if !ok {
		return
	}
	started := s.app.RunSync(rr.repo, r.URL.Query().Get("full") == "1")
	writeJSON(w, http.StatusAccepted, map[string]bool{"started": started})
}
