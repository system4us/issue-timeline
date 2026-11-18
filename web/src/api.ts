import type { Detail, Payload } from "./types"

const repoParam = (repo: string) => `?repo=${encodeURIComponent(repo)}`

export async function fetchRepos(): Promise<string[]> {
  const res = await fetch("/api/repos")
  if (!res.ok) throw new Error(`/api/repos ${res.status}`)
  return (await res.json()).repos
}

export async function addRepo(repo: string, provider: "github" | "gitlab"): Promise<{ repos: string[]; repo: string }> {
  const res = await fetch("/api/repos", { method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify({ repo, provider }) })
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
  await fetch(`/api/sync${repoParam(repo)}${full ? "&full=1" : ""}`, { method: "POST" })
}

export async function fetchDetail(kind: "issue" | "pull", number: number, repo: string): Promise<Detail> {
  const res = await fetch(`/api/${kind === "issue" ? "issues" : "pulls"}/${number}${repoParam(repo)}`)
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
  pr?: number | null // the pull request it belongs to (its own commit, or named by its title)
  issues?: number[] // the issues it closes or names
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
export async function fetchMergeMethod(repo: string, number: number): Promise<string | null> {
  const res = await fetch(`/api/merge?${new URLSearchParams({ repo, number: String(number) })}`)
  if (!res.ok) throw new Error(`/api/merge ${res.status}`)
  return (await res.json()).method ?? null
}
