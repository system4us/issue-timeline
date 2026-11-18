# issue-timeline

**See the whole story of your backlog.** issue-timeline turns the issues and pull/merge requests of your GitHub and
GitLab projects into an interactive timeline: when work was opened, how it moved, what blocks what, and where the
bottlenecks are. Read-only, self-hosted, one command to run.

> **Beta (`1.0.0-beta.1`).** The local API has no authentication and storage is plain JSON. Run it on `127.0.0.1` or
> behind your own access control. See [Roadmap](#roadmap).

---

## Why issue-timeline

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

## Features

### Timeline
- Horizontal, draggable timeline with logarithmic zoom and a one-click fit.
- Two time axes: **event order** (every creation, close or merge gets the same width, so nothing is hidden by a
  long quiet period) and **real time**, with idle stretches collapsed into a "N days without activity" marker.
- Open items fade into the present; closed items keep their full length.
- Issue ↔ pull request links, highlighted on hover or selection.
- Filters by text, state, milestone, labels (any or all), authors, assignees, dates, and linked/unlinked PRs.
- Group rows by milestone, label or author. The axis adapts to what is on screen.

### Hierarchy and dependencies
- **Epics and children:** native GitHub sub-issues and the task lists written in epic descriptions. Parents show a
  `▸closed/total` counter and a progress line; their children light up on hover, listed open-first in the tooltip.
- **Blockers:** native relations (GitHub "blocked by / blocks", GitLab issue links "blocks / is blocked by") and
  plain-language references in descriptions ("Depends on #N", "Blocked by #N", "Blocks #N", "Depende de #N", "Bloquea #N"…).
  Red arrows for open blockers, grey dashed for resolved ones. The words are a vocabulary, see below.
- **Chain analysis:** how many issues are stuck behind each blocker, transitively.

### Analysis
- Open issues over time, created vs closed per week.
- Time to close and time to merge, with medians and p90.
- PR size against merge time.
- Issues with and without a linked PR.
- PRs merged without a review.
- A comparison table for every milestone, label, author or assignee: progress, open, median and p90 close time,
  merged PRs, open without PR, oldest open item, and due dates. Click a row to filter the whole app by it.

### Built to run anywhere
- **Node service with zero dependencies** (Node 22+). Polls GitHub GraphQL or GitLab REST every 5 minutes.
- **Full history on the first pass:** issues and PRs/MRs with comments, events, commits, and reviews/approvals.
  Incremental syncs after that.
- **Multiple repositories**, switchable from the header, each stored separately.
- **Atomic JSON storage**, one file per repository. Easy to back up, inspect and move.
- Published as an **npm package**.

## Quick start

```sh
npx issue-timeline
```

Then open http://127.0.0.1:4310 and add a repository from the UI.


### Authentication

- **GitHub:** set `GITHUB_TOKEN`, or log in with `gh auth login`. Private repositories need the `repo` scope. You can add
  `owner/repo` or paste the repository URL. For GitHub Enterprise Server set `GITHUB_HOST` (one instance per run).
- **GitLab:** log in with `glab auth login` for each instance, then add `group/project` (GitLab.com) or the full URL
  of a self-managed instance.

### Configuration

| Variable       | Default       | Purpose                                              |
| -------------- | ------------- | ---------------------------------------------------- |
| `PORT`         | `4310`        | HTTP port                                            |
| `HOST`         | `127.0.0.1`   | Bind address       |
| `STORE_DIR`    | `./data`      | Where repository data and the repo list are kept     |
| `POLL_MINUTES` | `5`           | Sync interval                                        |
| `GITHUB_TOKEN` | from `gh`     | GitHub token                                         |
| `GITHUB_HOST`  | `github.com`  | GitHub Enterprise Server host (REST and GraphQL URLs follow) |
| `GITLAB_TOKEN` | from `glab`   | GitLab.com token; `GITLAB_TOKEN_<HOST>` for self-managed |
| `VOCABULARY_FILE` | built-in | JSON file with your own words for dependencies, fields and CI workflows (see below) |

### Vocabulary

Issue bodies say things like "Depends on #12" or "Bloqueado por #9", and "Design: …" or "Diseño: …" lines. Those words
are a list in [`server/src/vocabulary.json`](server/src/vocabulary.json), with English, Spanish, Portuguese, French and
German by default. To use your own words, write a JSON file with any of these keys and set `VOCABULARY_FILE` to its path.
Each key you give replaces the default list for that key; the rest keep the defaults.

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

## API

- `GET /api/data`: lightweight records, links and sync status
- `GET /api/issues/:n`, `GET /api/pulls/:n`: full record for one item
- `POST /api/sync[?full=1]`: trigger a sync

## Development

```sh
npm run install:all            # frontend dependencies
REPO=owner/name npm run dev:server   # API on :4310
npm run dev:web                # Vite on :5173, proxied to the API
npm test                       # server and frontend tests
```

## Roadmap

- Editing from the timeline (labels, close, comment) through GraphQL mutations
- Webhooks instead of polling
- Project and milestone events
- Velocity per person
- SQLite storage once the JSON files grow
- Parallel sync for large repositories (a first full sync of a large repo takes about 2 minutes today)

## Notes

- The interface is in English. Issue bodies are read with the vocabulary above (English, Spanish, Portuguese, French,
  German by default); a language not in it needs `VOCABULARY_FILE`.
- Not read: GitLab epics (a Premium feature) and the parent of an issue in GitLab. A dependency written only in a comment
  is not read either.
- Links from commits are read from the title: a "#N" that matches an issue links the commit to it. This is a heuristic.
- A GitLab issue's links are read when the issue is synced. If a link added later does not show after an incremental
  sync, a full sync (`POST /api/sync?full=1`) reads it.
- Contributions are welcome. See [CONTRIBUTING.md](CONTRIBUTING.md).

## License

[MIT](LICENSE)
