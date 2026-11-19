#!/usr/bin/env node
// Builds the npm packages of a release: one per platform with its binary (the web app is embedded, so run `make ui`
// first; `make npm` does), and the issue-timeline package that runs the right one. Without --publish it only packs them
// (dist/npm/*.tgz), to try: npm install -g dist/npm/issue-timeline-<platform>-<v>.tgz dist/npm/issue-timeline-<v>.tgz
//
//   node scripts/npm.mjs <version> [--publish] [--tag beta] [--dry-run]
//
// The platform packages are published first: the main one depends on them, at the same version.
import { execFileSync } from "node:child_process";
import { cpSync, existsSync, mkdirSync, readdirSync, rmSync, writeFileSync } from "node:fs";
import { join } from "node:path";

const args = process.argv.slice(2);
const version = args.find((a) => !a.startsWith("--") && args[args.indexOf(a) - 1] !== "--tag");
const publish = args.includes("--publish");
const dryRun = args.includes("--dry-run");
const tag = args.includes("--tag") ? args[args.indexOf("--tag") + 1] : version?.includes("-") ? "beta" : "latest";
if (!version || !/^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$/.test(version)) {
  console.error("usage: node scripts/npm.mjs <version> [--publish] [--tag beta] [--dry-run]");
  process.exit(2);
}
if (!existsSync("internal/webui/dist/index.html")) {
  console.error("the web app is not built into internal/webui/dist: run `make ui` first (or `make npm`)");
  process.exit(1);
}

const TARGETS = [
  { goos: "linux", goarch: "amd64", os: "linux", cpu: "x64" },
  { goos: "linux", goarch: "arm64", os: "linux", cpu: "arm64" },
  { goos: "darwin", goarch: "amd64", os: "darwin", cpu: "x64" },
  { goos: "darwin", goarch: "arm64", os: "darwin", cpu: "arm64" },
  { goos: "windows", goarch: "amd64", os: "win32", cpu: "x64" },
];
const common = {
  version,
  license: "MIT",
  homepage: "https://gitlab.com/system4us/issue-timeline#readme",
  repository: { type: "git", url: "git+https://gitlab.com/system4us/issue-timeline.git" },
  bugs: { url: "https://gitlab.com/system4us/issue-timeline/-/issues" },
};
const out = "dist/npm";
rmSync(out, { recursive: true, force: true });
mkdirSync(out, { recursive: true });
const run = (cmd, argv, opts = {}) => execFileSync(cmd, argv, { stdio: "inherit", ...opts });

const platforms = [];
for (const t of TARGETS) {
  const name = `issue-timeline-${t.os}-${t.cpu}`;
  const dir = join(out, name);
  const exe = t.goos === "windows" ? "issue-timeline.exe" : "issue-timeline";
  console.log(`building ${name}`);
  run("go", ["build", "-trimpath", "-ldflags", `-s -w -X main.version=${version}`, "-o", join(dir, "bin", exe), "./cmd/issue-timeline"], {
    env: { ...process.env, CGO_ENABLED: "0", GOOS: t.goos, GOARCH: t.goarch },
  });
  writeFileSync(join(dir, "package.json"), JSON.stringify({
    name, ...common,
    description: `The issue-timeline binary for ${t.os} ${t.cpu}. Install issue-timeline, not this package.`,
    os: [t.os], cpu: [t.cpu], files: ["bin"], preferUnplugged: true,
  }, null, 2) + "\n");
  cpSync("LICENSE", join(dir, "LICENSE"));
  platforms.push({ name, dir });
}

const main = join(out, "issue-timeline");
mkdirSync(join(main, "bin"), { recursive: true });
cpSync("npm/issue-timeline.js", join(main, "bin", "issue-timeline.js"));
cpSync("README.md", join(main, "README.md"));
cpSync("LICENSE", join(main, "LICENSE"));
writeFileSync(join(main, "package.json"), JSON.stringify({
  name: "issue-timeline", ...common,
  description: "Timeline of GitHub, GitLab, Redmine and Jira issues and pull/merge requests, with analysis, hierarchy, dependencies, live updates and opt-in edits.",
  keywords: ["issues", "pull-requests", "timeline", "github", "gitlab", "redmine", "jira", "project-management", "dependencies", "analytics"],
  bin: { "issue-timeline": "bin/issue-timeline.js" },
  files: ["bin", "README.md", "LICENSE"],
  engines: { node: ">=18" },
  optionalDependencies: Object.fromEntries(platforms.map((p) => [p.name, version])),
}, null, 2) + "\n");

const dirs = [...platforms.map((p) => p.dir), main];
if (!publish) {
  for (const dir of dirs) run("npm", ["pack", dir, "--pack-destination", out])
  console.log(`\npacked into ${out}: ${readdirSync(out).filter((f) => f.endsWith(".tgz")).join(", ")}`)
} else {
  // the platform packages first: the main one names them at this version
  for (const dir of dirs) run("npm", ["publish", dir, "--access", "public", "--tag", tag, ...(dryRun ? ["--dry-run"] : [])])
  console.log(`\npublished ${version} under the ${tag} tag${dryRun ? " (dry run: nothing was sent)" : ""}`)
}
