import { useState } from "react"
import { createEntity, type CreatedEntity, type EditOptions, type EntityKind } from "../api"
import type { Target } from "./changes"
import type { ListOption } from "./field"

const keyOf = (v: unknown) => (v === null || v === undefined ? "" : String(v))

/** What a host lets a person create, in the order a form offers it (the server answers 400 for any other). */
export const entityKinds = (t: Target): EntityKind[] => (t.host === "redmine" ? ["version", "category"] : ["label", "milestone"])

export const ENTITY_NAME: Record<EntityKind, string> = { label: "label", milestone: "milestone", version: "version", category: "category" }

/** A created item as a picker lists it. A label is chosen by its name; the others by id. */
export function entityOption(kind: EntityKind, c: CreatedEntity): ListOption {
  if (kind === "label") {
    const color = (c.color ?? "").replace(/^#/, "")
    return { key: keyOf(c.name), label: c.name, value: c.name, color: color || undefined }
  }
  return { key: keyOf(c.id), label: c.name, value: c.id }
}

/** The edit options with a just-created item added, so a picker offers it at once (a label of the same name is replaced). */
export function withCreated(o: EditOptions, kind: EntityKind, made: CreatedEntity): EditOptions {
  if (kind === "label") return { ...o, labels: [...(o.labels ?? []).filter((l) => l.name !== made.name), { name: made.name, color: (made.color ?? "").replace(/^#/, "") }] }
  const item = { id: made.id as number, name: made.name }
  if (kind === "milestone") return { ...o, milestones: [...(o.milestones ?? []), item] }
  if (kind === "version") return { ...o, versions: [...o.versions, item] }
  return { ...o, categories: [...o.categories, item] }
}

/** The name, and what the kind also takes (a colour for a label, a due date for a milestone or version). Creates on Enter or the button. */
export function NewEntityForm({ repo, kind, onCreated, onCancel }: {
  repo: string
  kind: EntityKind
  onCreated: (made: CreatedEntity) => void
  onCancel: () => void
}) {
  const [name, setName] = useState("")
  const [color, setColor] = useState("#0e8a16")
  const [dueOn, setDueOn] = useState("")
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const submit = async () => {
    if (!name.trim() || busy) return
    setBusy(true)
    setError(null)
    try {
      const made = await createEntity(repo, {
        kind, name: name.trim(),
        ...(kind === "label" ? { color: color.replace(/^#/, "") } : {}),
        ...((kind === "milestone" || kind === "version") && dueOn ? { dueOn } : {}),
      })
      onCreated(made)
    } catch (e) {
      setError(String((e as Error).message ?? e))
      setBusy(false)
    }
  }

  return (
    <span className="inline-newform" role="group" aria-label={`New ${ENTITY_NAME[kind]}`}
      onKeyDown={(e) => {
        if (e.key === "Enter") { e.preventDefault(); e.stopPropagation(); void submit() }
        if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); onCancel() }
      }}>
      <input autoFocus className="inline-input" aria-label={`Name of the new ${ENTITY_NAME[kind]}`} placeholder={`Name of the ${ENTITY_NAME[kind]}`} value={name} maxLength={200} onChange={(e) => setName(e.target.value)} disabled={busy} />
      {kind === "label" && <label className="small">Colour <input type="color" aria-label="Colour" value={color} onChange={(e) => setColor(e.target.value)} disabled={busy} /></label>}
      {(kind === "milestone" || kind === "version") && <label className="small">Due <input type="date" aria-label="Due date" value={dueOn} onChange={(e) => setDueOn(e.target.value)} disabled={busy} /></label>}
      {error && <span className="small error" role="alert">{error}</span>}
      <span className="inline-actions">
        <button type="button" onClick={onCancel} disabled={busy}>Cancel</button>
        <button type="button" className="primary" onClick={() => void submit()} disabled={busy || !name.trim()}>{busy ? "Creating…" : "Create"}</button>
      </span>
    </span>
  )
}
