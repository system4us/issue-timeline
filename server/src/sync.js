// Pulls every issue and pull request of a repo (history included) into the store, and keeps it current.
// A first run (or `full`) walks everything; later runs walk newest-updated first and stop at the first page
// older than the last sync, so the 5-minute poll is one or two small queries.
import { normalizeIssue, normalizePull, ISSUE_CONNECTIONS, PULL_CONNECTIONS, ISSUE_FIELDS, PULL_FIELDS, connectionSelection } from "./model.js";

const MARGIN_MS = 2 * 60 * 1000; // re-read a little before the last sync: clocks and eventual consistency

const listQuery = (kind, fields, conns, pageSize) => `
query($owner:String!,$name:String!,$after:String){
  repository(owner:$owner,name:$name){
    ${kind}(first:${pageSize},after:$after,orderBy:{field:UPDATED_AT,direction:DESC}){
      totalCount
      pageInfo{hasNextPage endCursor}
      nodes{ id number updatedAt ${fields} ${Object.entries(conns).map(([n, c]) => connectionSelection(n, c)).join("\n")} }
    }
  }
}`;

const ISSUES_QUERY = listQuery("issues", ISSUE_FIELDS, ISSUE_CONNECTIONS, 30);
const PULLS_QUERY = listQuery("pullRequests", PULL_FIELDS, PULL_CONNECTIONS, 20);

/** Reads the rest of a connection a node had more of than one page of. */
async function drain(graphql, typename, id, name, conn, firstPage) {
  const nodes = [...firstPage.nodes];
  let { hasNextPage, endCursor } = firstPage.pageInfo;
  while (hasNextPage) {
    const q = `query($id:ID!,$after:String){ node(id:$id){ ... on ${typename}{
      ${name}(first:100,after:$after${conn.args ? "," + conn.args : ""}){ pageInfo{hasNextPage endCursor} nodes{ ${conn.sel} } } } } }`;
    const data = await graphql(q, { id, after: endCursor });
    const page = data.node[name];
    nodes.push(...page.nodes);
    ({ hasNextPage, endCursor } = page.pageInfo);
  }
  return nodes;
}

async function completeNode(graphql, typename, conns, node) {
  for (const [name, conn] of Object.entries(conns)) {
    const page = node[name];
    if (page?.pageInfo?.hasNextPage) node[name] = { ...page, nodes: await drain(graphql, typename, node.id, name, conn, page) };
  }
  return node;
}

async function walk({ graphql, owner, name, query, rootField, typename, conns, since, normalize, onProgress = () => {} }) {
  const out = [];
  let processed = 0;
  let after = null;
  for (;;) {
    const data = await graphql(query, { owner, name, after });
    const page = data.repository[rootField];
    let reachedOld = false;
    for (const node of page.nodes) {
      if (since && Date.parse(node.updatedAt) < since - MARGIN_MS) {
        reachedOld = true; // newest first: everything after this one is older too
        break;
      }
      out.push(normalize(await completeNode(graphql, typename, conns, node)));
      processed++;
      onProgress({ phase: rootField === "issues" ? "Issues" : "Pull requests", done: processed, total: since ? null : page.totalCount });
    }
    if (reachedOld || !page.pageInfo.hasNextPage) return { items: out, total: page.totalCount };
    after = page.pageInfo.endCursor;
  }
}

/**
 * One sync pass. Returns what it did. `full` ignores the last sync and rereads everything.
 * @param {{graphql: Function, store: import("./store.js").Store, repo: string, full?: boolean, now?: () => number, log?: Function}} o
 */
export async function sync({ graphql, store, repo, full = false, now = Date.now, log = () => {}, onProgress = () => {} }) {
  const [owner, name] = repo.split("/");
  const t0 = now();
  const startedAt = new Date(t0).toISOString();
  const doFull = full || !store.data.meta.fullSyncAt || store.data.meta.repo !== repo;
  if (store.data.meta.repo && store.data.meta.repo !== repo) {
    store.data.issues = {};
    store.data.pulls = {};
  }
  const since = doFull ? null : Date.parse(store.data.meta.lastSync);

  const common = { graphql, owner, name, since };
  const issues = await walk({ ...common, query: ISSUES_QUERY, rootField: "issues", typename: "Issue", conns: ISSUE_CONNECTIONS, normalize: normalizeIssue, onProgress });
  const pulls = await walk({ ...common, query: PULLS_QUERY, rootField: "pullRequests", typename: "PullRequest", conns: PULL_CONNECTIONS, normalize: normalizePull, onProgress });
  store.upsert("issues", issues.items);
  store.upsert("pulls", pulls.items);

  const meta = store.data.meta;
  meta.repo = repo;
  meta.lastSync = startedAt;
  if (doFull) meta.fullSyncAt = startedAt;
  meta.totals = { issues: issues.total, pulls: pulls.total };
  const entry = { at: startedAt, full: doFull, issues: issues.items.length, pulls: pulls.items.length, ms: now() - t0 };
  meta.syncs = [...meta.syncs, entry].slice(-50);
  await store.save();
  log(`sync ${doFull ? "full" : "incremental"}: ${entry.issues} issues, ${entry.pulls} pulls in ${entry.ms} ms`);
  return entry;
}
