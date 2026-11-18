import { test } from "node:test";
import assert from "node:assert/strict";
import { categorize, stateOf, summarize, windowFloor, readFrom, gitlabRunOf } from "./ci.js";

test("GitLab: a merge request pipeline is the MR's CI; a branch name never decides the category", () => {
  const mr = gitlabRunOf({ id: 1, web_url: "u", ref: "feature/x", sha: "h7", status: "success", source: "merge_request_event", created_at: "2026-01-01T10:00:00Z" });
  assert.equal(mr.category, "ci");
  const push = gitlabRunOf({ id: 2, web_url: "u", ref: "main", sha: "m1", status: "success", source: "push", created_at: "2026-01-01T10:00:00Z" });
  assert.equal(push.category, "ci");
  const branchNamedPages = gitlabRunOf({ id: 3, web_url: "u", ref: "pages-redesign", sha: "h8", status: "failed", source: "merge_request_event", created_at: "2026-01-01T10:00:00Z" });
  assert.equal(branchNamedPages.category, "ci", "the branch name must not turn an MR's CI into Pages");
  assert.equal(branchNamedPages.branch, "pages-redesign");
  const summary = summarize({ runs: [mr], pulls: { 7: { number: 7, commits: [{ oid: "h7" }] } } });
  assert.equal(summary.byPr[7].state, "passed");
});

test("categories: CI events, and Dependabot / Pages / releases apart", () => {
  assert.equal(categorize({ name: "CI", path: ".github/workflows/ci.yml", event: "pull_request" }), "ci");
  assert.equal(categorize({ name: "CI", path: ".github/workflows/ci.yml", event: "push" }), "ci");
  assert.equal(categorize({ name: "npm_and_yarn in /site", path: "dynamic/dependabot/dependabot-updates", event: "dynamic", actor: "dependabot[bot]" }), "dependabot");
  assert.equal(categorize({ name: "pages build and deployment", path: "dynamic/pages/pages-build-deployment", event: "dynamic" }), "pages");
  assert.equal(categorize({ name: "Release", path: ".github/workflows/release.yml", event: "push" }), "other");
  assert.equal(categorize({ name: "Tag", path: ".github/workflows/tag.yml", event: "push" }), "other");
});

test("states: a run with no conclusion is running; skipped stays skipped", () => {
  assert.equal(stateOf({ conclusion: null }), "running");
  assert.equal(stateOf({ conclusion: "success" }), "passed");
  assert.equal(stateOf({ conclusion: "failure" }), "failed");
  assert.equal(stateOf({ conclusion: "skipped" }), "skipped");
  assert.equal(stateOf({ conclusion: "cancelled" }), "cancelled");
});

const pulls = {
  7: { number: 7, commits: [{ oid: "h7" }] },
  8: { number: 8, commits: [{ oid: "h8" }] },
  9: { number: 9, commits: [] },
};

test("a PR gets the state of its last CI run, with its link; other workflows are not its CI", () => {
  const runs = [
    { id: "1", sha: "h7", category: "ci", conclusion: "failure", url: "u1", at: "2026-01-01T10:00:00Z" },
    { id: "2", sha: "h7", category: "ci", conclusion: "success", url: "u2", at: "2026-01-01T11:00:00Z" },
    { id: "3", sha: "h8", category: "ci", conclusion: "skipped", url: "u3", at: "2026-01-02T10:00:00Z" },
    { id: "4", sha: "h7", category: "dependabot", conclusion: "success", url: "u4", at: "2026-01-03T10:00:00Z" },
    { id: "5", sha: "main1", category: "pages", conclusion: "success", url: "u5", name: "pages", at: "2026-01-03T11:00:00Z" },
  ];
  const s = summarize({ runs, pulls });
  assert.deepEqual(s.byPr[7], { state: "passed", url: "u2", at: "2026-01-01T11:00:00Z", count: 2 });
  assert.deepEqual(s.byPr[8], { state: "skipped", url: "u3", at: "2026-01-02T10:00:00Z", count: 1 });
  assert.equal(s.byPr[9], undefined, "a PR without runs has no CI entry");
  assert.equal(s.other.dependabot.count, 1);
  assert.equal(s.other.pages.last.url, "u5");
});

test("the window is always CI_DAYS back; a sync reads from a day before the last one, never before the window", () => {
  const now = Date.parse("2026-03-01T00:00:00Z");
  assert.equal(windowFloor(now), new Date(now - 90 * 86_400_000).toISOString());
  assert.equal(readFrom(now, { lastSync: null }), windowFloor(now));
  assert.equal(readFrom(now, { lastSync: "2026-02-28T12:00:00Z" }), "2026-02-27T12:00:00.000Z");
  assert.equal(readFrom(now, { lastSync: "2025-01-01T00:00:00Z" }), windowFloor(now), "an old last sync cannot reach before the window");
});
