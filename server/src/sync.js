// Pulls every issue and pull request of a repo (history included) into the store, and keeps it current.
// A first run (or `full`) walks everything; later runs walk newest-updated first and stop at the first page
// older than the last sync, so the 5-minute poll is one or two small queries.
import { normalizeIssue, normalizePull, ISSUE_CONNECTIONS, PULL_CONNECTIONS, ISSUE_FIELDS, PULL_FIELDS, connectionSelection } from "./model.js";

const MARGIN_MS = 2 * 60 * 1000; // re-read a little before the last sync: clocks and eventual consistency

const listQuery = (kind, stateType, fields, conns, pageSize) => `
query($owner:String!,$name:String!,$after:String,$states:[${stateType}!]){
  repository(owner:$owner,name:$name){
    ${kind}(first:${pageSize},after:$after,states:$states,orderBy:{field:UPDATED_AT,direction:DESC}){
      totalCount
      pageInfo{hasNextPage endCursor}
      nodes{ id number updatedAt ${fields} ${Object.entries(conns).map(([n, c]) => connectionSelection(n, c)).join("\n")} }
    }
  }
}`;

const ISSUES_QUERY = listQuery("issues", "IssueState", ISSUE_FIELDS, ISSUE_CONNECTIONS, 30);
const PULLS_QUERY = listQuery("pullRequests", "PullRequestState", PULL_FIELDS, PULL_CONNECTIONS, 20);

// Each state is its own chain of cursors (newest-updated first), so the chains can be read at the same time. A page
// costs GitHub several seconds, and the pages of one chain cannot overlap, but those of different chains can.
const ISSUE_STATES = [["OPEN"], ["CLOSED"]];
const PULL_STATES = [["OPEN"], ["MERGED"], ["CLOSED"]];

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

async function walk({ graphql, owner, name, query, rootField, typename, conns, since, normalize, states, onItem = () => {} }) {
  const out = [];
  let after = null;
  for (;;) {
    const data = await graphql(query, { owner, name, after, states });
    const page = data.repository[rootField];
    let reachedOld = false;
    for (const node of page.nodes) {
      if (since && Date.parse(node.updatedAt) < since - MARGIN_MS) {
        reachedOld = true; // newest first: everything after this one is older too
        break;
      }
      out.push(normalize(await completeNode(graphql, typename, conns, node)));
      onItem();
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
  let done = 0;
  const onItem = () => onProgress({ phase: "Issues and pull requests", done: ++done, total: null });
  const merge = (parts) => ({ items: parts.flatMap((p) => p.items), total: parts.reduce((n, p) => n + p.total, 0) });
  const [issues, pulls] = await Promise.all([
    Promise.all(ISSUE_STATES.map((states) => walk({ ...common, states, onItem, query: ISSUES_QUERY, rootField: "issues", typename: "Issue", conns: ISSUE_CONNECTIONS, normalize: normalizeIssue }))).then(merge),
    Promise.all(PULL_STATES.map((states) => walk({ ...common, states, onItem, query: PULLS_QUERY, rootField: "pullRequests", typename: "PullRequest", conns: PULL_CONNECTIONS, normalize: normalizePull }))).then(merge),
  ]);
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
