# issue-timeline

**Your issues, pull requests, commits, and CI on one timeline** — across **GitHub**, **GitLab**, **Redmine**, and **Jira**.

A single dependency-free Go binary with an embedded React web UI. It syncs issues, merge/pull requests, dependencies, parent/epic hierarchies, commits, and CI runs into local JSON stores, and serves both the web app and an OpenAPI-documented REST/SSE API.

- **Source & documentation:** [GitLab](https://gitlab.com/system4us/issue-timeline) · [GitHub mirror](https://github.com/system4us/issue-timeline)
- **Built-in API docs:** `/api/docs` (OpenAPI spec at `/api/openapi.yaml`)

---

## Tags

| Tag | Source |
| --- | --- |
| `latest`, `X.Y.Z` | Stable release tags (`vX.Y.Z`) |
| `beta`, `X.Y.Z-beta.N` | Pre-release tags (`vX.Y.Z-beta.N`) |
| `edge` | Latest build from `main` |

---

## Quick start

### Docker Compose

Create a `.env` file (see [Configuration](#configuration)) and run:

```yaml
services:
  issue-timeline:
    image: system4us/issue-timeline:latest
    env_file:
      - path: .env
        required: false
    ports:
      - "127.0.0.1:4310:4310"
    volumes:
      - issue-timeline-data:/data
    restart: unless-stopped

volumes:
  issue-timeline-data:
```

```sh
docker compose up -d
```

Open `http://localhost:4310`.

### `docker run`

```sh
docker run -d \
  --name issue-timeline \
  -p 127.0.0.1:4310:4310 \
  -v issue-timeline-data:/data \
  -e GITHUB_TOKEN=ghp_... \
  -e GITLAB_TOKEN=glpat-... \
  --restart unless-stopped \
  system4us/issue-timeline:latest
```

The container runs as the unprivileged `issue-timeline` user, listens on `0.0.0.0:4310`, and persists all data under `/data` (`STORE_DIR=/data`).

---

## Authentication

Inside the container there is no `gh` or `glab` CLI session. You can authenticate in two ways (or combine both):

### 1. Server tokens (recommended for background sync)

Set personal access tokens in `.env` so the server can validate and periodically sync repositories even when no user is signed in:

- **GitHub:** `GITHUB_TOKEN` (fine-grained or classic PAT; private repos require `repo` / Issues + Pull requests + Contents + Metadata + Actions read).
- **GitLab:** `GITLAB_TOKEN` for `gitlab.com`, or `GITLAB_TOKEN_<HOST>` (e.g. `GITLAB_TOKEN_GITLAB_EXAMPLE_COM`) for self-managed instances (`api` scope, or `read_api` for read-only).
- **Redmine / Jira:** store secrets inside `/data` with:
  ```sh
  docker compose exec issue-timeline issue-timeline credentials set redmine-api-key
  docker compose exec issue-timeline issue-timeline credentials set jira-email
  docker compose exec issue-timeline issue-timeline credentials set jira-api-token
  ```

### 2. OAuth sign-in (GitHub / GitLab)

Configure an OAuth app so users sign in via *Continue with GitHub* / *Continue with GitLab*:

- **Callback URLs:**
  - GitHub: `<PUBLIC_URL>/api/oauth/github/callback`
  - GitLab: `<PUBLIC_URL>/api/oauth/gitlab/callback`
- When OAuth is configured, you can sign in right away even before adding your first repository, then add repositories on that host from the UI.
- Active sessions are persisted in `/data/sessions.json` (`0600`, without storing OAuth client secrets) so container restarts keep you signed in until `SESSION_IDLE_HOURS` or `SESSION_MAX_DAYS` expires.

---

## Exposing via Reverse Proxy or LAN / Tailscale

For DNS-rebinding protection, the server only accepts `Host` headers matching its own loopback/container interfaces or `ALLOWED_HOSTS`.

If you access the container through a reverse proxy, LAN IP, or Tailscale IP/MagicDNS:

1. Set `ALLOWED_HOSTS` to the hostname(s) or IP(s) clients use (comma-separated):
   ```env
   ALLOWED_HOSTS=timeline.example.com,100.64.0.10
   ```
2. Set `PUBLIC_URL` to the external URL (for example `https://timeline.example.com`), or leave it empty to derive the callback base from the incoming `Host` header.
3. Behind an HTTPS reverse proxy, forward `X-Forwarded-For` and `X-Forwarded-Proto: https` (or set `COOKIE_SECURE=1`).

---

## Configuration

| Variable | Default | Purpose |
| --- | --- | --- |
| `PORT` | `4310` | HTTP port inside the container |
| `HOST` | `0.0.0.0` | Listen address inside the container |
| `STORE_DIR` | `/data` | Directory for repo stores, config, and sessions |
| `ALLOWED_HOSTS` | none | Comma-separated hostnames/IPs accepted in `Host` header |
| `PUBLIC_URL` | from request | Base URL for OAuth callback URLs (e.g. `https://timeline.example.com`) |
| `COOKIE_SECURE` | `0` | Set to `1` to force `Secure` on session cookies |
| `SESSION_IDLE_HOURS` | `12` | Idle timeout for user sessions |
| `SESSION_MAX_DAYS` | `7` | Maximum lifetime of user sessions |
| `POLL_MINUTES` | `5` | Background sync interval in minutes |
| `CI_DAYS` | `90` | Days of CI runs retained |
| `GITHUB_TOKEN` | none | Server token for GitHub |
| `GITHUB_HOST` | `github.com` | GitHub Enterprise Server host |
| `GITHUB_OAUTH_CLIENT_ID`, `GITHUB_OAUTH_CLIENT_SECRET` | none | GitHub App or OAuth App credentials |
| `GITHUB_OAUTH_SCOPES` | `repo` | Scopes requested for GitHub OAuth Apps |
| `GITLAB_TOKEN` | none | Server token for `gitlab.com` (`GITLAB_TOKEN_<HOST>` for self-managed) |
| `GITLAB_OAUTH_CLIENT_ID`, `GITLAB_OAUTH_CLIENT_SECRET` | none | GitLab OAuth application credentials |
| `GITLAB_OAUTH_URL` | `https://gitlab.com` | GitLab instance URL when different from `gitlab.com` |
| `GITLAB_OAUTH_SCOPES` | `api` | Scopes requested for GitLab OAuth (`api` or `read_api`) |
| `WEBHOOK_SECRET` | none | Secret enabling `POST /api/webhook` for instant updates |
| `VOCABULARY_FILE` | built-in | Path inside container to a custom vocabulary JSON file |
| `REDMINE_API_KEY` | from `credentials` | Fallback Redmine API key |
| `JIRA_EMAIL`, `JIRA_API_TOKEN` | from `credentials` | Fallback Jira Cloud credentials |

---

## Persistent Data (`/data`)

Mount a volume at `/data` to preserve:

- `repos.json`, `repo-modes.json`, `links.json`, `roles.json` — configured repositories, read/write modes, tracker links, and role mappings
- `sessions.json` (`0600`) — active user sessions across restarts
- `credentials.json`, `secret-origins.json` (`0600`) — Redmine/Jira credentials saved via `issue-timeline credentials set`
- `*.json`, `*.commits.json`, `*.ci.json` — cached issues, pull/merge requests, commits, and CI runs per repository
