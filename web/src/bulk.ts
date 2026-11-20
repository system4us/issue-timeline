// Bulk edit: one change applied to many issues. Each issue is its own PATCH (the same one the detail panel sends), so the
// server, its permissions and the host see an ordinary edit; this module decides what each issue's body is and runs them a
// few at a time.
import type { Body, Target } from "./inline/changes"
import type { Item } from "./types"

export type BulkOp =
  | { kind: "assign"; id: number | string | null; name: string | null } // null: unassigned
  | { kind: "milestone"; id: number | null; name: string | null } // Redmine: the version
  | { kind: "addLabel"; name: string }
  | { kind: "removeLabel"; name: string }
  | { kind: "state"; to: "OPEN" | "CLOSED" }
  | { kind: "dueDate"; date: string | null }

export type BulkKind = BulkOp["kind"]

export const BULK_LABEL: Record<BulkKind, string> = {
  assign: "Assign to", milestone: "Set milestone", addLabel: "Add label", removeLabel: "Remove label", state: "Set state", dueDate: "Set due date",
}

/** What can be applied to many issues on a host (labels are GitHub and GitLab's; Redmine has versions instead of milestones). */
export function bulkKinds(t: Target): BulkKind[] {
  if (t.host === "redmine") return ["assign", "milestone", "state", "dueDate"]
  if (t.host === "gitlab") return ["assign", "milestone", "addLabel", "removeLabel", "state", "dueDate"]
  return ["assign", "milestone", "addLabel", "removeLabel", "state"]
}

const day = (d: string | null | undefined) => (d ? d.slice(0, 10) : null)

/**
 * The PATCH body that applies `op` to one issue, or null when the issue already is that way (it is skipped, not sent) or the
 * item cannot take it (a pull request).
 */
export function bulkBody(t: Target, op: BulkOp, item: Item): Body | null {
  if (item.kind !== "issue") return null
  switch (op.kind) {
    case "assign": {
      const now = item.assignees
      if (op.id === null ? now.length === 0 : now.length === 1 && now[0] === op.name) return null
      if (t.host === "redmine") return { assignee: op.id }
      return { assignees: op.id === null ? [] : [op.id] }
    }
    case "milestone": {
      const now = item.milestone?.title ?? null
      if (now === op.name) return null
      return t.host === "redmine" ? { version: op.id } : { milestone: op.id }
    }
    case "addLabel": {
      const names = item.labels.map((l) => l.name)
      return names.includes(op.name) ? null : { labels: [...names, op.name] }
    }
    case "removeLabel": {
      const names = item.labels.map((l) => l.name)
      return names.includes(op.name) ? { labels: names.filter((n) => n !== op.name) } : null
    }
    case "state":
      return item.state === op.to ? null : { state: op.to }
    case "dueDate":
      return day(item.dueDate) === op.date ? null : { dueDate: op.date }
  }
}

export interface BulkResult {
  /** Keys changed. */
  changed: string[]
  /** Keys that already were as asked (or cannot take it), and were not sent. */
  skipped: string[]
  /** Keys that failed, with the host's reason. */
  failed: { key: string; error: string }[]
  /** Keys never tried because the run stopped after repeated failures. */
  notTried: string[]
}

/** After this many failures in a row the run stops: a refused credential would fail every issue the same way. */
export const STOP_AFTER = 3

/**
 * Applies `op` to `items`, `concurrency` at a time. `send(key, body)` is the PATCH of one issue. `onProgress` is told after
 * each issue settles. The run stops starting new ones after STOP_AFTER failures in a row; the rest are reported as not tried.
 */
export async function runBulk(
  items: Item[], t: Target, op: BulkOp,
  send: (key: string, body: Body) => Promise<unknown>,
  opts: { concurrency?: number; onProgress?: (settled: number, total: number) => void } = {},
): Promise<BulkResult> {
  const result: BulkResult = { changed: [], skipped: [], failed: [], notTried: [] }
  const todo: { key: string; body: Body }[] = []
  for (const it of items) {
    const body = bulkBody(t, op, it)
    if (body === null) result.skipped.push(it.key)
    else todo.push({ key: it.key, body })
  }
  let next = 0
  let settled = 0
  let streak = 0
  const worker = async () => {
    while (next < todo.length && streak < STOP_AFTER) {
      const job = todo[next++]!
      try {
        await send(job.key, job.body)
        result.changed.push(job.key)
        streak = 0
      } catch (e) {
        result.failed.push({ key: job.key, error: String((e as Error)?.message ?? e) })
        streak++
      }
      settled++
      opts.onProgress?.(settled, todo.length)
    }
  }
  await Promise.all(Array.from({ length: Math.max(1, Math.min(opts.concurrency ?? 3, todo.length)) }, worker))
  result.notTried = todo.slice(next).map((j) => j.key)
  return result
}
