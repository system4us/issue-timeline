// GitLab REST adapter. glab supplies the authenticated token; records are normalized to the UI's issue/PR shape.
import { spawnSync } from "node:child_process";
import { gitlabRunOf } from "./ci.js";

const PAGE_SIZE = 100;
const REQUEST_TIMEOUT_MS = 60_000;
const MARGIN_MS = 2 * 60 * 1000;
const authCache = new Map();

export function parseGitLabRepo(key) {
  const value = key.startsWith("gitlab:") ? key.slice("gitlab:".length) : key;
  const input = value.includes("://") ? new URL(value) : new URL(`https://gitlab.com/${value}`);
  const host = input.host;
  const project = decodeURIComponent(input.pathname.replace(/^\/+|\/+$/g, ""));
  if (!host || !project || project.split("/").some((s) => !s) || !/^[\w.-]+(?::\d+)?$/.test(host)) throw new Error("Invalid GitLab repository. Use group/project or the full URL.");
  return { host, project, origin: input.origin, apiBase: `${input.protocol}//${host}/api/v4` };
}

/**
 * The token for a host: GITLAB_TOKEN_<HOST> (for example GITLAB_TOKEN_GITLAB_MYCOMPANY_COM), then GITLAB_TOKEN, then the
 * glab session. An environment token needs no glab, so it works in containers, CI and on machines without glab.
 */
export function tokenFromEnv(host, env = process.env) {
  const specific = `GITLAB_TOKEN_${host.replace(/[^A-Za-z0-9]/g, "_").toUpperCase()}`;
  return env[specific] || (host === "gitlab.com" ? env.GITLAB_TOKEN : undefined) || null;
}

function getAuth(host) {
  const cached = authCache.get(host);
  if (cached && cached.expiresAt > Date.now()) return cached;
  const fromEnv = tokenFromEnv(host);
  if (fromEnv) {
    const auth = { token: fromEnv, apiBase: `https://${host}/api/v4`, expiresAt: Date.now() + 20 * 60 * 1000 };
    authCache.set(host, auth);
    return auth;
  }
  const result = spawnSync("glab", ["auth", "status", "--hostname", host, "--show-token"], { encoding: "utf8", timeout: 15_000, maxBuffer: 1024 * 1024 });
  const output = `${result.stdout ?? ""}\n${result.stderr ?? ""}`;
  const token = output.match(/Token found[^:\n]*:\s*(\S+)/i)?.[1];
  const apiBase = output.match(/REST API Endpoint:\s*(https?:\/\/\S+)/i)?.[1]?.replace(/\/+$/, "");
  if (!token || /^\*+$/.test(token)) throw new Error(`Could not read the glab session for ${host}. Run glab auth login --hostname ${host}.`);
  // glab can print the credential even if its optional API health check times out.
  const auth = { token, apiBase: apiBase ?? `https://${host}/api/v4`, expiresAt: Date.now() + 20 * 60 * 1000 };
  authCache.set(host, auth);
  return auth;
}

function createApi({ token, apiBase, fetchImpl = fetch, log = () => {}, invalidate = () => {} }) {
  let eventCooldownUntil = 0;
  let eventLimitLogged = false;
  return async (path, query = {}) => {
    const eventEndpoint = /\/resource_(?:state|label|milestone)_events(?:\/|$)/.test(path);
    if (eventEndpoint && Date.now() < eventCooldownUntil) return { body: [], next: null };
    const url = new URL(`${apiBase}/${path.replace(/^\//, "")}`);
    for (const [key, value] of Object.entries(query)) if (value !== undefined && value !== null && value !== "") url.searchParams.set(key, String(value));
    for (let attempt = 1; ; attempt++) {
      // A request that never answers would stall the whole sync: give it a deadline, and retry like a server error.
      let response;
      try {
        response = await fetchImpl(url, { headers: { authorization: `Bearer ${token}`, "user-agent": "issue-timeline" }, signal: AbortSignal.timeout(REQUEST_TIMEOUT_MS) });
      } catch (e) {
        if (attempt < 4) { await new Promise((resolve) => setTimeout(resolve, attempt * 2000)); continue; }
        throw new Error(`GitLab request to ${url.pathname} failed after ${attempt} tries: ${e.name}`);
      }
      if (response.status === 429 && eventEndpoint) {
        eventCooldownUntil = Date.now() + (Number(response.headers.get("retry-after")) || 60) * 1000;
        if (!eventLimitLogged) { log("GitLab rate-limited the events endpoints; skipping those events during the cooldown and continuing with issues and merge requests."); eventLimitLogged = true; }
        return { body: [], next: null };
      }
      // Rate limits (429) are waited out with the server's Retry-After, longer than a transient error (5xx).
      if ((response.status === 429 && attempt < 10) || (response.status >= 500 && attempt < 4)) {
        const seconds = Number(response.headers.get("retry-after")) || attempt * 2;
        await new Promise((resolve) => setTimeout(resolve, seconds * 1000));
        continue;
      }
      // Before the budget runs out, wait for the window to reset instead of getting refused.
      const remaining = Number(response.headers.get("ratelimit-remaining"));
      const reset = Number(response.headers.get("ratelimit-reset"));
      if (Number.isFinite(remaining) && remaining <= 2 && reset > Date.now() / 1000) {
        await new Promise((resolve) => setTimeout(resolve, Math.min(reset - Date.now() / 1000, 120) * 1000));
      }
      if (!response.ok) {
        if (response.status === 401) invalidate();
        if (response.status === 404) return null;
        throw new Error(`GitLab ${response.status} at ${url.pathname}: ${(await response.text()).slice(0, 180)}`);
      }
      return { body: await response.json(), next: response.headers.get("x-next-page") };
    }
  };
}

async function allPages(api, path, query = {}) {
  const out = [];
  let page = 1;
  for (;;) {
    const result = await api(path, { ...query, page, per_page: PAGE_SIZE });
    if (!result) return out;
    if (Array.isArray(result.body)) out.push(...result.body);
    if (!result.next) return out;
    page = Number(result.next);
    if (!Number.isFinite(page) || page < 1) return out;
  }
}

async function optionalPages(api, path, query = {}) {
  try { return await allPages(api, path, query); } catch (e) {
    if (/GitLab (?:400|403|404|405|429) /.test(String(e.message))) return [];
    throw e;
  }
}

const user = (u) => u?.username ?? u?.name ?? null;
const iso = (s) => s ?? null;
const state = (s) => s === "merged" ? "MERGED" : s === "opened" ? "OPEN" : "CLOSED";

function mappedEvents(stateEvents, labelEvents, milestoneEvents = []) {
  const out = [];
  for (const e of stateEvents) {
    const type = e.state === "closed" ? "Closed" : e.state === "merged" ? "Merged" : "Reopened";
    out.push({ type, at: e.created_at, actor: user(e.user), reason: null });
  }
  for (const e of labelEvents) out.push({ type: e.action === "remove" ? "Unlabeled" : "Labeled", at: e.created_at, actor: user(e.user), label: e.label?.name ?? e.label?.title ?? e.label?.name });
  for (const e of milestoneEvents) out.push({ type: e.action === "remove" ? "Demilestoned" : "Milestoned", at: e.created_at, actor: user(e.user), milestone: e.milestone?.title ?? e.milestone?.name });
  return out.filter((e) => e.at).sort((a, b) => Date.parse(a.at) - Date.parse(b.at));
}

function normalizeNotes(notes) {
  return notes.filter((n) => !n.system && n.body).map((n) => ({ id: String(n.id), author: user(n.author), createdAt: n.created_at, body: n.body }));
}

function normalizeLabels(labels, colors) {
  return (labels ?? []).map((name) => ({ name: typeof name === "string" ? name : name.name, color: (colors.get(typeof name === "string" ? name : name.name) ?? "b6c5d2").replace(/^#/, "") }));
}

function common(record, kind, notes, events, colors) {
  const milestone = record.milestone;
  const comments = normalizeNotes(notes);
  return {
    kind,
    id: String(record.id),
    number: record.iid,
    title: record.title,
    body: record.description ?? "",
    url: record.web_url,
    author: user(record.author),
    createdAt: record.created_at,
    updatedAt: record.updated_at,
    closedAt: iso(record.closed_at),
    labels: normalizeLabels(record.labels, colors),
    assignees: (record.assignees ?? (record.assignee ? [record.assignee] : [])).map(user).filter(Boolean),
    milestone: milestone ? { title: milestone.title, dueOn: milestone.due_date ?? null } : null,
    commentsCount: Number(record.user_notes_count ?? comments.length),
    comments,
    events,
  };
}

async function mapLimit(items, limit, fn) {
  const out = new Array(items.length);
  let cursor = 0;
  await Promise.all(Array.from({ length: Math.min(limit, items.length) }, async () => {
    for (;;) {
      const i = cursor++;
      if (i >= items.length) return;
      out[i] = await fn(items[i]);
    }
  }));
  return out;
}

/**
 * GitLab's issue links into the blocker relation of the UI: "is_blocked_by" makes the other issue a blocker of this one,
 * "blocks" makes it blocked. "relates_to" is not a dependency and is left out.
 */
export function gitlabIssueRelations(links) {
  const blockedBy = [], blocking = [];
  for (const l of links ?? []) {
    const n = Number(l.iid);
    if (!Number.isFinite(n)) continue;
    if (l.link_type === "is_blocked_by") blockedBy.push(n);
    else if (l.link_type === "blocks") blocking.push(n);
  }
  return { blockedBy, blocking };
}

async function loadIssue(api, project, record, colors) {
  const base = `projects/${project}/issues/${record.iid}`;
  const [notes, states, labels, milestones, links] = await Promise.all([
    optionalPages(api, `${base}/notes`), optionalPages(api, `${base}/resource_state_events`),
    optionalPages(api, `${base}/resource_label_events`), optionalPages(api, `${base}/resource_milestone_events`),
    optionalPages(api, `${base}/links`),
  ]);
  return {
    ...common(record, "issue", notes, mappedEvents(states, labels, milestones), colors),
    state: state(record.state), stateReason: null, parent: null, subIssues: [],
    ...gitlabIssueRelations(links),
  };
}

async function loadMergeRequest(api, project, record, colors) {
  const base = `projects/${project}/merge_requests/${record.iid}`;
  const [notes, states, labels, commits, approvals, closes, changes] = await Promise.all([
    optionalPages(api, `${base}/notes`), optionalPages(api, `${base}/resource_state_events`),
    optionalPages(api, `${base}/resource_label_events`), optionalPages(api, `${base}/commits`),
    api(`${base}/approvals`).then((r) => r?.body).catch(() => null),
    optionalPages(api, `${base}/closes_issues`), api(`${base}/changes`).then((r) => r?.body).catch(() => null),
  ]);
  const diffList = changes?.changes ?? [];
  const additions = diffList.reduce((n, f) => n + ((f.diff?.match(/^\+(?!\+\+)/gm) ?? []).length), 0);
  const deletions = diffList.reduce((n, f) => n + ((f.diff?.match(/^-(?!--)/gm) ?? []).length), 0);
  const reviews = (approvals?.approved_by ?? []).map((approval) => ({ author: user(approval.user), state: "APPROVED", at: approval.approved_at ?? null, body: "" }));
  const stateEvents = [...states];
  if (record.merged_at && !stateEvents.some((e) => e.state === "merged")) stateEvents.push({ state: "merged", created_at: record.merged_at, user: record.merge_user ?? record.merged_by });
  return {
    ...common(record, "pull", notes, mappedEvents(stateEvents, labels), colors),
    state: state(record.state), isDraft: !!(record.draft ?? record.work_in_progress),
    mergedAt: iso(record.merged_at), mergedBy: user(record.merge_user ?? record.merged_by),
    reviewDecision: reviews.length ? "APPROVED" : null,
    base: record.target_branch ?? "", head: record.source_branch ?? "",
    additions, deletions, changedFiles: diffList.length,
    closes: closes.map((issue) => issue.iid).filter(Number.isFinite),
    // The commit this MR left on the target branch (a merge or a squash). It is how a commit of the branch finds its MR.
    mergeCommitSha: record.merge_commit_sha ?? record.squash_commit_sha ?? null,
    commitsCount: commits.length,
    commits: commits.map((c) => ({ oid: c.id, message: c.title ?? c.message, at: c.committed_date ?? c.created_at, author: c.author?.username ?? c.author_name ?? null })),
    reviews,
  };
}

const HIERARCHY_QUERY = `query ($path: ID!, $after: String) { project(fullPath: $path) { workItems(first: 100, after: $after, state: all) {
  pageInfo { hasNextPage endCursor } nodes { iid workItemType { name } widgets { __typename ... on WorkItemWidgetHierarchy {
    parent { iid title webUrl workItemType { name } } children(first: 100) { nodes { iid } } } } } } } }`;

/**
 * The hierarchy of the project's work items (GitLab's issues, tasks and their parents), by iid: `parent` is the item one
 * level up, an issue or an epic of the group (epics are not in the REST issues, so they come from here), and `children`.
 * One GraphQL request per 100 work items. Free tiers have it; a server without work items returns an error.
 */
export async function gitlabHierarchy({ repoKey, fetchImpl = fetch }) {
  const { host, project, origin } = parseGitLabRepo(repoKey);
  const { token } = getAuth(host);
  const out = new Map();
  let after = null;
  for (let pages = 0; pages < 500; pages++) {
    const res = await fetchImpl(`${origin}/api/graphql`, {
      method: "POST",
      headers: { authorization: `Bearer ${token}`, "content-type": "application/json", "user-agent": "issue-timeline" },
      body: JSON.stringify({ query: HIERARCHY_QUERY, variables: { path: project, after } }),
      signal: AbortSignal.timeout(REQUEST_TIMEOUT_MS),
    });
    if (!res.ok) throw new Error(`GitLab GraphQL ${res.status}`);
    const body = await res.json();
    if (body.errors?.length) throw new Error(`GitLab GraphQL: ${body.errors[0].message}`);
    const items = body.data?.project?.workItems;
    if (!items) break;
    for (const n of items.nodes) {
      const hier = (n.widgets ?? []).find((w) => w.__typename === "WorkItemWidgetHierarchy") ?? {};
      const p = hier.parent;
      out.set(Number(n.iid), {
        type: n.workItemType?.name ?? null,
        parent: p ? { iid: Number(p.iid), title: p.title, url: p.webUrl, type: p.workItemType?.name ?? null } : null,
        children: (hier.children?.nodes ?? []).map((c) => Number(c.iid)),
      });
    }
    if (!items.pageInfo?.hasNextPage) break;
    after = items.pageInfo.endCursor;
  }
  return out;
}

/**
 * Writes the hierarchy onto the stored issues: `parent` when the parent is an issue of this project, `parentEpic` when it
 * is an epic (or anything else outside the issues), and `subIssues` for the children that are issues here.
 */
export function applyHierarchy(issues, hierarchy) {
  for (const issue of Object.values(issues)) {
    const h = hierarchy.get(Number(issue.number));
    const p = h?.parent ?? null;
    const isIssueHere = p && p.type !== "Epic" && !!issues[p.iid];
    issue.parent = isIssueHere ? p.iid : null;
    issue.parentEpic = p && !isIssueHere ? { iid: p.iid, title: p.title, url: p.url, type: p.type } : null;
    issue.subIssues = (h?.children ?? []).filter((n) => !!issues[n]);
  }
  return issues;
}

export async function syncGitLab({ repoKey, store, full = false, now = Date.now, log = () => {}, onProgress = () => {} }) {
  const { host, project } = parseGitLabRepo(repoKey);
  const { token, apiBase } = getAuth(host);
  const api = createApi({ token, apiBase, log, invalidate: () => authCache.delete(host) });
  const encodedProject = encodeURIComponent(project);
  const t0 = now();
  const startedAt = new Date(t0).toISOString();
  const doFull = full || !store.data.meta.fullSyncAt || store.data.meta.repo !== repoKey;
  if (store.data.meta.repo && store.data.meta.repo !== repoKey) { store.data.issues = {}; store.data.pulls = {}; }
  const updatedAfter = doFull ? undefined : new Date(Date.parse(store.data.meta.lastSync) - MARGIN_MS).toISOString();
  onProgress({ phase: "Listing issues and MRs", done: 0, total: null });
  const [rawIssues, rawMRs, labels] = await Promise.all([
    allPages(api, `projects/${encodedProject}/issues`, { order_by: "updated_at", sort: "desc", updated_after: updatedAfter }),
    allPages(api, `projects/${encodedProject}/merge_requests`, { state: "all", order_by: "updated_at", sort: "desc", updated_after: updatedAfter }),
    allPages(api, `projects/${encodedProject}/labels`),
  ]);
  const colors = new Map(labels.map((l) => [l.name, l.color ?? "b6c5d2"]));
  const total = rawIssues.length + rawMRs.length;
  let done = 0;
  onProgress({ phase: "Downloading issues and MRs", done, total });
  const issues = await mapLimit(rawIssues, 3, async (r) => {
    const item = await loadIssue(api, encodedProject, r, colors);
    onProgress({ phase: "Downloading issues and MRs", done: ++done, total });
    return item;
  });
  const pulls = await mapLimit(rawMRs, 2, async (r) => {
    const item = await loadMergeRequest(api, encodedProject, r, colors);
    onProgress({ phase: "Downloading issues and MRs", done: ++done, total });
    return item;
  });
  // GitLab's links are not in the issue's own record, and adding one does not always move its updated_at: on an incremental
  // sync the links of the open issues that did not come back are read again, so a new blocker shows up.
  if (!doFull) {
    const came = new Set(rawIssues.map((r) => String(r.iid)));
    const open = Object.values(store.data.issues).filter((i) => i.state === "OPEN" && !came.has(String(i.number)));
    await mapLimit(open, 3, async (i) => {
      const links = await optionalPages(api, `projects/${encodedProject}/issues/${i.number}/links`);
      Object.assign(i, gitlabIssueRelations(links));
    });
  }
  store.upsert("issues", issues);
  store.upsert("pulls", pulls);
  // Parents, children and epics come from the work items, not from the issue records: read them for every stored issue.
  try {
    applyHierarchy(store.data.issues, await gitlabHierarchy({ repoKey, fetchImpl: undefined }));
  } catch (e) {
    log(`GitLab work items unavailable, hierarchy not updated: ${e.message}`);
  }
  const meta = store.data.meta;
  meta.repo = repoKey;
  meta.lastSync = startedAt;
  if (doFull) meta.fullSyncAt = startedAt;
  meta.totals = { issues: Object.keys(store.data.issues).length, pulls: Object.keys(store.data.pulls).length };
  const entry = { at: startedAt, full: doFull, issues: issues.length, pulls: pulls.length, ms: now() - t0 };
  meta.syncs = [...meta.syncs, entry].slice(-50);
  await store.save();
  log(`GitLab ${doFull ? "full" : "incremental"}: ${entry.issues} issues, ${entry.pulls} merge requests`);
  return entry;
}

/** One page of the default branch's commits (newest first). `page` is the cursor: the next page number, or null. */
export async function gitlabCommitPage({ repoKey, page }) {
  const { host, project } = parseGitLabRepo(repoKey);
  const { token, apiBase } = getAuth(host);
  const api = createApi({ token, apiBase, invalidate: () => authCache.delete(host) });
  const result = await api(`projects/${encodeURIComponent(project)}/repository/commits`, { per_page: PAGE_SIZE, page: page ?? 1 });
  if (!result) return { items: [], next: null };
  return {
    items: result.body.map((c) => ({
      sha: c.id, title: c.title ?? c.message ?? "", author: c.author_name ?? null, at: c.committed_date ?? c.created_at ?? null,
      additions: null, deletions: null,
    })),
    next: result.next ? Number(result.next) : null,
  };
}

/** The info and the files of one commit on GitLab, normalized like the GitHub ones (`patch` is the text diff). */
export async function gitlabCommitFiles({ repoKey, sha }) {
  const { host, project } = parseGitLabRepo(repoKey);
  const { token, apiBase } = getAuth(host);
  const api = createApi({ token, apiBase, invalidate: () => authCache.delete(host) });
  const base = `projects/${encodeURIComponent(project)}/repository/commits/${sha}`;
  const commit = await api(base);
  const diffs = await allPages(api, `${base}/diff`);
  const info = commit?.body ? { title: commit.body.title ?? "", author: commit.body.author_name ?? null, at: commit.body.committed_date ?? null } : null;
  const files = diffs.map((d) => ({
    path: d.new_path, oldPath: d.renamed_file ? d.old_path : null,
    status: d.new_file ? "added" : d.deleted_file ? "deleted" : d.renamed_file ? "renamed" : "modified",
    patch: d.diff ?? null, tooLarge: !!d.too_large,
  }));
  return { info, files };
}

/** The merge method of a merged merge request on GitLab: squash, merge commit, or rebase (a fast-forward or rebased merge). */
export async function gitlabMergeMethod({ repoKey, iid }) {
  const { host, project } = parseGitLabRepo(repoKey);
  const { token, apiBase } = getAuth(host);
  const api = createApi({ token, apiBase, invalidate: () => authCache.delete(host) });
  const result = await api(`projects/${encodeURIComponent(project)}/merge_requests/${iid}`);
  const mr = result?.body;
  if (!mr || mr.state !== "merged") return null;
  if (mr.squash_commit_sha) return "squash";
  if (mr.merge_commit_sha) return "merge commit";
  return "rebase";
}

/** Whether the project exists and the glab login can see it (the host answers 404 otherwise). */
export async function gitlabProjectExists(repoKey) {
  const { host, project } = parseGitLabRepo(repoKey);
  const { token, apiBase } = getAuth(host);
  const api = createApi({ token, apiBase, invalidate: () => authCache.delete(host) });
  return !!(await api(`projects/${encodeURIComponent(project)}`));
}

/** The pipelines of the project updated since `since` (ISO time), newest first, as CI runs. */
export async function gitlabPipelines({ repoKey, since }) {
  const { host, project } = parseGitLabRepo(repoKey);
  const { token, apiBase } = getAuth(host);
  const api = createApi({ token, apiBase, invalidate: () => authCache.delete(host) });
  const rows = await allPages(api, `projects/${encodeURIComponent(project)}/pipelines`, { updated_after: since, order_by: "updated_at", sort: "desc" });
  return rows.map(gitlabRunOf);
}

/** The commits that changed the CI definition (.gitlab-ci.yml) since `since`, newest first. */
export async function gitlabWorkflowChanges({ repoKey, since }) {
  const { host, project } = parseGitLabRepo(repoKey);
  const { token, apiBase } = getAuth(host);
  const api = createApi({ token, apiBase, invalidate: () => authCache.delete(host) });
  const rows = await allPages(api, `projects/${encodeURIComponent(project)}/repository/commits`, { path: ".gitlab-ci.yml", since });
  return rows.map((c) => ({ sha: c.id, title: c.title ?? "", author: c.author_name ?? null, at: c.committed_date ?? c.created_at ?? null, url: c.web_url ?? null }));
}
