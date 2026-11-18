#!/usr/bin/env node
// Read-only issue and pull/merge request timeline for GitHub and GitLab repositories.
import { createServer } from "node:http";
import { mkdir, readFile, rename, writeFile } from "node:fs/promises";
import { extname, join, normalize } from "node:path";
import { createHash } from "node:crypto";
import { fileURLToPath } from "node:url";
import { Store } from "./store.js";
import { createClient, resolveToken } from "./github.js";
import { gitlabCommitFiles, gitlabCommitPage, gitlabMergeMethod, gitlabPipelines, gitlabProjectExists, gitlabWorkflowChanges, parseGitLabRepo, syncGitLab } from "./gitlab.js";
import { sync } from "./sync.js";
import { buildPayload } from "./derive.js";
import { buildCommitLinks } from "./links.js";
import { CiStore, summarize, windowFloor, readFrom, githubRuns, githubWorkflowChanges } from "./ci.js";
import { CommitStore, FULL_SHA, diffDirFor, finishFiles, githubCommitFiles, githubHistoryPager, githubMergeMethod, readCachedDiff, syncCommits, writeCachedDiff } from "./commits.js";

const root = fileURLToPath(new URL("../..", import.meta.url));
const port = Number(process.env.PORT ?? 4310);
const host = process.env.HOST ?? "127.0.0.1";
const intervalMs = Number(process.env.POLL_MINUTES ?? 5) * 60 * 1000;
// Data goes to ./data of the directory the command runs from (not inside the installed package).
const storeDir = process.env.STORE_DIR ?? join(process.cwd(), "data");
const repoConfigFile = join(storeDir, "repos.json");
const webDir = join(root, "web", "dist");
const entries = new Map();
const mergeMethods = new Map(); // "repo#number" -> how the PR was merged (immutable once known)
const savedRepos = await readFile(repoConfigFile, "utf8").then((s) => JSON.parse(s)).catch(() => []);
const validRepoKey = (r) => {
  if (typeof r !== "string") return false;
  try { if (r.startsWith("gitlab:")) { parseGitLabRepo(r); return true; } return /^[\w.-]+\/[\w.-]+$/.test(r); }
  catch { return false; }
};
let repos = [...new Set((Array.isArray(savedRepos) ? savedRepos : []).filter(validRepoKey))];
const storeFileFor = (repo) => repo.startsWith("gitlab:")
  ? join(storeDir, `gitlab-${createHash("sha256").update(repo).digest("hex").slice(0, 16)}.json`)
  : join(storeDir, `${repo.replace("/", "__")}.json`);
let graphql;
let token;
function getToken() {
  if (!token) token = resolveToken();
  return token;
}
function getGraphql() {
  if (!graphql) graphql = createClient({ token: getToken() });
  return graphql;
}
// The commits of a repo live next to its store file: <store>.commits.json.
const commitsFileOf = (storeFile) => storeFile.replace(/\.json$/, ".commits.json");
const ciFileOf = (storeFile) => storeFile.replace(/\.json$/, ".ci.json");
const ownerAndName = (repo) => repo.split("/");

// A repository is only saved once the host confirms it exists and the login can read it.
async function checkRepoExists(repo) {
  if (repo.startsWith("gitlab:")) {
    if (!(await gitlabProjectExists(repo))) throw new Error(`GitLab project not found, or the glab login cannot see it: ${repo.slice("gitlab:".length)}`);
    return;
  }
  const [owner, name] = ownerAndName(repo);
  try {
    const data = await getGraphql()("query($owner: String!, $name: String!) { repository(owner: $owner, name: $name) { id } }", { owner, name });
    if (!data.repository) throw new Error("not found");
  } catch (e) {
    if (/Could not resolve|not found/i.test(String(e.message))) throw new Error(`GitHub repository not found, or no access to it: ${repo}`);
    throw new Error(`Could not check ${repo} on GitHub: ${e.message}`);
  }
}

async function addConfiguredRepo(repo) {
  const store = await new Store(process.env.STORE_FILE && repos.length === 0 ? process.env.STORE_FILE : storeFileFor(repo)).load();
  const commits = await new CommitStore(commitsFileOf(store.file)).load();
  const ci = await new CiStore(ciFileOf(store.file)).load();
  const state = { syncing: false, lastError: null, nextSyncAt: null, progress: null, commitsError: null, ciError: null };
  entries.set(repo, { store, state, commits, ci });
  repos = [...repos, repo];
  await mkdir(storeDir, { recursive: true });
  const tmp = `${repoConfigFile}.tmp`;
  await writeFile(tmp, JSON.stringify(repos));
  await rename(tmp, repoConfigFile);
  runSync(repo);
}

for (const repo of repos) {
  const store = await new Store(process.env.STORE_FILE && repos.length === 1 ? process.env.STORE_FILE : storeFileFor(repo)).load();
  const commits = await new CommitStore(commitsFileOf(store.file)).load();
  const ci = await new CiStore(ciFileOf(store.file)).load();
  const state = { syncing: false, lastError: null, nextSyncAt: null, progress: null, commitsError: null, ciError: null };
  entries.set(repo, { store, state, commits, ci });
}

// The default branch's commits, read after the issues and PRs. Incremental: usually one page, the rest is stored.
// The CI runs of the window: GitHub Actions runs, or GitLab pipelines. Only summaries reach the page.
async function syncRepoCi(repo) {
  const { ci } = entries.get(repo);
  const now = Date.now();
  const since = readFrom(now, ci.data.meta);
  const fresh = repo.startsWith("gitlab:")
    ? await gitlabPipelines({ repoKey: repo, since })
    : await githubRuns({ token: getToken(), owner: ownerAndName(repo)[0], name: ownerAndName(repo)[1], since });
  ci.merge(repo, fresh, windowFloor(now), new Date(now).toISOString());
  // The commits that changed the CI definition in the window: the PR panel draws them as a line.
  ci.data.changes = repo.startsWith("gitlab:")
    ? await gitlabWorkflowChanges({ repoKey: repo, since: windowFloor(now) })
    : await githubWorkflowChanges({ token: getToken(), owner: ownerAndName(repo)[0], name: ownerAndName(repo)[1], since: windowFloor(now) });
  await ci.save();
  return { runs: ci.data.runs.length };
}

async function syncRepoCommits(repo) {
  const { commits } = entries.get(repo);
  const fetchPage = repo.startsWith("gitlab:")
    ? (cursor) => gitlabCommitPage({ repoKey: repo, page: cursor })
    : githubHistoryPager({ graphql: getGraphql(), owner: ownerAndName(repo)[0], name: ownerAndName(repo)[1] });
  return syncCommits({ store: commits, repo, fetchPage });
}

async function runSync(repo, full = false) {
  const { store, state } = entries.get(repo);
  if (state.syncing) return { skipped: true };
  state.syncing = true;
  state.progress = { phase: "Preparing", done: 0, total: null };
  try {
    const log = (m) => console.log(new Date().toISOString(), repo, m);
    const onProgress = (progress) => { state.progress = progress; };
    const r = repo.startsWith("gitlab:")
      ? await syncGitLab({ repoKey: repo, store, full, log, onProgress })
      : await sync({ graphql: getGraphql(), store, repo, full, log, onProgress });
    state.lastError = null;
    try {
      state.progress = { phase: "Reading commits", done: 0, total: null };
      await syncRepoCommits(repo);
      state.commitsError = null;
    } catch (e) {
      state.commitsError = String(e.message ?? e);
      console.error(new Date().toISOString(), repo, "commits sync failed:", state.commitsError);
    }
    try {
      state.progress = { phase: "Reading CI runs", done: 0, total: null };
      await syncRepoCi(repo);
      state.ciError = null;
    } catch (e) {
      state.ciError = String(e.message ?? e);
      console.error(new Date().toISOString(), repo, "CI sync failed:", state.ciError);
    }
    return r;
  } catch (e) {
    state.lastError = String(e.message ?? e);
    console.error(new Date().toISOString(), repo, "sync failed:", state.lastError);
    return { error: state.lastError };
  } finally {
    state.syncing = false;
    state.nextSyncAt = new Date(Date.now() + intervalMs).toISOString();
    state.progress = null;
  }
}

const json = (res, code, body) => { res.writeHead(code, { "content-type": "application/json; charset=utf-8", "cache-control": "no-store" }); res.end(JSON.stringify(body)); };
const MIME = { ".html": "text/html; charset=utf-8", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".json": "application/json" };
async function serveStatic(req, res, pathname) {
  const rel = normalize(pathname === "/" ? "/index.html" : pathname).replace(/^(\.\.[/\\])+/, "");
  try {
    const file = join(webDir, rel);
    if (!file.startsWith(webDir)) throw new Error("outside");
    const body = await readFile(file);
    res.writeHead(200, { "content-type": MIME[extname(file)] ?? "application/octet-stream" }); res.end(body);
  } catch {
    try {
      const body = await readFile(join(webDir, "index.html"));
      res.writeHead(200, { "content-type": MIME[".html"] }); res.end(body);
    } catch {
      res.writeHead(404, { "content-type": "text/plain" });
      res.end("The frontend is not built: run `npm run build` (or `npm run dev:web` for the dev server on :5173).");
    }
  }
}

const server = createServer(async (req, res) => {
  const url = new URL(req.url, "http://localhost");
  try {
    if (url.pathname === "/api/repos" && req.method === "GET") return json(res, 200, { repos });
    if (url.pathname === "/api/repos" && req.method === "POST") {
      let body = "";
      for await (const chunk of req) body += chunk;
      const input = JSON.parse(body);
      let repo;
      try {
        if (input.provider === "gitlab") {
          const source = input.repo?.trim() ?? "";
          const parsed = parseGitLabRepo(source);
          repo = `gitlab:${parsed.origin}/${parsed.project}`;
        } else {
          if (input.provider && input.provider !== "github") throw new Error("Unsupported provider");
          repo = (input.repo?.trim() ?? "").replace(/^https?:\/\/[^/]+\//, "").replace(/\.git$/, "").replace(/\/+$/, "");
          if (!/^[\w.-]+\/[\w.-]+$/.test(repo)) throw new Error("Use the owner/repo format");
        }
      } catch (e) { return json(res, 400, { error: e.message }); }
      if (entries.has(repo)) return json(res, 200, { repos, repo, added: false });
      try { await checkRepoExists(repo); } catch (e) { return json(res, 400, { error: e.message }); }
      await addConfiguredRepo(repo);
      return json(res, 201, { repos, repo, added: true });
    }
    const repo = url.searchParams.get("repo") ?? repos[0];
    const entry = entries.get(repo);
    if (url.pathname.startsWith("/api/") && !entry) return json(res, 400, { error: "repo not configured" });
    if (url.pathname === "/api/data" && req.method === "GET") return json(res, 200, { ...buildPayload(entry.store.data), ci: { ...summarize({ runs: entry.ci.data.runs, pulls: entry.store.data.pulls }), changes: entry.ci.data.changes ?? [] }, status: { ...entry.state } });
    const one = url.pathname.match(/^\/api\/(issues|pulls)\/(\d+)$/);
    if (one && req.method === "GET") {
      const record = entry.store.data[one[1]][one[2]];
      return record ? json(res, 200, record) : json(res, 404, { error: "not found" });
    }
    // The list of commits: only read when the Commits tab asks for it, a page at a time, filtered on the server.
    if (url.pathname === "/api/commits" && req.method === "GET") {
      const q = (url.searchParams.get("q") ?? "").trim().toLowerCase();
      const author = (url.searchParams.get("author") ?? "").trim().toLowerCase();
      const limit = Math.min(500, Math.max(1, Number(url.searchParams.get("limit")) || 100));
      const offset = Math.max(0, Number(url.searchParams.get("offset")) || 0);
      const pr = Number(url.searchParams.get("pr")) || null;
      const issue = Number(url.searchParams.get("issue")) || null;
      const { items, meta } = entry.commits.data;
      const { pulls, issues } = entry.store.data;
      // A PR's own commits join the list, even those that never reached the default branch.
      const known = new Set(items.map((c) => c.sha));
      const own = pr && pulls[pr]
        ? (pulls[pr].commits ?? []).filter((c) => !known.has(c.oid)).map((c) => ({ sha: c.oid, title: c.message, author: c.author, at: c.at, additions: null, deletions: null }))
        : [];
      const all = [...items, ...own];
      const links = buildCommitLinks({ commits: all, pulls, issues });
      const matches = all
        .map((c) => ({ ...c, ...links.get(c.sha), inHistory: known.has(c.sha) }))
        .filter((c) =>
          (!q || c.title.toLowerCase().includes(q) || c.sha.startsWith(q))
          && (!author || (c.author ?? "").toLowerCase().includes(author))
          && (!pr || c.pr === pr)
          && (!issue || c.issues.includes(issue)));
      return json(res, 200, { total: matches.length, synced: items.length, lastSync: meta.lastSync, items: matches.slice(offset, offset + limit) });
    }
    // One commit's files and diffs: fetched from the host the first time, then kept in the cache. Any commit of the repo
    // works, not only the default branch's: a PR's own commits (squashed or never merged there) are on the host too.
    if (url.pathname === "/api/commit" && req.method === "GET") {
      const sha = (url.searchParams.get("sha") ?? "").toLowerCase();
      const known = entry.commits.get(sha);
      if (!known && !FULL_SHA.test(sha)) return json(res, 404, { error: "not a full commit sha of this repo" });
      const dir = diffDirFor(storeDir, repo);
      let value = await readCachedDiff(dir, sha);
      if (!value) {
        const host = repo.startsWith("gitlab:")
          ? await gitlabCommitFiles({ repoKey: repo, sha })
          : await githubCommitFiles({ token: getToken(), owner: ownerAndName(repo)[0], name: ownerAndName(repo)[1], sha });
        value = { info: host.info, ...finishFiles(host.files) };
        await writeCachedDiff(dir, sha, value);
      }
      const info = known
        ? { title: known.title, author: known.author, at: known.at, additions: known.additions, deletions: known.deletions }
        : { title: value.info?.title ?? "", author: value.info?.author ?? null, at: value.info?.at ?? null, additions: null, deletions: null };
      const links = buildCommitLinks({ commits: [{ sha, title: info.title }], pulls: entry.store.data.pulls, issues: entry.store.data.issues }).get(sha);
      return json(res, 200, { sha, ...info, ...links, inHistory: !!known, files: value.files, truncated: value.truncated });
    }
    // How a merged PR landed (squash, merge commit, rebase...). Asked of the host once per PR, then kept in memory.
    if (url.pathname === "/api/merge" && req.method === "GET") {
      const number = Number(url.searchParams.get("number"));
      const record = entry.store.data.pulls[number];
      if (!record) return json(res, 404, { error: "not found" });
      if (!record.mergedAt) return json(res, 200, { method: null });
      const key = `${repo}#${number}`;
      if (!mergeMethods.has(key)) {
        const method = repo.startsWith("gitlab:")
          ? await gitlabMergeMethod({ repoKey: repo, iid: number })
          : await githubMergeMethod({ token: getToken(), owner: ownerAndName(repo)[0], name: ownerAndName(repo)[1], number, record });
        if (method) mergeMethods.set(key, method);
        return json(res, 200, { method });
      }
      return json(res, 200, { method: mergeMethods.get(key) });
    }
    if (url.pathname === "/api/sync" && req.method === "POST") {
      runSync(repo, url.searchParams.get("full") === "1");
      return json(res, 202, { started: !entry.state.syncing });
    }
    if (url.pathname.startsWith("/api/")) return json(res, 404, { error: "not found" });
    return serveStatic(req, res, decodeURIComponent(url.pathname));
  } catch (e) { console.error(e); return json(res, 500, { error: String(e.message ?? e) }); }
});

server.listen(port, host, () => console.log(`issue-timeline (${repos.join(", ") || "no repos"}): http://${host}:${port}  (stores: ${storeDir})`));
for (const [repo, { store, state }] of entries) {
  const stale = !store.data.meta.lastSync || Date.now() - Date.parse(store.data.meta.lastSync) > intervalMs;
  if (stale) runSync(repo);
  else state.nextSyncAt = new Date(Date.parse(store.data.meta.lastSync) + intervalMs).toISOString();
}
setInterval(() => { for (const repo of repos) runSync(repo); }, intervalMs).unref?.();
