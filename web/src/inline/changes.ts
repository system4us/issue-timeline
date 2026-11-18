import type { EditOptions } from "../api"

// The PATCH bodies the inline editors send. Ported from the two edit forms this replaces (EditIssue for Redmine,
// EditHostIssue for GitHub, GitLab and their pull or merge requests): the field names and the conversions are the ones
// the server reads. Each field reads its current value from the record the way the form did, so a control that
// commits what is already there sends nothing.

export type Host = "redmine" | "github" | "gitlab"
export interface Target { host: Host; pull: boolean }
export type Body = Record<string, unknown>
type Lists = EditOptions | null
type Rec = any

export function targetOf(repo: string, pull = false): Target {
  return { host: repo.startsWith("redmine:") ? "redmine" : repo.startsWith("gitlab:") ? "gitlab" : "github", pull }
}

/** Redmine's relation names, as the form showed them (from this issue's side). */
export const RELATION_LABEL: Record<string, string> = {
  relates: "Relates to", duplicates: "Duplicates", duplicated: "Duplicated by", blocks: "Blocks", blocked: "Blocked by",
  precedes: "Precedes", follows: "Follows", copied_to: "Copied to", copied_from: "Copied from",
}
/** The other side of a Redmine relation, when this issue is the one it points at. */
export const RELATION_INVERSE: Record<string, string> = {
  blocks: "Blocked by", blocked: "Blocks", precedes: "Follows", follows: "Precedes", copied_to: "Copied from",
  copied_from: "Copied to", duplicates: "Duplicated by", duplicated: "Duplicates",
}
/** GitHub and GitLab links: a blocked-by or blocks relation, and (GitLab) relates to. */
export const LINK_LABEL: Record<string, string> = { blocks: "Blocks", blockedBy: "Blocked by", relates: "Relates to" }

/** Equal as the form compared them: null and undefined alike, and lists whatever their order. */
export function same(a: unknown, b: unknown): boolean {
  const norm = (x: unknown) => (Array.isArray(x) ? JSON.stringify([...x].map((y) => JSON.stringify(y)).sort()) : JSON.stringify(x ?? null))
  return norm(a) === norm(b)
}

const idsByName = (names: string[] | undefined, list: { id: number | string; name: string }[] = []) =>
  list.filter((m) => (names ?? []).includes(m.name)).map((m) => m.id)

const customValue = (r: Rec, id: number) => {
  const f = ((r?.customFields ?? []) as { id: number; value?: unknown; multiple?: boolean }[]).find((x) => x.id === id)
  if (!f) return ""
  return f.value ?? (f.multiple || Array.isArray(f.value) ? [] : "")
}

/** The state as the menu shows it: "CLOSED:completed" when GitHub gave a reason (lower case, as the form sent it). */
function stateValue(r: Rec, t: Target): string {
  const state = r?.state ?? "OPEN"
  const reason = r?.stateReason ? String(r.stateReason).toLowerCase() : ""
  return t.host === "github" && !t.pull && state === "CLOSED" && reason ? `CLOSED:${reason}` : state
}

interface Spec {
  /** The value as the form showed it, from the record. */
  read: (r: Rec, o: Lists) => unknown
  /** How a control's value is brought to the form's shape before it is compared and sent. */
  norm?: (v: any) => unknown
  /** The PATCH body that sets it, or null when there is nothing to send. */
  body: (v: any) => Body | null
}

const text = (k: "title" | "body"): Spec => ({ read: (r) => r?.[k] ?? "", body: (v) => ({ [k]: v }) })

function redmineSpecs(): Record<string, Spec> {
  const plain = (k: string, read: (r: Rec) => unknown): Spec => ({ read, body: (v) => ({ [k]: v }) })
  return {
    title: text("title"),
    body: text("body"),
    state: plain("state", (r) => r?.state ?? "OPEN"),
    tracker: plain("tracker", (r) => r?.tracker?.id ?? null),
    priority: plain("priority", (r) => r?.priority?.id ?? null),
    assignee: plain("assignee", (r) => r?.assigneeId ?? null),
    version: plain("version", (r) => r?.versionId ?? null),
    category: plain("category", (r) => r?.categoryId ?? null),
    parent: plain("parent", (r) => r?.parentId ?? null),
    startDate: { read: (r) => r?.startDate ?? "", norm: (v) => v ?? "", body: (v) => ({ startDate: v || null }) },
    dueDate: { read: (r) => r?.dueDate ?? "", norm: (v) => v ?? "", body: (v) => ({ dueDate: v || null }) },
    doneRatio: { read: (r) => r?.doneRatio ?? 0, norm: (v) => Number(v), body: (v) => ({ doneRatio: v }) },
    estimatedHours: {
      read: (r) => r?.estimatedHours ?? null,
      norm: (v) => (v === "" || v == null ? null : Number(v)),
      body: (v) => ({ estimatedHours: v }),
    },
    isPrivate: { read: (r) => r?.isPrivate === true, norm: (v) => v === true, body: (v) => ({ isPrivate: v }) },
  }
}

function hostSpecs(t: Target): Record<string, Spec> {
  const specs: Record<string, Spec> = {
    title: text("title"),
    body: text("body"),
    state: {
      read: (r) => stateValue(r, t),
      body: (v) => {
        const [state, reason] = String(v).split(":")
        return reason ? { state, stateReason: reason } : { state }
      },
    },
    labels: { read: (r) => (r?.labels ?? []).map((l: { name: string }) => l.name), body: (v) => ({ labels: v }) },
    assignees: { read: (r, o) => idsByName(r?.assignees, o?.members), body: (v) => ({ assignees: v }) },
    milestone: {
      read: (r, o) => (r?.milestone && o?.milestones?.find((m) => m.name === r.milestone.title)?.id) || null,
      body: (v) => ({ milestone: v }),
    },
  }
  if (t.pull) {
    specs.isDraft = { read: (r) => r?.isDraft === true, norm: (v) => v === true, body: (v) => ({ isDraft: v }) }
    specs.reviewers = { read: (r, o) => idsByName(r?.reviewers, o?.members), body: (v) => ({ reviewers: v }) }
    return specs
  }
  specs.parent = {
    read: (r) => r?.parent ?? "",
    norm: (v) => (v == null ? "" : String(v)),
    body: (v) => ({ parent: v === "" ? null : v }),
  }
  if (t.host === "gitlab") {
    specs.dueDate = { read: (r) => r?.dueDate ?? "", norm: (v) => v ?? "", body: (v) => ({ dueDate: v || null }) }
    specs.confidential = { read: (r) => r?.confidential === true, norm: (v) => v === true, body: (v) => ({ confidential: v }) }
    specs.weight = {
      read: (r) => (r?.weight == null ? "" : String(r.weight)),
      norm: (v) => (v === "" || v == null ? "" : String(v)),
      body: (v) => ({ weight: v === "" ? null : Number(v) }),
    }
    // Time is an action, not a value: an empty box sends nothing
    specs.timeEstimate = { read: () => "", norm: (v) => String(v ?? "").trim(), body: (v) => (v ? { timeEstimate: v } : null) }
    specs.addSpent = { read: () => "", norm: (v) => String(v ?? "").trim(), body: (v) => (v ? { addSpent: v } : null) }
  } else {
    specs.type = { read: (r) => r?.issueType ?? null, norm: (v) => v || null, body: (v) => ({ type: v }) }
    specs.locked = { read: (r) => r?.locked === true, norm: (v) => v === true, body: (v) => ({ locked: v }) }
  }
  return specs
}

/** The spec of one field of the form, for this kind of item. A custom field is "cf:<id>" (Redmine). */
function specFor(t: Target, field: string): Spec | undefined {
  if (t.host === "redmine") {
    if (field.startsWith("cf:")) {
      const id = Number(field.slice(3))
      return { read: (r) => customValue(r, id), body: (v) => ({ customFields: { [id]: v } }) }
    }
    return redmineSpecs()[field]
  }
  return hostSpecs(t)[field]
}

/** The current value of a field, as the form showed it (what a control starts with). */
export function fieldValue(t: Target, field: string, rec: Rec, opts: Lists = null): unknown {
  const spec = specFor(t, field)
  return spec ? spec.read(rec, opts) : undefined
}

/**
 * The PATCH body that sets one field to `value`, or null when the value is what the record already has (or the body
 * would be empty). A control commits its raw value; the spec brings it to the form's shape first.
 */
export function fieldChange(t: Target, field: string, value: unknown, rec: Rec, opts: Lists = null): Body | null {
  const spec = specFor(t, field)
  if (!spec) return null
  const next = spec.norm ? spec.norm(value) : value
  if (same(next, spec.read(rec, opts))) return null
  return spec.body(next)
}

// ---- actions: comments, relations, watchers, files, time. Each is one PATCH on its own.

/** A comment (a private note on Redmine when asked); empty text sends nothing. */
export function commentChange(body: string, privateNote = false): Body | null {
  const comment = body.trim()
  if (!comment) return null
  return privateNote ? { comment, privateNote: true } : { comment }
}

export const editCommentChange = (id: string, body: string): Body => ({ editComments: [{ id: String(id), body }] })
export const deleteCommentChange = (id: string): Body => ({ deleteComments: [String(id)] })

/** Adds a relation: Redmine's type with the other issue's id, or a GitHub/GitLab link by the other issue's number. */
export function relationAdd(t: Target, type: string, to: string | number): Body {
  return t.host === "redmine" ? { addRelations: [{ type, to: Number(to) }] } : { addLinks: [{ type, to: String(to) }] }
}

/** Removes a relation the issue has: Redmine's relation id, or a host link (type and the other issue's number). */
export function relationRemove(t: Target, rel: { id?: number; type?: string; to?: string }): Body {
  return t.host === "redmine" ? { removeRelations: [rel.id] } : { removeLinks: [{ type: rel.type, to: rel.to }] }
}

export const watcherAdd = (userId: number | string): Body => ({ addWatchers: [Number(userId)] })
export const watcherRemove = (userId: number): Body => ({ removeWatchers: [userId] })

export interface FilePayload { filename: string; contentType: string; data: string }
export const attachmentsAdd = (files: FilePayload[]): Body => ({ attachments: files })
export const attachmentRemove = (id: number): Body => ({ removeAttachments: [id] })

/** A time entry (Redmine): hours, the activity's id and what was done. */
export function timeEntryChange(p: { hours: string; activity: string; comment: string }): Body {
  return { timeEntry: { hours: Number(p.hours), activityId: Number(p.activity), comments: p.comment.trim() } }
}

/** The limits the attachments keep to, as the form did: five files at a time, five megabytes each. */
export const ATTACHMENT_LIMIT = { files: 5, bytes: 5 * 1024 * 1024 }
