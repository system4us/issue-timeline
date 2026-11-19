# The server

One Go binary (`cmd/issue-timeline`), with the web app embedded (`make build`). It replaced the earlier Node server,
with the same JSON on disk and the same HTTP API, described in `api/openapi.yaml`.

Module: `gitlab.com/system4us/issue-timeline`, Go 1.26. Standard library first. The only dependencies (in `go.mod`):
`github.com/getkin/kin-openapi` (tests only), `golang.org/x/term`, `github.com/zalando/go-keyring`.

## Layout

| Package | What |
| --- | --- |
| `internal/model` | Records, commits, CI runs (JSON-compatible with the stores and `web/src/types.ts`) |
| `internal/store` | One repo's store file, locks, `FullDue` |
| `internal/provider` | The interfaces every host implements (the server asks for the optional ones with a type assertion) |
| `internal/derive` | `BuildPayload`, vocabulary, commit links, key links, search, local apply |
| `internal/commits`, `internal/ci` | Commit store, diff cache, merge classification; CI store, summaries, windows |
| `internal/github`, `internal/gitlab` | Sync, edits, commits, CI, merge method, meta refresh, roles, visibility |
| `internal/redmine`, `internal/jira` | The two trackers, with Redmine's edits, roles and visibility |
| `internal/auth`, `internal/access`, `internal/credentials`, `internal/cli` | Sessions and roles, host/origin/local checks and body reading, secrets and keyring, the command line |
| `internal/app` | Repos, links, modes, entries, sync scheduling, linked trackers, writes |
| `internal/httpapi`, `internal/webui` | Routes, gates, visibility filtering, the OpenAPI document served and validated, the embedded web app |

## Rules

**Behaviour.** Keep the JSON field names exactly: the store files and the API are shared with the web app
(`web/src/types.ts`) and described in `api/openapi.yaml`.

**Records.** Use `model.Record`. A reference to another record (`parent`, `subIssues`, `blockedBy`, `blocking`,
`closes`) is a `model.Ref`: compare as text, never convert to int to compare. Records in a store are immutable once
stored: to change one, copy it (`c := *old`, and copy the slices you change), change the copy, and put the copy in the
map under `st.Lock()`.

**Concurrency.** Syncs run in goroutines while requests are served. Read store data under `st.RLock()` / `st.View`,
change it under `st.Lock()` / `st.Update`. Never hold a store lock during a network call or a disk write
(`st.Save()` takes its own read lock). Any cache you keep in a package (tokens, roles) is guarded by a mutex. Tests
must pass with `-race`.

**HTTP to hosts.** `net/http` with a context and a timeout (30 s). Retry 429 up to 3 times for every method (the
host did not process it), waiting what `Retry-After` says, at most 15 s. Retry 5xx only for GET, PUT and DELETE
(idempotent): a POST that got a 5xx may already have created something (an issue, a note, a time entry, an upload), so
it is never sent again; the error goes back to the caller. A value from a request that goes in a URL path is checked with `provider.PathID`.
Pagination follows the host's own next-page signal (the `Link` header for GitHub and GitLab REST, `pageInfo` for
GraphQL, offsets for Redmine and Jira) and never follows a next link to another origin. An error the API should pass
on is a `*provider.Error` with its status.

**Tests.** Every package has tests, using `httptest.Server` (or a fake
`http.RoundTripper`) for hosts. No test reaches the network or runs `gh`, `glab` or `secret-tool`. Run
`make test` (Go with `-race`, the web tests and the end-to-end test) and `go vet ./...` until both pass;
`gofmt -l cmd internal` must print nothing.

**Style.** Comments in plain English that say why. Exported names documented. Small functions. No
global mutable state except guarded caches. Errors wrapped with context (`fmt.Errorf("reading %s: %w", ...)`).

**Regex.** Go's `regexp` (RE2) has no lookbehind or lookahead. Where a pattern needs one, match the boundary character as part of the pattern
(`(?:^|[^\p{L}\p{N}_])`) and check the character after the match in code, or count lines with `strings`.

**Security.** Secrets are only sent to the site they are bound to. Sign-in only to sites
of configured repos. A signed-in person only sees repos their host gives them a role in. Writes need the
`x-issue-timeline: 1` header and an `Origin` matching `Host`. `Host` must name this server. A request is local only
from loopback without proxy headers.

## What each person may see

The records come from the sync, which reads with the server's own identity. A signed-in person must not see more than
their own credential could read on the host. Each `provider.Accounts` host computes it in `VisibilityOf`:

- **GitHub**: a role in the repo (pull or more) reads every issue and pull request. Nothing more to hide.
- **GitLab**: confidential issues are visible only when the person's token lists them
  (`GET /projects/:id/issues?confidential=true`, every page, with their token). Internal notes (`internal: true` on a
  note; the sync keeps it as `Comment.Private`) are hidden below Reporter (access level 20). Below Reporter, pulls,
  commits, diffs and CI are hidden too (`HideCode`).
- **Redmine**: the issues the person may see are the ones Redmine lists for their key
  (`GET /issues.json?project_id=<id>&status_id=*`, ids only, every page): that covers private issues and roles that
  only see their own issues. Private notes (`private_notes: true` on a journal; the sync keeps it as `Comment.Private`)
  are checked per issue that has any: `GET /issues/:id.json?include=journals` with the person's key, and the private
  notes not in that answer are hidden.
- **Jira**: no sign-in yet; a session never sees a Jira project.

The server caches a Visibility per session and repo for 5 minutes, and applies it to every route that returns records
(data, search, one record, commits' issue links, refresh).
