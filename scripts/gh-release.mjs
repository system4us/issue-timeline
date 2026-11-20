#!/usr/bin/env node
// Creates (or updates) a GitHub Release for <tag> and uploads all files in <dir>.
// No dependencies (Node >= 18).
//
//   GH_TOKEN=... node scripts/gh-release.mjs <tag> <assets-dir> [owner/repo]
import { readdirSync, readFileSync, statSync } from "node:fs";
import { join } from "node:path";

const [tag, assetsDir, repo = "system4us/issue-timeline"] = process.argv.slice(2);
const token = process.env.GH_TOKEN;
if (!tag || !assetsDir || !token) {
  console.error("usage: GH_TOKEN=... node scripts/gh-release.mjs <tag> <assets-dir> [owner/repo]");
  process.exit(2);
}

const headers = {
  Authorization: `Bearer ${token}`,
  Accept: "application/vnd.github+json",
  "X-GitHub-Api-Version": "2022-11-28",
};

async function api(url, init = {}) {
  const res = await fetch(url, { ...init, headers: { ...headers, ...(init.headers || {}) } });
  const text = await res.text();
  return { status: res.status, ok: res.ok, body: text ? JSON.parse(text) : null, text };
}

// Find existing release for tag, or create one.
let rel = await api(`https://api.github.com/repos/${repo}/releases/tags/${encodeURIComponent(tag)}`);
if (rel.status === 404) {
  rel = await api(`https://api.github.com/repos/${repo}/releases`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      tag_name: tag,
      name: tag,
      prerelease: tag.includes("-"),
      generate_release_notes: true,
    }),
  });
}
if (!rel.ok) {
  console.error(`GitHub release error (${rel.status}): ${rel.text}`);
  process.exit(1);
}

const release = rel.body;
const uploadBase = release.upload_url.replace(/\{[^}]*\}$/, "");
const existing = new Map((release.assets || []).map((a) => [a.name, a.id]));

for (const name of readdirSync(assetsDir).sort()) {
  const full = join(assetsDir, name);
  if (!statSync(full).isFile()) continue;
  if (existing.has(name)) {
    const del = await api(`https://api.github.com/repos/${repo}/releases/assets/${existing.get(name)}`, {
      method: "DELETE",
    });
    if (!del.ok && del.status !== 404) {
      console.error(`failed to delete existing asset ${name}: ${del.status} ${del.text}`);
      process.exit(1);
    }
  }
  const data = readFileSync(full);
  const up = await fetch(`${uploadBase}?name=${encodeURIComponent(name)}`, {
    method: "POST",
    headers: { ...headers, "Content-Type": "application/octet-stream" },
    body: data,
  });
  if (!up.ok) {
    console.error(`failed to upload ${name}: ${up.status} ${await up.text()}`);
    process.exit(1);
  }
  console.log(`uploaded ${name} (${data.length} bytes) to ${repo}@${tag}`);
}
