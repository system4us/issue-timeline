// CI runs, kept apart from the issues and the commits. Only what the timeline needs is summarized per pull request:
// whether its commits ran CI, the state of the last run and a link to it. The runs themselves stay in their own file
// and are not shown one by one. Dependabot, Pages and release/tag workflows are a separate category, never a PR's CI.

import { mkdir, readFile, rename, writeFile } from "node:fs/promises";
import { githubEndpoints } from "./github.js";
import { compile, vocabulary } from "./vocabulary.js";

export const CI_DAYS = Number(process.env.CI_DAYS ?? 90); // how far back the runs are kept
const CI_EVENTS = new Set(["pull_request", "push", "workflow_dispatch", "merge_group", "pull_request_target", "schedule"]);

const OTHER = compile(vocabulary).otherWorkflows;

/**
 * The category of a run: "ci", "dependabot", "pages", or "other" (releases, tags, deploys: the names in vocabulary.json).
 * Words are matched whole, so a workflow called "Stage" is not a tag.
 */
export function categorize({ name = "", path = "", event = "", actor = "" }) {
  if (/dependabot/i.test(`${path} ${actor}`)) return "dependabot";
  if (/pages/i.test(`${path} ${name}`)) return "pages";
  if (OTHER.test(`${name} ${path}`)) return "other";
  return CI_EVENTS.has(event) ? "ci" : "other";
}

const STATE = {
  success: "passed", failure: "failed", failed: "failed", skipped: "skipped", cancelled: "cancelled", canceled: "cancelled",
  timed_out: "failed", action_required: "pending", neutral: "passed", stale: "cancelled",
};
/** The state a run shows: "running" while it has no conclusion yet, otherwise the mapped conclusion. */
export function stateOf(run) {
  if (!run.conclusion) return "running";
  return STATE[run.conclusion] ?? run.conclusion;
}

/** GitHub Actions runs created since `since` (a date, yyyy-mm-dd), newest first. */
export async function githubRuns({ token, owner, name, since, fetchImpl = fetch }) {
  const day = since.slice(0, 10); // the API filters by date
  const out = [];
  for (let page = 1; page <= 100; page++) {
    const res = await fetchImpl(`${githubEndpoints().rest}/repos/${owner}/${name}/actions/runs?per_page=100&page=${page}&created=${encodeURIComponent(`>=${day}`)}`, {
      headers: { authorization: `Bearer ${token}`, accept: "application/vnd.github+json", "user-agent": "issue-timeline" },
    });
    if (!res.ok) throw new Error(`GitHub ${res.status} reading the Actions runs: ${(await res.text()).slice(0, 160)}`);
    const body = await res.json();
    for (const r of body.workflow_runs ?? []) {
      const run = {
        id: String(r.id), url: r.html_url, name: r.name ?? "", path: r.path ?? "", event: r.event ?? "",
        actor: r.actor?.login ?? "", branch: r.head_branch ?? null, sha: r.head_sha, status: r.status, conclusion: r.conclusion ?? null, at: r.created_at,
      };
      out.push({ ...run, category: categorize(run) });
    }
    if ((body.workflow_runs ?? []).length < 100) break;
  }
  return out;
}

// GitLab's pipeline "source" in the words GitHub uses for the same trigger. A merge request pipeline is the PR's CI.
const GITLAB_SOURCE = { merge_request_event: "pull_request", external_pull_request_event: "pull_request" };

/**
 * GitLab pipelines updated since `since` (ISO time), normalized like the GitHub runs. The branch goes in `branch`, not in
 * `path`: `categorize` reads `path` for the workflow file, and a branch named "pages-redesign" must not look like Pages.
 */
export function gitlabRunOf(p) {
  const event = GITLAB_SOURCE[p.source] ?? p.source ?? "";
  const run = { id: String(p.id), url: p.web_url, name: "pipeline", path: "", event, actor: "", branch: p.ref ?? null, sha: p.sha, status: p.status, conclusion: p.status, at: p.created_at };
  return { ...run, category: categorize(run) };
}

/** Each pull request: the state of its last CI run, with the link to it. Only "ci" runs count; the others are summarized apart. */
export function summarize({ runs, pulls }) {
  const prOfSha = new Map();
  for (const p of Object.values(pulls)) for (const c of p.commits ?? []) if (!prOfSha.has(c.oid)) prOfSha.set(c.oid, p.number);
  const byPr = {};
  for (const run of runs) {
    if (run.category !== "ci") continue;
    const pr = prOfSha.get(run.sha);
    if (!pr) continue;
    const entry = byPr[pr] ?? (byPr[pr] = { count: 0, last: null });
    entry.count++;
    if (!entry.last || run.at > entry.last.at) entry.last = run;
  }
  const out = {};
  for (const [pr, { count, last }] of Object.entries(byPr)) {
    out[pr] = { state: stateOf(last), url: last.url, at: last.at, count };
  }
  // Each other kind of workflow: how many runs, and the last one.
  const other = {};
  for (const run of runs) {
    if (run.category === "ci") continue;
    const entry = other[run.category] ?? (other[run.category] = { count: 0, last: null });
    entry.count++;
    if (!entry.last || run.at > entry.last.at) entry.last = { url: run.url, at: run.at, state: stateOf(run), name: run.name };
  }
  return { byPr: out, other, lastRunAt: runs.reduce((m, r) => (r.at > m ? r.at : m), "") || null };
}

/** The CI store: one JSON file with the runs of the window. */
export class CiStore {
  constructor(file) { this.file = file; this.data = { meta: { repo: null, lastSync: null }, runs: [], changes: [] }; this.writing = Promise.resolve(); }

  async load() {
    try {
      const parsed = JSON.parse(await readFile(this.file, "utf8"));
      this.data = { meta: { repo: null, lastSync: null, ...parsed.meta }, runs: parsed.runs ?? [], changes: parsed.changes ?? [] };
    } catch (e) { if (e.code !== "ENOENT") throw e; }
    return this;
  }

  /** Adds the runs read now (replacing the same ids), keeps the rest of the window (from `floor` on), and drops what fell out of it. */
  merge(repo, fresh, floor, at) {
    if (this.data.meta.repo !== repo) this.data = { meta: { repo: null, lastSync: null }, runs: [] };
    const seen = new Set(fresh.map((r) => r.id));
    const kept = this.data.runs.filter((r) => !seen.has(r.id) && r.at >= floor);
    this.data = { meta: { repo, lastSync: at }, runs: [...fresh.filter((r) => r.at >= floor), ...kept].sort((a, b) => (b.at > a.at ? 1 : -1)), changes: this.data.changes ?? [] };
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

/** The oldest run kept: CI_DAYS back. The window is always this, whatever the sync. */
export function windowFloor(now) {
  return new Date(now - CI_DAYS * 86_400_000).toISOString();
}

/** Where this sync starts reading the host: the window, or a day before the last sync (a later sync reads less). */
export function readFrom(now, meta) {
  const floor = Date.parse(windowFloor(now));
  const incremental = meta.lastSync ? Date.parse(meta.lastSync) - 86_400_000 : floor;
  return new Date(Math.max(incremental, floor)).toISOString();
}

/** The commits that changed the CI definition on GitHub (the workflow files) since `since`, newest first. */
export async function githubWorkflowChanges({ token, owner, name, since, fetchImpl = fetch }) {
  const out = [];
  for (let page = 1; page <= 10; page++) {
    const res = await fetchImpl(`${githubEndpoints().rest}/repos/${owner}/${name}/commits?path=${encodeURIComponent(".github/workflows")}&since=${encodeURIComponent(since)}&per_page=100&page=${page}`, {
      headers: { authorization: `Bearer ${token}`, accept: "application/vnd.github+json", "user-agent": "issue-timeline" },
    });
    if (!res.ok) throw new Error(`GitHub ${res.status} reading the workflow commits: ${(await res.text()).slice(0, 160)}`);
    const rows = await res.json();
    for (const c of rows) {
      out.push({
        sha: c.sha, title: (c.commit?.message ?? "").split("\n")[0], author: c.author?.login ?? c.commit?.author?.name ?? null,
        at: c.commit?.committer?.date ?? c.commit?.author?.date ?? null, url: c.html_url,
      });
    }
    if (rows.length < 100) break;
  }
  return out;
}
