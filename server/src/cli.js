// Command line behaviour: where the data lives, opening the browser, running in the background.
import { spawn, spawnSync } from "node:child_process";
import { existsSync, mkdirSync, openSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";

export const HELP = `issue-timeline: a timeline of the issues and pull/merge requests of GitHub and GitLab repositories

Usage: issue-timeline [options]
       issue-timeline stop

  -b, --background   keep running after the terminal closes (logs in the data folder)
      --no-open      do not open the browser
  -h, --help         show this help

Sign-in: it uses your existing sessions, nothing to configure. GitHub: gh auth login (or GITHUB_TOKEN).
GitLab: glab auth login (or GITLAB_TOKEN). It only reads, and never stores your tokens.

Environment: PORT, HOST, STORE_DIR, POLL_MINUTES, GITHUB_TOKEN, GITHUB_HOST, VOCABULARY_FILE. See the README.`;

export function parseArgs(argv) {
  const has = (...names) => names.some((n) => argv.includes(n));
  return { help: has("-h", "--help"), background: has("-b", "--background"), noOpen: has("--no-open"), stop: argv[0] === "stop" };
}

/**
 * Where the data is kept: STORE_DIR; the ./data of the current folder when it already holds a repository list (the
 * layout before this default existed); otherwise a folder of the user, so running the command in a project does not
 * drop a data folder inside it.
 */
export function defaultStoreDir({ env = process.env, cwd = process.cwd(), platform = process.platform, home = homedir() } = {}) {
  if (env.STORE_DIR) return env.STORE_DIR;
  if (existsSync(join(cwd, "data", "repos.json"))) return join(cwd, "data");
  if (platform === "win32") return join(env.APPDATA ?? join(home, "AppData", "Roaming"), "issue-timeline");
  if (platform === "darwin") return join(home, "Library", "Application Support", "issue-timeline");
  return join(env.XDG_DATA_HOME ?? join(home, ".local", "share"), "issue-timeline");
}

/** Opens a URL in the default browser. Best effort: a headless machine just keeps the printed URL. */
export function openBrowser(url, { platform = process.platform, spawnImpl = spawn } = {}) {
  const [cmd, args] = platform === "darwin" ? ["open", [url]] : platform === "win32" ? ["cmd", ["/c", "start", "", url]] : ["xdg-open", [url]];
  try {
    const child = spawnImpl(cmd, args, { stdio: "ignore", detached: true });
    child.on?.("error", () => {});
    child.unref?.();
  } catch { /* no browser available */ }
}

/** Should the browser open by itself? Not in CI, not over a non-interactive pipe, not when asked not to. */
export function shouldOpen({ noOpen, env = process.env, isTTY = process.stdout.isTTY } = {}) {
  return !noOpen && Boolean(isTTY) && !env.CI && !env.NO_OPEN;
}

const pidFile = (dir) => join(dir, "issue-timeline.pid");

/** Starts this same command detached, with its output in <dir>/issue-timeline.log, and returns its pid. */
export function startBackground({ dir, argv, script }) {
  mkdirSync(dir, { recursive: true });
  const log = openSync(join(dir, "issue-timeline.log"), "a");
  const rest = argv.filter((a) => a !== "-b" && a !== "--background");
  const child = spawn(process.execPath, [script, ...rest, "--no-open"], {
    detached: true, stdio: ["ignore", log, log], env: { ...process.env, STORE_DIR: dir, ISSUE_TIMELINE_CHILD: "1" },
  });
  child.unref();
  writeFileSync(pidFile(dir), String(child.pid));
  return child.pid;
}

export function stopBackground(dir) {
  let pid;
  try { pid = Number(readFileSync(pidFile(dir), "utf8")); } catch { return null; }
  try { process.kill(pid, "SIGTERM"); } catch { /* already gone */ }
  rmSync(pidFile(dir), { force: true });
  return pid;
}

const loggedIn = (cmd, args, run) => { try { return run(cmd, args, { stdio: "ignore", timeout: 5000 }).status === 0; } catch { return false; } };

/** How each host is signed in, for the startup message. Never prints a token. */
export function authSummary({ env = process.env, run = spawnSync } = {}) {
  const github = env.GITHUB_TOKEN ? "GITHUB_TOKEN" : loggedIn("gh", ["auth", "status"], run) ? "gh session" : null;
  const gitlab = env.GITLAB_TOKEN ? "GITLAB_TOKEN" : loggedIn("glab", ["auth", "status"], run) ? "glab session" : null;
  return [
    `GitHub: ${github ?? "not signed in. Run `gh auth login` or set GITHUB_TOKEN"}`,
    `GitLab: ${gitlab ?? "not signed in. Run `glab auth login` or set GITLAB_TOKEN"}`,
  ];
}
