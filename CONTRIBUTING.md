# Contributing to issue-timeline

Thanks for taking the time to contribute. This project is read-only by design, so most useful changes are
visualizations, analysis, provider support and performance.

## Getting started

Requirements: Node.js 22 or newer, npm.

```sh
git clone https://github.com/system4us/issue-timeline.git
cd issue-timeline
npm run install:all
npm test
```

Run it locally with `REPO=owner/name npm run dev:server` (API on port 4310) and `npm run dev:web` (Vite on port 5173).

## Project layout

- `server/src/`: the Node service (no runtime dependencies). Providers (`github.js`, `gitlab.js`), sync (`sync.js`),
  derived data (`derive.js`), storage (`store.js`) and the HTTP API (`index.js`).
- `server/test/`: server tests, using `node:test` and a fake GitHub client.
- `web/src/`: the React frontend. Timeline, analysis, filters and detail views. Pure logic (`filters.ts`, `metrics.ts`,
  `lanes.ts`, `timeScale.ts`) lives next to its tests.

## Before you open a pull request

1. `npm test` passes (server and frontend).
2. `npm run build` passes (it type-checks with `tsc`).
3. New logic has tests. Pure functions are preferred over logic inside components.
4. The change is focused. One feature or fix per pull request.
5. The commit messages say what changed and why, in the imperative mood ("add blocker chain analysis").

## Guidelines

- **Keep the server dependency-free** unless there is a strong reason. It is a deliberate property of the project.
- **Read-only first.** Changes that write to GitHub or GitLab need a design discussion before code.
- **Never commit secrets or data.** Tokens, certificates, `data/` and downloaded store files must stay out of the
  repository. Check `git status` before committing.
- **Keep both providers in mind.** A feature for GitHub should either work for GitLab or say clearly why it doesn't.

## Reporting bugs

Open an issue with:

- what you did, what you expected, and what happened
- the provider (GitHub or GitLab, and the instance for self-managed)
- the version (from `package.json`, or the npm version you used) and your Node version
- a minimal reproduction, or the relevant part of the store file with private data removed

Do not paste tokens, private repository content or certificates into an issue.

## Proposing features

Open an issue describing the problem before the solution. Explain who it helps and what the view should show. Small
ideas are welcome in the same way.

## Security

If you find a security issue, do not open a public issue. Contact the maintainers privately first. Note that the local
API currently has no authentication, so exposing it beyond `127.0.0.1` without your own access control is a known
limitation, not a vulnerability to report.

## License

By contributing, you agree that your contributions are licensed under the [MIT License](LICENSE).
