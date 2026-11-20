// Downloads the prebuilt issue-timeline binary for this platform from the GitHub
// release matching this package's version, verifying its SHA-256.
"use strict";

const fs = require("node:fs");
const path = require("node:path");
const crypto = require("node:crypto");

const { version } = require("./package.json");
const REPO = "system4us/issue-timeline";
const TARGETS = {
  "linux-x64": "linux-amd64",
  "linux-arm64": "linux-arm64",
  "darwin-x64": "darwin-amd64",
  "darwin-arm64": "darwin-arm64",
  "win32-x64": "windows-amd64",
};

function assetName() {
  const key = `${process.platform}-${process.arch}`;
  const t = TARGETS[key];
  if (!t) {
    throw new Error(`no prebuilt binary for ${key}; build from source: https://github.com/${REPO}`);
  }
  return `issue-timeline-${t}${process.platform === "win32" ? ".exe" : ""}`;
}

const binPath = () => path.join(__dirname, "vendor", assetName());

async function get(url) {
  const res = await fetch(url, { redirect: "follow" });
  if (!res.ok) throw new Error(`GET ${url} -> ${res.status}`);
  return Buffer.from(await res.arrayBuffer());
}

async function install() {
  const dest = binPath();
  if (fs.existsSync(dest)) return dest;
  const base = `https://github.com/${REPO}/releases/download/v${version}`;
  const name = assetName();
  const [bin, sums] = await Promise.all([get(`${base}/${name}`), get(`${base}/checksums.txt`)]);
  const line = sums
    .toString("utf8")
    .split("\n")
    .find((l) => l.trim().endsWith(` ${name}`) || l.trim().endsWith(`*${name}`));
  if (!line) throw new Error(`${name} not listed in checksums.txt`);
  const want = line.trim().split(/\s+/)[0];
  const got = crypto.createHash("sha256").update(bin).digest("hex");
  if (got !== want) throw new Error(`checksum mismatch for ${name}`);
  fs.mkdirSync(path.dirname(dest), { recursive: true });
  const tmp = `${dest}.tmp${process.pid}`;
  fs.writeFileSync(tmp, bin, { mode: 0o755 });
  fs.renameSync(tmp, dest);
  return dest;
}

module.exports = { install, binPath };

if (require.main === module) {
  // postinstall: never fail npm install; the launcher retries on first run.
  install().catch((e) =>
    console.warn(`issue-timeline: postinstall download skipped (${e.message}); will retry on first run`),
  );
}
