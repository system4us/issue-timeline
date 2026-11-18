import test from "node:test";
import assert from "node:assert/strict";
import { mkdtemp, readFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { Store } from "../src/store.js";
import { sync } from "../src/sync.js";
import { buildPayload, deriveLinks, deriveTree, deriveDeps } from "../src/derive.js";

const conn = (nodes, more = null) => ({ totalCount: nodes.length + (more ? 1 : 0), pageInfo: { hasNextPage: !!more, endCursor: more }, nodes });
const issueNode = (n, updatedAt, extra = {}) => ({
  id: `I${n}`, number: n, title: `issue ${n}`, body: "b", url: `u${n}`, state: n % 2 ? "OPEN" : "CLOSED", stateReason: null,
  createdAt: "2026-01-01T00:00:00Z", updatedAt, closedAt: null, author: { login: "ana" },
  labels: { nodes: [{ name: "bug", color: "d73a4a" }] }, assignees: { nodes: [] }, milestone: null,
  comments: conn([]), timelineItems: conn([]), ...extra,
});
const pullNode = (n, updatedAt, extra = {}) => ({
  ...issueNode(n, updatedAt), merged: true, mergedAt: "2026-01-03T00:00:00Z", state: "MERGED", isDraft: false, mergedBy: { login: "ana" },
  reviewDecision: null, baseRefName: "main", headRefName: `b${n}`, additions: 3, deletions: 1, changedFiles: 1,
  closingIssuesReferences: { nodes: [{ number: 1 }] }, commits: conn([{ commit: { oid: "abc", messageHeadline: "m", committedDate: "2026-01-02T00:00:00Z", author: { user: { login: "ana" } } } }]),
  reviews: conn([{ author: { login: "bo" }, state: "APPROVED", submittedAt: "2026-01-02T12:00:00Z", body: "" }]), ...extra,
});

/** A fake GitHub: pages of issues and pulls newest first, and the follow-up node(id) reads. */
function fakeGraphql({ issues, pulls, extraComments = [] }) {
  const calls = [];
  // Like GitHub, a chain of cursors only holds the nodes of the requested states.
  const page = (all, size, after, states) => {
    const list = states ? all.filter((n) => states.includes(n.state)) : all;
    const start = after ? Number(after) : 0;
    const nodes = list.slice(start, start + size);
    const next = start + size < list.length ? String(start + size) : null;
    return { totalCount: list.length, pageInfo: { hasNextPage: !!next, endCursor: next }, nodes };
  };
  const fn = async (query, vars) => {
    calls.push(query.includes("node(id") ? "node" : query.includes("pullRequests") ? "pulls" : "issues");
    if (query.includes("node(id")) return { node: { comments: { pageInfo: { hasNextPage: false, endCursor: null }, nodes: extraComments } } };
    if (query.includes("pullRequests(")) return { repository: { pullRequests: page(pulls, 20, vars.after, vars.states) } };
    return { repository: { issues: page(issues, 30, vars.after, vars.states) } };
  };
  fn.calls = calls;
  return fn;
}

const newStore = async () => new Store(join(await mkdtemp(join(tmpdir(), "it-")), "s.json")).load();

test("a first sync walks every page and stores normalized issues and pulls", async () => {
  const issues = Array.from({ length: 65 }, (_, i) => issueNode(65 - i, `2026-02-${String(28 - (i % 20)).padStart(2, "0")}T00:00:00Z`));
  const gql = fakeGraphql({ issues, pulls: [pullNode(100, "2026-02-10T00:00:00Z")] });
  const store = await newStore();
  const r = await sync({ graphql: gql, store, repo: "o/r", now: () => Date.parse("2026-03-01T00:00:00Z") });
  assert.equal(r.issues, 65);
  assert.equal(r.pulls, 1);
  assert.equal(Object.keys(store.data.issues).length, 65);
  assert.equal(store.data.pulls[100].state, "MERGED");
  assert.deepEqual(store.data.pulls[100].closes, [1]);
  assert.equal(store.data.meta.fullSyncAt, "2026-03-01T00:00:00.000Z");
  // persisted to disk
  assert.equal(Object.keys(JSON.parse(await readFile(store.file, "utf8")).issues).length, 65);
});

test("an incremental sync stops at the first page older than the last sync", async () => {
  const fresh = Array.from({ length: 3 }, (_, i) => issueNode(100 - i, "2026-03-02T00:00:00Z"));
  const old = Array.from({ length: 60 }, (_, i) => issueNode(60 - i, "2026-01-05T00:00:00Z"));
  const gql = fakeGraphql({ issues: [...fresh, ...old], pulls: [] });
  const store = await newStore();
  store.data.meta = { ...store.data.meta, repo: "o/r", lastSync: "2026-03-01T00:00:00.000Z", fullSyncAt: "2026-02-01T00:00:00.000Z" };
  store.upsert("issues", [issueNode(1, "2026-01-05T00:00:00Z")]);
  const r = await sync({ graphql: gql, store, repo: "o/r", now: () => Date.parse("2026-03-03T00:00:00Z") });
  assert.equal(r.full, false);
  assert.equal(r.issues, 3, "only the three updated since the last sync are read");
  assert.equal(gql.calls.filter((c) => c === "issues").length, 2, "one page per state (open, closed), not the whole history");
  assert.ok(store.data.issues[100] && store.data.issues[1]);
});

test("a connection with more than one page is drained", async () => {
  const issues = [issueNode(1, "2026-02-01T00:00:00Z", { comments: conn([{ id: "c1", author: { login: "a" }, createdAt: "2026-02-01T00:00:00Z", body: "x" }], "cursor1") })];
  const gql = fakeGraphql({ issues, pulls: [], extraComments: [{ id: "c2", author: { login: "b" }, createdAt: "2026-02-02T00:00:00Z", body: "y" }] });
  const store = await newStore();
  await sync({ graphql: gql, store, repo: "o/r" });
  assert.deepEqual(store.data.issues[1].comments.map((c) => c.id), ["c1", "c2"]);
  assert.ok(gql.calls.includes("node"));
});

test("links tie pull requests to issues by closing references, events and mentions", () => {
  const issues = { 1: { number: 1, events: [] }, 2: { number: 2, events: [{ type: "CrossReferenced", source: 20, sourceKind: "PullRequest" }] }, 3: { number: 3, events: [] } };
  const pulls = {
    10: { number: 10, closes: [1], events: [], body: "" },
    20: { number: 20, closes: [], events: [], body: "" },
    30: { number: 30, closes: [], events: [], body: "see #3 and #999" },
  };
  const links = deriveLinks(issues, pulls).sort((a, b) => a.pull - b.pull);
  assert.deepEqual(links, [{ issue: 1, pull: 10, via: "closes" }, { issue: 2, pull: 20, via: "event" }, { issue: 3, pull: 30, via: "mention" }]);
});

test("the payload is light: no bodies, comments or commits", async () => {
  const store = await newStore();
  store.upsert("issues", [{ ...issueNode(1, "x"), kind: "issue", comments: [{ body: "big" }], events: [], labels: [] }]);
  const p = buildPayload(store.data);
  assert.equal(p.issues[0].body, undefined);
  assert.equal(p.issues[0].comments, undefined);
});

test("the tree reads native sub-issues and the checklists of an epic's body", () => {
  const issues = {
    1: { number: 1, body: "## Trabajo\n- [ ] #2 first\n- [x] #3 second\n* [ ] see #4 and #1\n- [ ] #99 gone\nplain #5", subIssues: [], parent: null },
    2: { number: 2, body: "", subIssues: [], parent: null },
    3: { number: 3, body: "", subIssues: [], parent: null },
    4: { number: 4, body: "", subIssues: [], parent: null },
    5: { number: 5, body: "", subIssues: [6], parent: 7 },
    6: { number: 6, body: "", subIssues: [], parent: null },
    7: { number: 7, body: "", subIssues: [], parent: null },
  };
  const tree = deriveTree(issues).sort((a, b) => a.parent - b.parent || a.child - b.child);
  assert.deepEqual(tree, [
    { parent: 1, child: 2, via: "checklist", checked: false },
    { parent: 1, child: 3, via: "checklist", checked: true },
    { parent: 1, child: 4, via: "checklist", checked: false },
    { parent: 5, child: 6, via: "native", checked: null },
    { parent: 7, child: 5, via: "native", checked: null },
  ]);
});

test("dependencies read native blocked-by and the phrases of a body, blocker -> blocked", () => {
  const issue = (number, body = "", extra = {}) => ({ number, body, blockedBy: [], blocking: [], ...extra });
  const issues = {
    1: issue(1, "Depende de F2a y del outbox (#2). Más abajo #3 no cuenta."),
    2: issue(2, "bloquea #4 y #5\nPendiente decidir memoria. Refs #1"),
    3: issue(3, "", { blockedBy: [4] }),
    4: issue(4, "Blocked by #5; blocks #1"),
    5: issue(5),
  };
  const deps = deriveDeps(issues).map((d) => `${d.blocker}>${d.blocked}:${d.via}`).sort();
  assert.deepEqual(deps, ["2>1:text", "2>4:text", "2>5:text", "4>1:text", "4>3:native", "5>4:text"]);
});
