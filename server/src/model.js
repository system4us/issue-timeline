// What is asked of GitHub (GraphQL selections) and how an answer becomes the store's records.
const login = "author{login}";

const COMMENT = `id ${login} createdAt body`;
const ISSUE_EVENTS = [
  "LABELED_EVENT", "UNLABELED_EVENT", "CLOSED_EVENT", "REOPENED_EVENT", "ASSIGNED_EVENT",
  "MILESTONED_EVENT", "RENAMED_TITLE_EVENT", "CROSS_REFERENCED_EVENT",
];
const PULL_EVENTS = [
  "LABELED_EVENT", "UNLABELED_EVENT", "CLOSED_EVENT", "REOPENED_EVENT", "MERGED_EVENT",
  "READY_FOR_REVIEW_EVENT", "CONVERT_TO_DRAFT_EVENT", "REVIEW_REQUESTED_EVENT", "CROSS_REFERENCED_EVENT",
];
// An issue's timeline union and a pull request's differ: a fragment on a type the union lacks is an error.
const ISSUE_EVENT_SEL = `__typename
  ... on LabeledEvent{createdAt actor{login} label{name}}
  ... on UnlabeledEvent{createdAt actor{login} label{name}}
  ... on ClosedEvent{createdAt actor{login} stateReason}
  ... on ReopenedEvent{createdAt actor{login}}
  ... on AssignedEvent{createdAt actor{login} assignee{... on User{login}}}
  ... on MilestonedEvent{createdAt milestoneTitle}
  ... on RenamedTitleEvent{createdAt currentTitle previousTitle}
  ... on CrossReferencedEvent{createdAt isCrossRepository source{__typename ... on Issue{number} ... on PullRequest{number}}}`;
const PULL_EVENT_SEL = `${ISSUE_EVENT_SEL}
  ... on MergedEvent{createdAt actor{login}}
  ... on ReadyForReviewEvent{createdAt actor{login}}
  ... on ConvertToDraftEvent{createdAt actor{login}}
  ... on ReviewRequestedEvent{createdAt requestedReviewer{... on User{login}}}`;

const events = (list, sel) => ({ args: `itemTypes:[${list.join(",")}]`, sel });

export const ISSUE_CONNECTIONS = {
  comments: { args: "", sel: COMMENT },
  timelineItems: events(ISSUE_EVENTS, ISSUE_EVENT_SEL),
};
export const PULL_CONNECTIONS = {
  comments: { args: "", sel: COMMENT },
  timelineItems: events(PULL_EVENTS, PULL_EVENT_SEL),
  reviews: { args: "", sel: `${login} state submittedAt body` },
  commits: { args: "", sel: "commit{oid messageHeadline committedDate authoredDate author{user{login}}}" },
};

export const connectionSelection = (name, c) =>
  `${name}(first:100${c.args ? "," + c.args : ""}){ totalCount pageInfo{hasNextPage endCursor} nodes{ ${c.sel} } }`;

const COMMON = `title body url state createdAt closedAt ${login}
  labels(first:50){nodes{name color}}
  assignees(first:20){nodes{login}}
  milestone{title dueOn}`;
export const ISSUE_FIELDS = `${COMMON} stateReason parent{number} subIssues(first:100){nodes{number}}
  blockedBy(first:50){nodes{number}} blocking(first:50){nodes{number}}`;
export const PULL_FIELDS = `${COMMON} isDraft merged mergedAt additions deletions changedFiles baseRefName headRefName
  mergedBy{login} reviewDecision closingIssuesReferences(first:50){nodes{number}}`;

const who = (u) => u?.login ?? null;
// A connection GitHub could not return (NOT_FOUND on a node the token cannot see) is null: it reads as empty.
const nodesOf = (c) => c?.nodes ?? [];

function normalizeEvent(e) {
  const base = { type: e.__typename.replace(/Event$/, ""), at: e.createdAt, actor: who(e.actor) };
  switch (e.__typename) {
    case "LabeledEvent":
    case "UnlabeledEvent": return { ...base, label: e.label?.name };
    case "ClosedEvent": return { ...base, reason: e.stateReason ?? null };
    case "AssignedEvent": return { ...base, assignee: who(e.assignee) };
    case "MilestonedEvent": return { ...base, milestone: e.milestoneTitle };
    case "RenamedTitleEvent": return { ...base, from: e.previousTitle, to: e.currentTitle };
    case "ReviewRequestedEvent": return { ...base, reviewer: who(e.requestedReviewer) };
    case "CrossReferencedEvent": return { ...base, source: e.source?.number ?? null, sourceKind: e.source?.__typename ?? null, external: !!e.isCrossRepository };
    default: return base;
  }
}

const common = (n) => ({
  id: n.id,
  number: n.number,
  title: n.title,
  body: n.body ?? "",
  url: n.url,
  author: who(n.author),
  createdAt: n.createdAt,
  updatedAt: n.updatedAt,
  closedAt: n.closedAt ?? null,
  labels: nodesOf(n.labels).map((l) => ({ name: l.name, color: l.color })),
  assignees: nodesOf(n.assignees).map((a) => a.login),
  milestone: n.milestone ? { title: n.milestone.title, dueOn: n.milestone.dueOn ?? null } : null,
  commentsCount: n.comments?.totalCount ?? 0,
  comments: nodesOf(n.comments).map((c) => ({ id: c.id, author: who(c.author), createdAt: c.createdAt, body: c.body })),
  events: nodesOf(n.timelineItems).filter(Boolean).map(normalizeEvent),
});

export function normalizeIssue(n) {
  return {
    kind: "issue",
    ...common(n),
    state: n.state,
    stateReason: n.stateReason ?? null,
    // GitHub's own sub-issue relation (the checklists of a body are read apart, see derive.js)
    parent: n.parent?.number ?? null,
    subIssues: (n.subIssues?.nodes ?? []).map((s) => s.number),
    // GitHub's own dependencies (the blocker words written in a body are read apart, see derive.js)
    blockedBy: (n.blockedBy?.nodes ?? []).map((s) => s.number),
    blocking: (n.blocking?.nodes ?? []).map((s) => s.number),
  };
}

export function normalizePull(n) {
  return {
    kind: "pull",
    ...common(n),
    state: n.merged ? "MERGED" : n.state, // OPEN | CLOSED | MERGED
    isDraft: n.isDraft,
    mergedAt: n.mergedAt ?? null,
    mergedBy: who(n.mergedBy),
    reviewDecision: n.reviewDecision ?? null,
    base: n.baseRefName,
    head: n.headRefName,
    additions: n.additions,
    deletions: n.deletions,
    changedFiles: n.changedFiles,
    closes: nodesOf(n.closingIssuesReferences).map((i) => i.number),
    commitsCount: n.commits?.totalCount ?? 0,
    commits: nodesOf(n.commits).map((c) => ({
      oid: c.commit.oid, message: c.commit.messageHeadline, at: c.commit.committedDate, author: c.commit.author?.user?.login ?? null,
    })),
    reviews: nodesOf(n.reviews).map((r) => ({ author: who(r.author), state: r.state, at: r.submittedAt, body: r.body })),
  };
}
