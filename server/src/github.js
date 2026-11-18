// A tiny GitHub GraphQL client. The token is GITHUB_TOKEN or, failing that, the one of the `gh` CLI.
import { execFileSync } from "node:child_process";

/**
 * The GitHub endpoints. github.com by default; for GitHub Enterprise Server set GITHUB_HOST (for example
 * github.mycompany.com) and the REST and GraphQL URLs follow. GITHUB_API_URL / GITHUB_GRAPHQL_URL override either.
 */
export function githubEndpoints(env = process.env) {
  const host = (env.GITHUB_HOST ?? "").replace(/^https?:\/\//, "").replace(/\/+$/, "");
  const enterprise = host && host !== "github.com";
  const rest = (env.GITHUB_API_URL || (enterprise ? `https://${host}/api/v3` : "https://api.github.com")).replace(/\/+$/, "");
  const graphql = env.GITHUB_GRAPHQL_URL || (enterprise ? `https://${host}/api/graphql` : "https://api.github.com/graphql");
  return { host: host || "github.com", rest, graphql };
}

export function resolveToken(env = process.env) {
  if (env.GITHUB_TOKEN) return env.GITHUB_TOKEN;
  try {
    const { host } = githubEndpoints(env);
    return execFileSync("gh", ["auth", "token", "--hostname", host], { encoding: "utf8" }).trim();
  } catch {
    throw new Error("No token: set GITHUB_TOKEN or log in with `gh auth login`.");
  }
}

export function createClient({ token, fetchImpl = fetch, endpoint = githubEndpoints().graphql }) {
  /** @returns {Promise<any>} the `data` of the answer; throws on transport or GraphQL errors. */
  return async function graphql(query, variables = {}) {
    for (let attempt = 1; ; attempt++) {
      // A connection that drops or a body cut short is a transient error too: read the whole answer inside the retry.
      let res, body;
      try {
        res = await fetchImpl(endpoint, {
          signal: AbortSignal.timeout(60_000), // a request that never answers would stall the sync
          method: "POST",
          headers: {
            authorization: `bearer ${token}`, "content-type": "application/json", "user-agent": "issue-timeline",
            "graphql-features": "sub_issues,issue_dependencies", // native parent / sub-issue and blocked-by / blocking fields
          },
          body: JSON.stringify({ query, variables }),
        });
        // Secondary rate limits and transient errors: back off and retry a few times.
        if ((res.status === 403 || res.status >= 500) && attempt < 4) {
          await new Promise((r) => setTimeout(r, attempt * 2000));
          continue;
        }
        if (!res.ok) throw new Error(`GitHub ${res.status}: ${(await res.text()).slice(0, 200)}`);
        body = await res.json();
      } catch (e) {
        if (attempt < 4 && !/^GitHub \d{3}:/.test(e.message)) {
          await new Promise((r) => setTimeout(r, attempt * 2000));
          continue;
        }
        throw e;
      }
      if (body.errors?.length) {
        // A NOT_FOUND on one node (a parent or blocker in a repo the token cannot see) comes with the rest of the data: keep
        // it, that node reads as empty. Anything else is a real failure.
        const onlyMissing = body.data && body.errors.every((e) => e.type === "NOT_FOUND");
        if (!onlyMissing) throw new Error(`GitHub GraphQL: ${body.errors.map((e) => e.message).join("; ")}`);
      }
      return body.data;
    }
  };
}
