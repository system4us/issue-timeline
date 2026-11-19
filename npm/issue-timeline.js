#!/usr/bin/env node
// The npm package's command: it runs the issue-timeline binary built for this machine. npm installs only the package of
// this platform (they are optionalDependencies, each limited by its os and cpu), so the binary is found by name.
"use strict";

const { spawn } = require("node:child_process");
const path = require("node:path");

const PLATFORMS = {
  "linux-x64": "issue-timeline-linux-x64",
  "linux-arm64": "issue-timeline-linux-arm64",
  "darwin-x64": "issue-timeline-darwin-x64",
  "darwin-arm64": "issue-timeline-darwin-arm64",
  "win32-x64": "issue-timeline-win32-x64",
};

const key = `${process.platform}-${process.arch}`;
const pkg = PLATFORMS[key];
if (!pkg) {
  console.error(`issue-timeline has no build for ${key}. Build it from source: https://gitlab.com/system4us/issue-timeline`);
  process.exit(1);
}

let bin;
try {
  const exe = process.platform === "win32" ? "issue-timeline.exe" : "issue-timeline";
  bin = path.join(path.dirname(require.resolve(`${pkg}/package.json`)), "bin", exe);
} catch {
  console.error(
    `The ${pkg} package is missing: npm skipped the optional dependency of this platform.\n` +
      `Install again without --no-optional / --omit=optional, for example: npm install -g issue-timeline`,
  );
  process.exit(1);
}

const child = spawn(bin, process.argv.slice(2), { stdio: "inherit", windowsHide: false });
// Ctrl-C and a stop reach the server, which shuts down cleanly; its exit code is this command's.
for (const sig of ["SIGINT", "SIGTERM", "SIGHUP"]) {
  process.on(sig, () => { if (!child.killed) child.kill(sig); });
}
child.on("error", (err) => {
  console.error(`Could not run ${bin}: ${err.message}`);
  process.exit(1);
});
child.on("exit", (code, signal) => {
  if (signal) {
    // end the same way the server did, so a shell sees the signal (our own handlers would only forward it again)
    process.removeAllListeners(signal);
    process.kill(process.pid, signal);
  } else process.exit(code ?? 0);
});
