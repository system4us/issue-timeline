import { test } from "node:test";
import assert from "node:assert/strict";
import { mkdtempSync, mkdirSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { defaultStoreDir, parseArgs, shouldOpen } from "./cli.js";

test("data folder: STORE_DIR, then a previous ./data, then a user folder", () => {
  const cwd = mkdtempSync(join(tmpdir(), "it-"));
  const home = "/home/u";
  assert.equal(defaultStoreDir({ env: { STORE_DIR: "/x" }, cwd, home }), "/x");
  assert.equal(defaultStoreDir({ env: {}, cwd, platform: "linux", home }), "/home/u/.local/share/issue-timeline");
  assert.equal(defaultStoreDir({ env: { XDG_DATA_HOME: "/xdg" }, cwd, platform: "linux", home }), "/xdg/issue-timeline");
  assert.equal(defaultStoreDir({ env: {}, cwd, platform: "darwin", home }), "/home/u/Library/Application Support/issue-timeline");
  mkdirSync(join(cwd, "data")); writeFileSync(join(cwd, "data", "repos.json"), "[]");
  assert.equal(defaultStoreDir({ env: {}, cwd, home }), join(cwd, "data"));
});

test("arguments and when the browser opens", () => {
  assert.deepEqual(parseArgs(["-b", "--no-open"]), { help: false, background: true, noOpen: true, stop: false });
  assert.equal(parseArgs(["stop"]).stop, true);
  assert.equal(shouldOpen({ noOpen: false, env: {}, isTTY: true }), true);
  assert.equal(shouldOpen({ noOpen: true, env: {}, isTTY: true }), false);
  assert.equal(shouldOpen({ noOpen: false, env: { CI: "1" }, isTTY: true }), false);
  assert.equal(shouldOpen({ noOpen: false, env: {}, isTTY: false }), false);
});

import { authSummary } from "./cli.js";

test("startup auth summary: env tokens, CLI sessions or a hint", () => {
  const ok = () => ({ status: 0 }), no = () => ({ status: 1 });
  assert.deepEqual(authSummary({ env: {}, run: ok }), ["GitHub: gh session", "GitLab: glab session"]);
  assert.deepEqual(authSummary({ env: { GITHUB_TOKEN: "x", GITLAB_TOKEN: "y" }, run: no }), ["GitHub: GITHUB_TOKEN", "GitLab: GITLAB_TOKEN"]);
  assert.match(authSummary({ env: {}, run: no })[1], /glab auth login/);
});
