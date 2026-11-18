export interface Label { name: string; color: string }
export interface Milestone { title: string; dueOn: string | null }

interface Base {
  id: string
  number: number
  title: string
  url: string
  author: string | null
  createdAt: string
  updatedAt: string
  closedAt: string | null
  labels: Label[]
  assignees: string[]
  milestone: Milestone | null
  commentsCount: number
  labelEvents: number
}

export interface Issue extends Base {
  kind: "issue"
  state: "OPEN" | "CLOSED"
  stateReason: string | null
  // GitLab: the epic (or other work item of the group) this issue belongs to, when it is not an issue of this project
  parentEpic?: { iid: number; title: string; url: string; type: string | null } | null
}

export interface Pull extends Base {
  kind: "pull"
  commits?: { oid: string; message: string; at: string; author: string | null }[] // its own commits (the store keeps them)
  state: "OPEN" | "CLOSED" | "MERGED"
  isDraft: boolean
  mergedAt: string | null
  mergedBy: string | null
  reviewDecision: string | null
  base: string
  head: string
  additions: number
  deletions: number
  changedFiles: number
  closes: number[]
  commitsCount: number
  firstReviewAt: string | null
  reviewsCount: number
}

export type Item = Issue | Pull
export interface Link { issue: number; pull: number; via: "closes" | "event" | "mention" }

export interface TreeEdge { parent: number; child: number; via: "native" | "checklist"; checked: boolean | null }

export interface DepEdge { blocker: number; blocked: number; via: "native" | "text" }

export interface Payload {
  vocabulary?: { blockedBy: string[]; blocks: string[]; fieldKeys: string[] }
  ci?: import("./CiBadge").CiSummary
  meta: { repo: string | null; lastSync: string | null; fullSyncAt: string | null; syncs: { at: string; full: boolean; issues: number; pulls: number; ms: number }[]; totals?: { issues: number; pulls: number } }
  issues: Issue[]
  pulls: Pull[]
  links: Link[]
  tree: TreeEdge[]
  deps: DepEdge[]
  status: {
    syncing: boolean
    lastError: string | null
    nextSyncAt: string | null
    progress?: { phase: string; done: number; total: number | null } | null
  }
}

export interface Detail extends Base {
  kind: "issue" | "pull"
  state: string
  body: string
  comments: { id: string; author: string | null; createdAt: string; body: string }[]
  events: { type: string; at: string; actor: string | null; [k: string]: unknown }[]
  commits?: { oid: string; message: string; at: string; author: string | null }[]
  reviews?: { author: string | null; state: string; at: string | null; body: string }[]
}
