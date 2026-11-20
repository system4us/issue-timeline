#!/usr/bin/env node
// Builds the release binaries (dist/bin/issue-timeline-<goos>-<goarch>[.exe] + checksums.txt)
// and packs the single `issue-timeline` npm launcher package (dist/npm/issue-timeline-<version>.tgz).
// The web app is embedded in the Go binary, so run `make ui` first (`make npm` does).
//
//   node scripts/npm.mjs <version> [--publish] [--tag beta] [--dry-run]
import { execFileSync } from "node:child_process";
import { createHash } from "node:crypto";
import { cpSync, existsSync, mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
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
  { goos: "linux", goarch: "amd64" },
  { goos: "linux", goarch: "arm64" },
  { goos: "darwin", goarch: "amd64" },
  { goos: "darwin", goarch: "arm64" },
  { goos: "windows", goarch: "amd64" },
];

const binOut = "dist/bin";
const npmOut = "dist/npm";
rmSync(binOut, { recursive: true, force: true });
rmSync(npmOut, { recursive: true, force: true });
mkdirSync(binOut, { recursive: true });
mkdirSync(npmOut, { recursive: true });

const run = (cmd, argv, opts = {}) => execFileSync(cmd, argv, { stdio: "inherit", ...opts });

const checksums = [];
for (const t of TARGETS) {
  const ext = t.goos === "windows" ? ".exe" : "";
  const asset = `issue-timeline-${t.goos}-${t.goarch}${ext}`;
  const outPath = join(binOut, asset);
  console.log(`building ${asset}`);
  run(
    "go",
    ["build", "-trimpath", "-ldflags", `-s -w -X main.version=${version}`, "-o", outPath, "./cmd/issue-timeline"],
    { env: { ...process.env, CGO_ENABLED: "0", GOOS: t.goos, GOARCH: t.goarch } },
  );
  const sha = createHash("sha256").update(readFileSync(outPath)).digest("hex");
  checksums.push(`${sha}  ${asset}`);
}
writeFileSync(join(binOut, "checksums.txt"), checksums.join("\n") + "\n");

const mainDir = join(npmOut, "issue-timeline");
mkdirSync(join(mainDir, "bin"), { recursive: true });
cpSync("npm/issue-timeline.js", join(mainDir, "bin", "issue-timeline.js"));
cpSync("npm/install.js", join(mainDir, "install.js"));
cpSync("README.md", join(mainDir, "README.md"));
cpSync("LICENSE", join(mainDir, "LICENSE"));
writeFileSync(
  join(mainDir, "package.json"),
  JSON.stringify(
    {
      name: "issue-timeline",
      version,
      description:
        "Timeline of GitHub, GitLab, Redmine and Jira issues and pull/merge requests, with analysis, hierarchy, dependencies, live updates and opt-in edits.",
      license: "MIT",
      homepage: "https://gitlab.com/system4us/issue-timeline#readme",
      repository: { type: "git", url: "git+https://gitlab.com/system4us/issue-timeline.git" },
      bugs: { url: "https://gitlab.com/system4us/issue-timeline/-/issues" },
      keywords: [
        "issues",
        "pull-requests",
        "timeline",
        "github",
        "gitlab",
        "redmine",
        "jira",
        "project-management",
        "dependencies",
        "analytics",
      ],
      bin: { "issue-timeline": "bin/issue-timeline.js" },
      scripts: { postinstall: "node install.js" },
      files: ["bin", "install.js", "README.md", "LICENSE"],
      engines: { node: ">=18" },
    },
    null,
    2,
  ) + "\n",
);

if (!publish) {
  run("npm", ["pack", mainDir, "--pack-destination", npmOut]);
  console.log(`\npacked ${npmOut}/issue-timeline-${version}.tgz and ${checksums.length} binaries in ${binOut}/`);
} else {
  run("npm", ["publish", mainDir, "--access", "public", "--tag", tag, ...(dryRun ? ["--dry-run"] : [])]);
  console.log(`\npublished issue-timeline@${version} under the ${tag} tag${dryRun ? " (dry run: nothing was sent)" : ""}`);
}
