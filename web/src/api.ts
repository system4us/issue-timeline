import type { Detail, Payload } from "./types"

const repoParam = (repo: string) => `?repo=${encodeURIComponent(repo)}`

export type RepoMode = "read" | "write"

/**
 * The repos the person may see, and each one's mode (read unless it was switched). `canConfigure` is true when the page is
 * used from the machine the server runs on: only then can repos be added and linked.
 */
export async function fetchRepos(): Promise<{ repos: string[]; modes: Record<string, RepoMode>; canConfigure: boolean }> {
  const res = await fetch("/api/repos")
  if (!res.ok) throw new Error(`/api/repos ${res.status}`)
  const body = await res.json()
  return { repos: body.repos, modes: body.modes ?? {}, canConfigure: body.canConfigure !== false }
}

/** Switches a repo between read and write. Writing is chosen on purpose, and the server keeps the choice. */
export async function setRepoMode(repo: string, mode: RepoMode): Promise<void> {
  const res = await fetch(`/api/repo-mode?repo=${encodeURIComponent(repo)}`, { method: "PUT", headers: { "content-type": "application/json", "x-issue-timeline": "1" }, body: JSON.stringify({ mode }) })
  if (!res.ok) throw new Error((await res.json().catch(() => ({})))?.error ?? `/api/repo-mode ${res.status}`)
}

export async function addRepo(repo: string, provider: "github" | "gitlab" | "redmine" | "jira", mode: RepoMode = "read"): Promise<{ repos: string[]; repo: string }> {
  const res = await fetch("/api/repos", { method: "POST", headers: { "content-type": "application/json", "x-issue-timeline": "1" }, body: JSON.stringify({ repo, provider, mode }) })
  const body = await res.json()
  if (!res.ok) throw new Error(body.error ?? `add repo ${res.status}`)
  return body
}

export async function fetchData(repo: string): Promise<Payload> {
  const res = await fetch(`/api/data${repoParam(repo)}`)
  if (!res.ok) throw new Error(`/api/data ${res.status}`)
  return res.json()
}

export async function triggerSync(full: boolean, repo: string): Promise<void> {
  await fetch(`/api/sync${repoParam(repo)}${full ? "&full=1" : ""}`, { method: "POST", headers: { "x-issue-timeline": "1" } })
}

export async function fetchDetail(kind: "issue" | "pull", key: string, repo: string): Promise<Detail> {
  const res = await fetch(`/api/${kind === "issue" ? "issues" : "pulls"}/${encodeURIComponent(key)}${repoParam(repo)}`)
  if (!res.ok) throw new Error(`detail ${res.status}`)
  return res.json()
}

export interface CommitRow {
  sha: string
  title: string
  author: string | null
  at: string | null
  additions: number | null
  deletions: number | null
  pr?: string | null // the pull request it belongs to (its own commit, or named by its title)
  issues?: string[] // the issues it closes or names
  inHistory?: boolean
}

export interface CommitPage {
  total: number // matching the search
  synced: number // stored in all
  lastSync: string | null
  items: CommitRow[]
}

export interface CommitFile {
  path: string
  oldPath: string | null
  status: "added" | "deleted" | "modified" | "renamed"
  additions: number
  deletions: number
  patch: string | null // null: binary, too large, or not included by the host (then `note` says why)
  note: string | null
}

export interface CommitDetail extends CommitRow {
  files: CommitFile[]
  truncated: boolean
  inHistory: boolean // on the default branch's history (false for a PR's own commit that never landed there)
}

/** One page of the default branch's commits, newest first, filtered on the server. */
export async function fetchCommits(repo: string, p: { q: string; author: string; pr: string; issue: string; offset: number; limit: number }): Promise<CommitPage> {
  const params = new URLSearchParams({ repo, q: p.q, author: p.author, pr: p.pr, issue: p.issue, offset: String(p.offset), limit: String(p.limit) })
  const res = await fetch(`/api/commits?${params}`)
  if (!res.ok) throw new Error(`/api/commits ${res.status}`)
  return res.json()
}

/** One commit with its files and diffs (the first call asks the host, the next ones read the cache). */
export async function fetchCommit(repo: string, sha: string): Promise<CommitDetail> {
  const res = await fetch(`/api/commit?${new URLSearchParams({ repo, sha })}`)
  const body = await res.json()
  if (!res.ok) throw new Error(body.error ?? `commit ${res.status}`)
  return body
}

/** How a merged PR landed on the default branch ("squash", "merge commit", "rebase"...), or null when unknown. */
export async function fetchMergeMethod(repo: string, key: string): Promise<string | null> {
  const res = await fetch(`/api/merge?${new URLSearchParams({ repo, number: key })}`)
  if (!res.ok) throw new Error(`/api/merge ${res.status}`)
  return (await res.json()).method ?? null
}

/** Saves which code repos (GitHub, GitLab) a tracker (Jira, Redmine) is linked to; an empty list unlinks it. */
export async function saveLinks(tracker: string, code: string[]): Promise<void> {
  const res = await fetch("/api/links", { method: "POST", headers: { "content-type": "application/json", "x-issue-timeline": "1" }, body: JSON.stringify({ tracker, code }) })
  if (!res.ok) throw new Error((await res.json().catch(() => ({})))?.error ?? `/api/links ${res.status}`)
}

/** The lists a Redmine issue form offers (trackers, priorities, members, versions, categories, custom fields). */
export interface EditOptions {
  trackers: { id: number; name: string }[]
  priorities: { id: number; name: string; isDefault: boolean }[]
  // GitHub's member id is the login; GitLab's is the numeric id
  members: { id: number | string; name: string }[]
  // GitHub and GitLab: the repo's labels and open milestones (ids are the host's)
  labels?: { name: string; color: string }[]
  milestones?: { id: number; name: string }[]
  versions: { id: number; name: string }[]
  categories: { id: number; name: string }[]
  customFields: { id: number; name: string; format: string | null; multiple: boolean; values: string[] }[]
  activities?: { id: number; name: string }[]
  issueTypes?: { id: number; name: string }[] // GitHub: the repo's issue types
  relationTypes: string[]
}

export async function fetchEditOptions(repo: string): Promise<EditOptions> {
  const res = await fetch(`/api/edit-options${repoParam(repo)}`)
  if (!res.ok) throw new Error((await res.json().catch(() => ({})))?.error ?? `/api/edit-options ${res.status}`)
  return res.json()
}

/** The whole record of an issue as the store keeps it (body, relations, custom fields and the form's ids included). */
export async function fetchIssueRecord(repo: string, key: string): Promise<any> {
  const res = await fetch(`/api/issues/${encodeURIComponent(key)}${repoParam(repo)}`)
  if (!res.ok) throw new Error(`/api/issues ${res.status}`)
  return res.json()
}

/** Sends an edit to the tracker. The server answers with the record as the tracker now has it, or with the reason it refused. */
export async function editIssue(repo: string, key: string, changes: Record<string, unknown>): Promise<any> {
  const res = await fetch(`/api/issues/${encodeURIComponent(key)}${repoParam(repo)}`, {
    method: "PATCH",
    headers: { "content-type": "application/json", "x-issue-timeline": "1" },
    body: JSON.stringify(changes),
  })
  const body = await res.json().catch(() => ({}))
  if (!res.ok) throw new Error(body?.error ?? `PATCH ${res.status}`)
  return body
}

/** The person signed in (their own Redmine key), or null. */
export interface Me { user: { id: number; login: string; name: string; admin: boolean }; instance: string; provider?: "github" | "gitlab" | "redmine" }
export async function fetchMe(): Promise<Me | null> {
  const res = await fetch("/api/me")
  if (res.status === 401) return null
  if (!res.ok) throw new Error(`/api/me ${res.status}`)
  return res.json()
}

/** A way to sign in the server offers: a host of the configured repos, with its own sign-in page (oauth) or a pasted token. */
export interface SignInMethod { provider: "github" | "gitlab" | "redmine"; origin: string; oauth: boolean; token: boolean }

export async function fetchSignInOptions(): Promise<SignInMethod[]> {
  const res = await fetch("/api/sign-in-options")
  if (!res.ok) throw new Error(`/api/sign-in-options ${res.status}`)
  return (await res.json()).methods ?? []
}

/** Where the host's own sign-in page starts (GitHub, GitLab): the browser goes there and comes back signed in. */
export const oauthStart = (provider: "github" | "gitlab", back: string) => `/api/oauth/${provider}/start?return=${encodeURIComponent(back)}`

/** Signs in with a pasted key or token: a Redmine API key, a GitHub or GitLab personal access token. The server checks it
 *  against the host and keeps it for this session only. */
export async function signIn(provider: SignInMethod["provider"], instance: string, key: string): Promise<Me> {
  const body: Record<string, string> = { provider, key }
  if (provider !== "github") body.instance = instance
  const res = await fetch("/api/session", { method: "POST", headers: { "content-type": "application/json", "x-issue-timeline": "1" }, body: JSON.stringify(body) })
  const out = await res.json().catch(() => ({}))
  if (!res.ok) throw new Error(out?.error ?? `/api/session ${res.status}`)
  return out
}

export async function signOut(): Promise<void> {
  await fetch("/api/session", { method: "DELETE", headers: { "x-issue-timeline": "1" } })
}

/** Creates an issue on a GitHub or GitLab repo. */
export async function createHostIssue(repo: string, fields: { title: string; body: string; labels: string[]; assignees: (string | number)[]; milestone: number | null; dueDate: string | null }): Promise<any> {
  const res = await fetch(`/api/issues${repoParam(repo)}`, { method: "POST", headers: { "content-type": "application/json", "x-issue-timeline": "1" }, body: JSON.stringify(fields) })
  const body = await res.json().catch(() => ({}))
  if (!res.ok) throw new Error(body?.error ?? `POST /api/issues ${res.status}`)
  return body
}

/** Edits a pull request (GitHub) or merge request (GitLab). */
export async function editPull(repo: string, key: string, changes: Record<string, unknown>): Promise<any> {
  const res = await fetch(`/api/pulls/${encodeURIComponent(key)}${repoParam(repo)}`, {
    method: "PATCH", headers: { "content-type": "application/json", "x-issue-timeline": "1" }, body: JSON.stringify(changes),
  })
  const body = await res.json().catch(() => ({}))
  if (!res.ok) throw new Error(body?.error ?? `PATCH ${res.status}`)
  return body
}

/** The matches of a search inside one repo: by key, title, and content. */
export interface SearchHit { key: string; kind: "issue" | "pull"; title: string; state: string; snippet: string | null }
export async function searchRepo(repo: string, q: string): Promise<SearchHit[]> {
  const res = await fetch(`/api/search${repoParam(repo)}&q=${encodeURIComponent(q)}`)
  if (!res.ok) throw new Error(`/api/search ${res.status}`)
  return (await res.json()).results
}

/** Reads one issue or pull/merge request again from its host. `changed` says whether anything moved; a read less than a minute old is not repeated. */
export async function refreshRecord(repo: string, kind: "issue" | "pull", key: string): Promise<{ changed: boolean; throttled?: boolean; fields?: string[] }> {
  const res = await fetch(`/api/refresh-record${repoParam(repo)}&kind=${kind}&key=${encodeURIComponent(key)}`, { method: "POST", headers: { "x-issue-timeline": "1" } })
  if (!res.ok) throw new Error((await res.json().catch(() => ({})))?.error ?? `/api/refresh-record ${res.status}`)
  return res.json()
}
