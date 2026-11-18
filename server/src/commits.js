// Commits of the default branch. They live apart from the issues store: the list is read on its own, when the Commits
// tab is open, and a commit's diff is fetched on demand and cached in its own file (a commit never changes, so the
// cache never goes stale and the big diffs stay out of the store the page reloads every 30 s).
import { mkdir, readFile, rename, writeFile } from "node:fs/promises";
import { githubEndpoints } from "./github.js";
import { join } from "node:path";
import { createHash } from "node:crypto";

export const FULL_SHA = /^[0-9a-f]{40}$/;
const MAX_FILE_PATCH = 200_000; // characters: a bigger file keeps its stats, not its patch
const MAX_DIFF = 1_500_000; // characters of patch per commit

const HISTORY_QUERY = `query($owner: String!, $name: String!, $cursor: String) {
  repository(owner: $owner, name: $name) {
    defaultBranchRef { target { ... on Commit {
      history(first: 100, after: $cursor) {
        pageInfo { hasNextPage endCursor }
        nodes { oid messageHeadline committedDate author { name user { login } } additions deletions }
      }
    } } }
  }
}`;

/** One JSON file: { meta: { repo, lastSync }, items: [newest first] }. */
export class CommitStore {
  constructor(file) {
    this.file = file;
    this.data = { meta: { repo: null, lastSync: null }, items: [] };
    this.bySha = new Map();
    this.writing = Promise.resolve();
  }

  async load() {
    try {
      const parsed = JSON.parse(await readFile(this.file, "utf8"));
      this.data = { meta: { repo: null, lastSync: null, ...parsed.meta }, items: parsed.items ?? [] };
    } catch (e) {
      if (e.code !== "ENOENT") throw e;
    }
    this.bySha = new Map(this.data.items.map((c) => [c.sha, c]));
    return this;
  }

  get(sha) { return this.bySha.get(sha); }
  has(sha) { return this.bySha.has(sha); }

  /** Puts the commits that are not known yet on top (they are newer), and records when the history was last read. */
  merge(newer, repo, at) {
    if (this.data.meta.repo !== repo) { this.data = { meta: { repo: null, lastSync: null }, items: [] }; this.bySha = new Map(); }
    const fresh = newer.filter((c) => !this.bySha.has(c.sha));
    this.data = { meta: { repo, lastSync: at }, items: [...fresh, ...this.data.items] };
    this.bySha = new Map(this.data.items.map((c) => [c.sha, c]));
    return fresh.length;
  }

  save() {
    this.writing = this.writing.then(async () => {
      await mkdir(this.file.replace(/[\\/][^\\/]*$/, ""), { recursive: true });
      const tmp = `${this.file}.tmp`;
      await writeFile(tmp, JSON.stringify(this.data));
      await rename(tmp, this.file);
    });
    return this.writing;
  }
}

/**
 * Reads the history from the newest commit back until it reaches one already stored: a first sync walks everything,
 * the later ones read one page. `fetchPage(cursor)` returns { items (newest first), next (cursor or null) }.
 */
export async function syncCommits({ store, repo, fetchPage, now = Date.now }) {
  const fresh = [];
  let cursor = null;
  const own = store.data.meta.repo === repo;
  for (;;) {
    const page = await fetchPage(cursor);
    let reached = false;
    for (const c of page.items) {
      if (own && store.has(c.sha)) { reached = true; break; }
      fresh.push(c);
    }
    if (reached || !page.next) break;
    cursor = page.next;
  }
  const added = store.merge(fresh, repo, new Date(now()).toISOString());
  await store.save();
  return { added, total: store.data.items.length };
}

/** The page reader for a GitHub repository (GraphQL history of the default branch). */
export function githubHistoryPager({ graphql, owner, name }) {
  return async (cursor) => {
    const data = await graphql(HISTORY_QUERY, { owner, name, cursor });
    const history = data.repository?.defaultBranchRef?.target?.history;
    if (!history) return { items: [], next: null };
    return {
      items: history.nodes.map((c) => ({
        sha: c.oid, title: c.messageHeadline, author: c.author?.user?.login ?? c.author?.name ?? null,
        at: c.committedDate, additions: c.additions, deletions: c.deletions,
      })),
      next: history.pageInfo.hasNextPage ? history.pageInfo.endCursor : null,
    };
  };
}

const GITHUB_STATUS = { added: "added", removed: "deleted", modified: "modified", renamed: "renamed", copied: "added", changed: "modified" };

/** The info (title, author, date) and the files of one commit on GitHub (REST), normalized. */
export async function githubCommitFiles({ token, owner, name, sha, fetchImpl = fetch }) {
  const res = await fetchImpl(`${githubEndpoints().rest}/repos/${owner}/${name}/commits/${sha}`, {
    headers: { authorization: `Bearer ${token}`, accept: "application/vnd.github+json", "user-agent": "issue-timeline" },
  });
  if (!res.ok) throw new Error(`GitHub ${res.status} reading commit ${sha.slice(0, 10)}: ${(await res.text()).slice(0, 160)}`);
  const body = await res.json();
  const info = {
    title: (body.commit?.message ?? "").split("\n")[0], author: body.author?.login ?? body.commit?.author?.name ?? null,
    at: body.commit?.committer?.date ?? body.commit?.author?.date ?? null,
  };
  const files = (body.files ?? []).map((f) => ({
    path: f.filename, oldPath: f.previous_filename ?? null, status: GITHUB_STATUS[f.status] ?? "modified",
    additions: f.additions, deletions: f.deletions, patch: f.patch ?? null,
  }));
  return { info, files };
}

/** Counts the added and removed lines of a patch (its lines start with + or -; the patches carry no file headers). */
export function countLines(patch) {
  let additions = 0;
  let deletions = 0;
  for (const line of patch.split("\n")) {
    if (line.startsWith("+")) additions++;
    else if (line.startsWith("-")) deletions++;
  }
  return { additions, deletions };
}

/** Fills in the stats from the patches and caps the size of what is cached and sent. */
export function finishFiles(files) {
  let budget = MAX_DIFF;
  let truncated = false;
  const out = files.map((f) => {
    const patch = f.patch ?? null;
    const stats = patch != null ? countLines(patch) : { additions: f.additions ?? 0, deletions: f.deletions ?? 0 };
    let text = patch;
    if (text != null && (text.length > MAX_FILE_PATCH || text.length > budget)) { text = null; truncated = true; }
    if (text != null) budget -= text.length;
    const note = patch == null
      ? (f.tooLarge ? "Too large to show." : "No text diff (binary, or not included by the host).")
      : text == null ? "Diff omitted: too large." : null;
    return { path: f.path, oldPath: f.oldPath ?? null, status: f.status, additions: stats.additions, deletions: stats.deletions, patch: text, note };
  });
  return { files: out, truncated };
}

export function diffDirFor(storeDir, repo) {
  return join(storeDir, "diffs", createHash("sha256").update(repo).digest("hex").slice(0, 16));
}

export async function readCachedDiff(dir, sha) {
  try { return JSON.parse(await readFile(join(dir, `${sha}.json`), "utf8")); } catch { return null; }
}

export async function writeCachedDiff(dir, sha, value) {
  await mkdir(dir, { recursive: true });
  await writeFile(join(dir, `${sha}.json`), JSON.stringify(value));
}

/**
 * How a merged PR landed on the default branch, from its merge commit: a merge commit has two parents; a squash is one
 * commit titled like the PR (with "(#N)"); a rebase is one commit per PR commit, each keeping its own title.
 */
export function classifyGitHubMerge({ parents, title, prTitle, number, prCommitTitles }) {
  if (parents >= 2) return "merge commit";
  if (title.includes(`(#${number})`) || title === prTitle) return "squash";
  if (prCommitTitles.includes(title)) return "rebase";
  return "single commit";
}

/** The merge method of a merged PR on GitHub (REST), or null while it has no merge commit. */
export async function githubMergeMethod({ token, owner, name, number, record, fetchImpl = fetch }) {
  const get = async (path) => {
    const res = await fetchImpl(`${githubEndpoints().rest}/repos/${owner}/${name}/${path}`, {
      headers: { authorization: `Bearer ${token}`, accept: "application/vnd.github+json", "user-agent": "issue-timeline" },
    });
    if (!res.ok) throw new Error(`GitHub ${res.status} reading ${path}: ${(await res.text()).slice(0, 160)}`);
    return res.json();
  };
  const pr = await get(`pulls/${number}`);
  if (!pr.merge_commit_sha) return null;
  const commit = await get(`commits/${pr.merge_commit_sha}`);
  const title = (commit.commit?.message ?? "").split("\n")[0];
  return classifyGitHubMerge({
    parents: commit.parents?.length ?? 0, title, prTitle: record.title, number,
    prCommitTitles: (record.commits ?? []).map((c) => c.message),
  });
}
