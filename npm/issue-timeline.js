#!/usr/bin/env node
// The npm package's command: runs the issue-timeline binary for this platform,
// downloading it from the matching GitHub release (verifying SHA-256) on first
// use if postinstall didn't already fetch it.
"use strict";

const { spawn } = require("node:child_process");
const { install } = require("../install.js");

(async () => {
  let bin;
  try {
    bin = await install();
  } catch (e) {
    console.error(`issue-timeline: ${e.message}`);
    process.exit(1);
  }

  const child = spawn(bin, process.argv.slice(2), { stdio: "inherit", windowsHide: false });
  // Ctrl-C and a stop reach the server, which shuts down cleanly; its exit code is this command's.
  for (const sig of ["SIGINT", "SIGTERM", "SIGHUP"]) {
    process.on(sig, () => {
      if (!child.killed) child.kill(sig);
    });
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
    } else {
      process.exit(code ?? 0);
    }
  });
})();
