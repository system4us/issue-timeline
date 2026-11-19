// The server's own gates, end to end: the real server, started against a fake Redmine. A request with X-Forwarded-For
// stands for one from another machine (through a proxy), which is what the gates tell apart.
import test, { after, before } from "node:test";
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { createServer } from "node:http";
import { mkdtemp, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

// The server under test: the Go binary (make build), or another build named by ISSUE_TIMELINE_BIN. End to end, over
// HTTP, against a fake Redmine: the API, the gates and the sessions as a browser sees them. Run with `make e2e`.
const BIN = process.env.ISSUE_TIMELINE_BIN ?? join(dirname(fileURLToPath(import.meta.url)), "..", "..", "bin", "issue-timeline");

// Who each key is in the fake Redmine. The owner's key is the server's own; the others sign in.
const USERS = {
  "k-owner": { id: 1, login: "owner", firstname: "Owner", admin: false },
  "k-viewer": { id: 2, login: "vera", firstname: "Vera", admin: false },
  "k-editor": { id: 3, login: "eddie", firstname: "Eddie", admin: false },
  "k-outsider": { id: 4, login: "otto", firstname: "Otto", admin: false },
};
const MEMBERS = [
  { user: { id: 1 }, roles: [{ name: "Manager" }] },
  { user: { id: 2 }, roles: [{ name: "Reporter" }] },
  { user: { id: 3 }, roles: [{ name: "Developer" }] },
];
const ISSUE = { id: 1, subject: "first", description: "d", status: { id: 1, name: "New" }, tracker: { id: 1, name: "Bug" }, author: { id: 1, name: "Owner" }, created_on: "2026-01-01T00:00:00Z", updated_on: "2026-01-02T00:00:00Z", journals: [], relations: [], children: [] };

let redmine, server, base, origin;
const seenKeys = new Set();
const writes = [];

function freePort() {
  return new Promise((resolve) => { const s = createServer().listen(0, "127.0.0.1", () => { const { port } = s.address(); s.close(() => resolve(port)); }); });
}

before(async () => {
  redmine = createServer((req, res) => {
    const key = req.headers["x-redmine-api-key"];
    seenKeys.add(key);
    const u = new URL(req.url, "http://x");
    const send = (code, body) => { res.writeHead(code, { "content-type": "application/json" }); res.end(JSON.stringify(body ?? {})); };
    if (!USERS[key]) return send(401);
    if (req.method !== "GET") { writes.push({ method: req.method, path: u.pathname, key }); return send(204); }
    if (u.pathname === "/users/current.json") return send(200, { user: { ...USERS[key], groups: [] } });
    if (u.pathname === "/projects/backend.json") return send(200, { project: { id: 42, name: "Backend", identifier: "backend" } });
    if (u.pathname === "/projects/backend/memberships.json") return send(200, { memberships: MEMBERS, total_count: MEMBERS.length });
    if (u.pathname === "/issue_statuses.json") return send(200, { issue_statuses: [{ id: 1, name: "New", is_closed: false }, { id: 5, name: "Closed", is_closed: true }] });
    if (u.pathname === "/issues.json") return send(200, { issues: [{ id: 1 }], total_count: 1 });
    if (u.pathname === "/issues/1.json") return send(200, { issue: ISSUE });
    return send(404);
  });
  await new Promise((r) => redmine.listen(0, "127.0.0.1", r));
  origin = `http://127.0.0.1:${redmine.address().port}`;

  const dir = await mkdtemp(join(tmpdir(), "routes-"));
  const repo = `redmine:${origin}/backend`;
  await writeFile(join(dir, "repos.json"), JSON.stringify([repo]));
  await writeFile(join(dir, "repo-modes.json"), JSON.stringify({ [repo]: "write" }));
  const port = await freePort();
  base = `http://127.0.0.1:${port}`;
  // PATH holds node only: no gh, glab or secret-tool, so nothing reads this machine's own accounts or keyring.
  server = spawn(BIN, [], {
    env: { PATH: dirname(process.execPath), STORE_DIR: dir, PORT: String(port), HOST: "127.0.0.1", NO_OPEN: "1", REDMINE_API_KEY: "k-owner", POLL_MINUTES: "600" },
    stdio: ["ignore", "pipe", "pipe"],
  });
  let log = "";
  server.stdout.on("data", (d) => { log += d; });
  server.stderr.on("data", (d) => { log += d; });
  for (let i = 0; i < 100; i++) {
    try { if ((await fetch(`${base}/api/me`)).status) break; } catch { /* not up yet */ }
    await new Promise((r) => setTimeout(r, 100));
  }
  // the first sync, with the owner's key
  for (let i = 0; i < 100; i++) {
    const d = await fetch(`${base}/api/data`).then((r) => r.json()).catch(() => ({}));
    if (d.issues?.length) break;
    await new Promise((r) => setTimeout(r, 100));
  }
  process.on("exit", () => server.kill());
  if (!server.pid) throw new Error(log);
});

after(() => { server?.kill(); redmine?.close(); });

const REMOTE = { "x-forwarded-for": "203.0.113.9" };
const W = { "x-issue-timeline": "1", "content-type": "application/json" };
const call = (path, { method = "GET", headers = {}, body } = {}) => fetch(`${base}${path}`, { method, headers, body: body === undefined ? undefined : JSON.stringify(body) });
async function signIn(key, instance = origin) {
  const r = await call("/api/session", { method: "POST", headers: { ...REMOTE, ...W }, body: { provider: "redmine", instance, key } });
  const cookie = (r.headers.get("set-cookie") ?? "").split(";")[0];
  return { status: r.status, cookie, body: await r.json() };
}

test("a Host header that does not name this server is refused (DNS rebinding)", async () => {
  // fetch sets Host from the URL, so the request is made with a raw socket
  const { request } = await import("node:http");
  const status = await new Promise((resolve) => {
    const req = request(`${base}/api/data`, { headers: { host: "evil.example:80" } }, (res) => { res.resume(); resolve(res.statusCode); });
    req.end();
  });
  assert.equal(status, 421);
});

test("from another machine, the API needs a sign-in, and only to a site this server reads", async () => {
  assert.equal((await call("/api/data", { headers: REMOTE })).status, 401);
  assert.equal((await call("/api/repos", { headers: REMOTE })).status, 401);
  const fake = await signIn("anything", "http://127.0.0.1:1");
  assert.equal(fake.status, 400, "an unknown instance is never asked (no SSRF, no fake login)");
  assert.match(fake.body.error, /sites this server reads/);
  assert.equal((await signIn("not-a-key")).status, 401);
});

test("a person who is not a member does not see the repo", async () => {
  const { cookie } = await signIn("k-outsider");
  const repos = await call("/api/repos", { headers: { ...REMOTE, cookie } }).then((r) => r.json());
  assert.deepEqual(repos.repos, []);
  assert.equal(repos.canConfigure, false);
  // a repo the person may not see reads as one that is not configured (400 in the Node server, 404 in the Go one)
  assert.ok([400, 404].includes((await call(`/api/data?repo=${encodeURIComponent(`redmine:${origin}/backend`)}`, { headers: { ...REMOTE, cookie } })).status));
});

test("a viewer reads but cannot edit, switch the mode, add or link repos", async () => {
  const { cookie } = await signIn("k-viewer");
  const h = { ...REMOTE, cookie };
  const repo = encodeURIComponent(`redmine:${origin}/backend`);
  const repos = await call("/api/repos", { headers: h }).then((r) => r.json());
  assert.deepEqual(repos.repos, [`redmine:${origin}/backend`]);
  const data = await call(`/api/data?repo=${repo}`, { headers: h }).then((r) => r.json());
  assert.equal(data.issues.length, 1);
  const edit = await call(`/api/issues/1?repo=${repo}`, { method: "PATCH", headers: { ...h, ...W }, body: { title: "x" } });
  assert.equal(edit.status, 403);
  assert.match((await edit.json()).error, /read-only/);
  assert.equal((await call(`/api/repo-mode?repo=${repo}`, { method: "PUT", headers: { ...h, ...W }, body: { mode: "read" } })).status, 403);
  assert.equal((await call("/api/repos", { method: "POST", headers: { ...h, ...W }, body: { provider: "redmine", repo: "https://evil.example/projects/x" } })).status, 403);
  assert.equal((await call("/api/links", { method: "POST", headers: { ...h, ...W }, body: { tracker: `redmine:${origin}/backend`, code: [] } })).status, 403);
  assert.ok(!writes.some((w) => w.key === "k-viewer"), "nothing reached Redmine");
});

test("an editor edits with their own key", async () => {
  const { cookie } = await signIn("k-editor");
  const repo = encodeURIComponent(`redmine:${origin}/backend`);
  const r = await call(`/api/issues/1?repo=${repo}`, { method: "PATCH", headers: { ...REMOTE, cookie, ...W }, body: { title: "renamed" } });
  assert.equal(r.status, 200);
  assert.ok(writes.some((w) => w.method === "PUT" && w.path === "/issues/1.json" && w.key === "k-editor"));
});

test("a body that is not JSON is a 400, not a 500; a write from another origin is refused", async () => {
  const r = await fetch(`${base}/api/repo-mode?repo=x`, { method: "PUT", headers: W, body: "{nope" });
  // an unknown repo with a broken body: either check may come first (400 in the Node server, 404 in the Go one)
  assert.ok([400, 404].includes(r.status), `status ${r.status}`);
  const bad = await fetch(`${base}/api/links`, { method: "POST", headers: W, body: "{nope" });
  assert.equal(bad.status, 400);
  const cross = await fetch(`${base}/api/sync`, { method: "POST", headers: { ...W, origin: "https://evil.example" } });
  assert.equal(cross.status, 403);
});

test("this machine adds repos, and the owner's key is never sent to a site it is not bound to", async () => {
  const r = await call("/api/repos", { method: "POST", headers: W, body: { provider: "redmine", repo: "http://127.0.0.1:1/projects/other" } });
  assert.equal(r.status, 400);
  assert.match((await r.json()).error, /belongs to/);
  const repos = await call("/api/repos").then((x) => x.json());
  assert.equal(repos.canConfigure, true);
});
