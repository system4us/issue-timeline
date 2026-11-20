# Contributing to issue-timeline

Thanks for taking the time to contribute. Most useful changes are visualizations, analysis, provider support and
performance.

## Getting started

Requirements: Go 1.26, and Node.js 22 or newer with npm (for the web app only).

```sh
git clone https://gitlab.com/system4us/issue-timeline.git
cd issue-timeline
make deps
make test
```

Run it locally with `make dev` (the server on port 4310) and `cd web && npm run dev` (Vite on port 5173, proxied to it).
`make` lists every target.

## Project layout

- `cmd/issue-timeline/`: `main`.
- `internal/`: the server, one package per concern. Hosts (`github`, `gitlab`, `redmine`, `jira`) behind the interfaces
  of `provider`; records (`model`) and their files (`store`); derived data (`derive`); commits and CI (`commits`, `ci`);
  sessions, roles and gates (`auth`, `access`, `credentials`); repos, syncs and writes (`app`); the routes (`httpapi`);
  the embedded web app (`webui`); the command line (`cli`).
- `api/openapi.yaml`: the HTTP API contract. `web/src/api/schema.d.ts` is generated from it (`make gen`); the Go tests
  validate every operation against real responses.
- `test/e2e/`: the end-to-end test, over HTTP against the built binary and a fake Redmine (`make e2e`).
- `web/src/`: the React frontend. Timeline, analysis, filters and detail views. Pure logic (`filters.ts`, `metrics.ts`,
  `dashboard.ts`, `lanes.ts`, `timeScale.ts`) lives next to its tests.

## Before you open a merge request

1. `make test` passes (Go with `-race`, the web unit tests and the end-to-end test).
2. `make gen-check` passes when the API changed, and `gofmt -l cmd internal` prints nothing.
3. New logic has tests. Pure functions are preferred over logic inside components.
4. The change is focused. One feature or fix per merge request.
5. The commit messages say what changed and why, in the imperative mood ("add blocker chain analysis").

## Guidelines

- **Keep the server dependency-free** unless there is a strong reason: the standard library first. It is a deliberate
  property of the project.
- **Writes are opt-in.** A repo is read-only until it is switched to write; changes that write to a host need a design
  discussion before code.
- **Never commit secrets or data.** Tokens, certificates, `data/` and downloaded store files must stay out of the
  repository. Check `git status` before committing.
- **Keep every provider in mind.** A feature for GitHub should either work for GitLab (and the trackers, where it
  applies) or say clearly why it doesn't.

## Reporting bugs

Open an issue with:

- what you did, what you expected, and what happened
- the provider (GitHub, GitLab, Redmine or Jira, and the instance for self-managed)
- the version (the commit you built from)
- a minimal reproduction, or the relevant part of the store file with private data removed

Do not paste tokens, private repository content or certificates into an issue.

## Proposing features

Open an issue describing the problem before the solution. Explain who it helps and what the view should show. Small
ideas are welcome in the same way.

## Security

If you find a security issue, do not open a public issue. Contact the maintainers privately first. Note that without
sign-in the local API has no authentication, so exposing it beyond `127.0.0.1` without your own access control is a
known limitation, not a vulnerability to report.

## License

By contributing, you agree that your contributions are licensed under the [MIT License](LICENSE).
