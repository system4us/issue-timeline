# issue-timeline

**See the whole story of your backlog.** issue-timeline turns the issues and pull/merge requests of your GitHub and
GitLab projects into an interactive timeline: when work was opened, how it moved, what blocks what, and where the
bottlenecks are. Read-only, self-hosted, one command to run.

[![npm](https://img.shields.io/npm/v/issue-timeline?label=npm&color=cb3837)](https://www.npmjs.com/package/issue-timeline)
[![license](https://img.shields.io/npm/l/issue-timeline)](LICENSE)
[![node](https://img.shields.io/node/v/issue-timeline)](package.json)

![The timeline: every issue and pull request as a bar from creation to close or merge](https://raw.githubusercontent.com/system4us/issue-timeline/main/docs/timeline.png)

```sh
npx issue-timeline@beta
```

It opens in your browser. Add `owner/repo` (GitHub) or `group/project` (GitLab) and the history loads in the background.
No account, no server to deploy, no dependencies: it signs in with the `gh` and `glab` sessions you already have.

> **Beta.** The local API has no authentication and storage is plain JSON, so run it on `127.0.0.1` (the default) or
> behind your own access control. Until the first stable release `npx` needs the `@beta` tag. See [Status](#status).

---

## Why

Issue lists tell you what is open today. They don't tell you how the work actually flowed. issue-timeline does:

- **See bursts and droughts.** Work arrives in waves. A horizontal timeline shows every issue and pull request as a bar
  from creation to close or merge, so you can spot the week everything stalled.
- **Read dependencies at a glance.** Blockers are drawn as arrows, blocked items are marked, and each item shows how many
  open issues sit behind it, all the way down the chain.
- **Track epics without guessing.** Progress is computed from the real state of each child, not from checkboxes someone
  forgot to tick. Sub-issues and task lists both count.
- **Answer the questions that matter.** Open issues over time, created vs closed per week, days to close, days to merge,
  PRs merged without review, the oldest open issues, and per-milestone, per-label or per-author breakdowns.
- **Share a view.** Filters live in the URL. Copy it, send it, reload it, and your teammate sees exactly what you see.

![Analysis: progress, throughput, time to close and to merge, PR size against merge time](https://raw.githubusercontent.com/system4us/issue-timeline/main/docs/analysis.png)

## Features

### Timeline
- Horizontal, draggable timeline with logarithmic zoom and a one-click fit.
- Two time axes: **event order** (every creation, close or merge gets the same width, so nothing is hidden by a long
  quiet period) and **real time**, with idle stretches collapsed into a "N days without activity" marker.
- Open items fade into the present; closed items keep their full length.
- Issue ↔ pull request links, highlighted on hover or selection.
- Filters by text, state, milestone, labels (any or all), authors, assignees, dates, and linked/unlinked PRs.
- Group rows by milestone, label or author. The axis adapts to what is on screen.

### Hierarchy and dependencies
- **Epics and children:** native GitHub sub-issues, GitLab epics and parent/child work items, and the task lists
  written in epic descriptions. Parents show a `▸closed/total` counter and a progress line; their children light up on
  hover.
- **Blockers:** native relations (GitHub "blocked by / blocks", GitLab issue links "blocks / is blocked by") and
  plain-language references in descriptions ("Depends on #N", "Blocked by #N", "Blocks #N", "Depende de #N"…). Red
  arrows for open blockers, grey dashed for resolved ones. The words are a [vocabulary](#vocabulary) you can extend.
- **Chain analysis:** how many issues are stuck behind each blocker, transitively.

### Analysis
- Open issues over time, created vs closed per week, PRs opened and merged per week.
- Time to close and time to merge, with medians and p90. PR size against merge time.
- Issues with and without a linked PR. PRs merged without a review.
- A comparison table for every milestone, label, author or assignee: progress, open, median and p90 close time, merged
  PRs, open without PR, oldest open item and due dates. Click a row to filter the whole app by it. Export it as CSV.

### Commits and CI
- The default branch's commits, tied to issues and PRs by the `#N` in their title, with the files each one changed.
- How each PR was merged (squash, rebase, merge commit) and its CI runs: GitHub Actions runs and GitLab pipelines, plus
  the commits that changed the CI definition.

## Requirements

- **Node.js 22 or newer.** Node 20 reached end of life in April 2026 and the package targets maintained versions.
- For GitHub, the [GitHub CLI](https://cli.github.com) signed in (`gh auth login`) or a `GITHUB_TOKEN`.
- For GitLab, the [GitLab CLI](https://gitlab.com/gitlab-org/cli) signed in (`glab auth login`) or a `GITLAB_TOKEN`.

You need only the one you use.

## Usage

```sh
npx issue-timeline@beta            # run in the foreground and open the browser
npx issue-timeline@beta --background   # keep running after you close the terminal
issue-timeline stop                # stop the background one
```

| Option             | What it does                                                            |
| ------------------ | ----------------------------------------------------------------------- |
| `-b`, `--background` | Run detached. Logs go to `issue-timeline.log` in the data folder.     |
| `--no-open`        | Do not open the browser (it is also skipped in CI and without a terminal). |
| `-h`, `--help`     | Show the help.                                                          |

On start it prints where to open it, where the data lives, and which sessions it found:

```
issue-timeline is running
  Open:   http://127.0.0.1:4310
  Repos:  none yet, add one from the page
  Data:   ~/.local/share/issue-timeline
  Auth:   uses your gh and glab sessions (read-only)
          GitHub: gh session
          GitLab: not signed in. Run `glab auth login` or set GITLAB_TOKEN
```

### Adding repositories

From the page, pick the provider and type:

- **GitHub:** `owner/repo`, or paste the repository URL.
- **GitLab:** `group/project`, or the full URL of a self-managed instance (`https://gitlab.example.com/group/project`).

A repository is saved only after the host confirms it exists and your login can read it. Add as many as you want and
switch between them from the header.

### Authentication

issue-timeline uses the sessions you already have, so there is nothing to configure. It only reads, and it never
stores your tokens.

- **GitHub:** `gh auth login`, or set `GITHUB_TOKEN`. Private repositories need the `repo` scope.
  For **GitHub Enterprise Server** set `GITHUB_HOST` (for example `github.example.com`), one instance per run.
- **GitLab:** `glab auth login` once per instance, or set `GITLAB_TOKEN` (GitLab.com) / `GITLAB_TOKEN_<HOST>` for a
  self-managed instance, for example `GITLAB_TOKEN_GITLAB_EXAMPLE_COM`. Environment tokens need no CLI, so they work in
  containers and on servers.

### Configuration

| Variable        | Default                | Purpose                                                           |
| --------------- | ---------------------- | ----------------------------------------------------------------- |
| `PORT`          | `4310`                 | HTTP port                                                         |
| `HOST`          | `127.0.0.1`            | Bind address. Change it only behind your own access control.      |
| `STORE_DIR`     | user data folder       | Where repository data and the repo list are kept (see below)      |
| `POLL_MINUTES`  | `5`                    | Sync interval                                                     |
| `CI_DAYS`       | `90`                   | How many days of CI runs are kept                                 |
| `GITHUB_TOKEN`  | from `gh`              | GitHub token                                                      |
| `GITHUB_HOST`   | `github.com`           | GitHub Enterprise Server host (REST and GraphQL URLs follow)      |
| `GITLAB_TOKEN`  | from `glab`            | GitLab.com token. `GITLAB_TOKEN_<HOST>` for other instances.      |
| `VOCABULARY_FILE` | built in             | Your own words for dependencies, fields and CI workflows         |

**Where the data lives.** `STORE_DIR` if set; otherwise the `./data` of the current folder if it already holds a
repository list; otherwise a folder of your user: `~/.local/share/issue-timeline` (Linux),
`~/Library/Application Support/issue-timeline` (macOS), `%APPDATA%\issue-timeline` (Windows). Running the command inside
a project never drops a data folder in it.

### Vocabulary

Issue bodies say things like "Depends on #12" or "Bloqueado por #9", and "Design: …" or "Diseño: …" lines. Those words
are a list in [`server/src/vocabulary.json`](server/src/vocabulary.json), with English, Spanish, Portuguese, French and
German by default. To use your own words, write a JSON file with any of these keys and set `VOCABULARY_FILE` to its
path. Each key you give replaces the default list for that key; the rest keep the defaults.

```json
{
  "blockedBy": ["waits for"],
  "blocks": ["unblocks"],
  "fieldKeys": ["Ticket", "Spec"],
  "ci": { "otherWorkflows": ["nightly", "deploy"] }
}
```

- `blockedBy`: phrases that mean "blocked by / depends on" (the issue is blocked by the number that follows).
- `blocks`: phrases that mean "blocks" (the number that follows is blocked by this issue).
- `fieldKeys`: keys shown as fields of the detail panel.
- `ci.otherWorkflows`: workflows that are not a PR's CI (releases, deploys, docs, ...), matched as whole words.

The same file is used by the frontend, which receives it with the data.

## How it works

A small Node service, with no runtime dependencies, polls GitHub (GraphQL) or GitLab (REST) every few minutes and keeps
one JSON file per repository. The React frontend is served by the same process.

- **First sync** reads the full history: issues and PRs/MRs with comments, events, commits and reviews/approvals. On
  GitHub the open, closed and merged items are read as parallel chains of pages, so a full read of a repository with
  about 300 issues and PRs takes **about 20 seconds instead of one and a half to two minutes**. GitLab is bound by its own rate limits
  (about seven requests per merge request), and the client waits them out.
- **Later syncs** walk newest-updated first and stop at the first item older than the last sync: usually one or two small
  queries.
- **Storage** is atomic: each write goes to a temporary file and is renamed over the old one, so a crash never leaves a
  half-written file. The files are easy to back up, inspect and move. An item with its comments and events takes about
  3 to 4 KB, so a repository with a thousand of them is a few megabytes.

### Why JSON and not a database

The files are small (about 3 to 4 KB per issue or PR, comments and events included) and the time of a sync is spent waiting
for the hosts, not writing to disk, so a database would not make it faster. The built-in `node:sqlite` is still
experimental and needs a recent Node, and a native driver would give up the zero-dependency install. If a repository
grows so large that rewriting its file hurts, that is the moment to move it.

## API

- `GET /api/data`: lightweight records, links and sync status
- `GET /api/issues/:n`, `GET /api/pulls/:n`: full record for one item
- `POST /api/sync[?full=1]`: trigger a sync

The API has no authentication. Do not expose it to a network you do not trust.

## Status

issue-timeline is in **beta**. What is left before `1.0.0`:

- Protect the API when it is bound to anything other than the loopback address.
- Test on macOS and Windows (the data folder and browser opening are written for them, but nobody has run it there yet).
- Try it against a GitHub Enterprise Server instance.

## Roadmap

- Editing from the timeline (labels, close, comment) through GraphQL mutations
- Webhooks instead of polling
- Project and milestone events
- Velocity per person
- SQLite storage once the JSON files outgrow it

## Notes

- The interface is in English. Issue bodies are read with the vocabulary above (English, Spanish, Portuguese, French,
  German by default); another language needs `VOCABULARY_FILE`.
- A dependency written only in a comment is not read.
- Links from commits are read from the title: a "#N" that matches an issue links the commit to it. This is a heuristic.
- A GitLab issue's links are read when the issue is synced. If a link added later does not show after an incremental sync,
  a full sync (`POST /api/sync?full=1`) reads it.
- Hierarchy on GitLab comes from the work items API, so it needs a version that has it.

## Development

```sh
git clone https://github.com/system4us/issue-timeline.git
cd issue-timeline
npm run install:all            # frontend dependencies
npm run build                  # builds the frontend
npm run dev:server             # API on :4310, add repositories from the page
npm run dev:web                # Vite on :5173, proxied to the API
npm test                       # server and frontend tests
```

Contributions are welcome. See [CONTRIBUTING.md](CONTRIBUTING.md). The development happens on
[GitLab](https://gitlab.com/system4us/issue-timeline); the GitHub repository is a read-only mirror.

## License

[MIT](LICENSE)
