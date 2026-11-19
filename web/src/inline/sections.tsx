import { useId, useRef, useState, type ReactNode } from "react"
import { refOf } from "../keys"
import type { Issue, Item } from "../types"
import {
  ATTACHMENT_LIMIT, LINK_LABEL, RELATION_INVERSE, RELATION_LABEL, type Body, type FilePayload, type Target,
  attachmentRemove, attachmentsAdd, commentChange, deleteCommentChange, editCommentChange, fieldChange, fieldValue,
  relationAdd, relationRemove, timeEntryChange, watcherAdd, watcherRemove,
} from "./changes"
import { AreaControl, FieldMessages, Pencil, opt, useFieldEdit } from "./field"
import { AreaProp, ListProp, SwitchProp, TextProp } from "./fields"
import type { EditSession } from "./session"

// The sections an item gets in edit mode: its properties, relations, watchers, files and time, the composer and the
// comments' edit and delete, and the description. Each one saves what it changes on its own.

/** "Remove?" with Yes and No, in place of the × that asks. */
export function ConfirmRemove({ label, onConfirm, disabled = false, question = "Remove?" }: { label: string; onConfirm: () => void; disabled?: boolean; question?: string }) {
  const [asking, setAsking] = useState(false)
  if (!asking) {
    return <button type="button" className="link-btn inline-remove" aria-label={label} title={label} disabled={disabled} onClick={() => setAsking(true)}>×</button>
  }
  return (
    <span className="inline-confirm">
      {question}{" "}
      <button type="button" onClick={() => { setAsking(false); onConfirm() }}>Yes</button>{" "}
      <button type="button" onClick={() => setAsking(false)}>No</button>
    </span>
  )
}

const dash = "—"
const nameOf = (list: { id: number | string; name: string }[] | undefined, id: unknown) => list?.find((x) => x.id === id)?.name

/** The item's own values: those the record gives, each one a control of its own. */
export function Properties({ t, item, session }: { t: Target; item: Item; session: EditSession }) {
  const { rec, opts } = session
  const ready = !!rec
  const rows: ReactNode[] = []
  const row = (label: string, node: ReactNode) => <div className="field inline-row" key={label}><dt>{label}</dt><dd>{node}</dd></div>

  if (t.host === "redmine") {
    const tracker = rec?.tracker?.name ?? dash
    rows.push(row("Tracker", <ListProp t={t} field="tracker" label="Tracker" session={session} enabled={ready} display={<span>{tracker}</span>} valueText={tracker} options={opts ? opts.trackers.map((x) => opt(x.id, x.name)) : null} />))
    const priority = rec?.priority?.name ?? dash
    rows.push(row("Priority", <ListProp t={t} field="priority" label="Priority" session={session} enabled={ready} display={<span>{priority}</span>} valueText={priority} options={opts ? opts.priorities.map((x) => opt(x.id, x.name)) : null} />))
    const version = nameOf(opts?.versions, rec?.versionId) ?? item.milestone?.title ?? dash
    rows.push(row("Version", <ListProp t={t} field="version" label="Version" session={session} enabled={ready} display={<span>{version}</span>} valueText={version} options={opts ? [opt(null, "(none)"), ...opts.versions.map((x) => opt(x.id, x.name))] : null} />))
    const category = nameOf(opts?.categories, rec?.categoryId) ?? dash
    rows.push(row("Category", <ListProp t={t} field="category" label="Category" session={session} enabled={ready} display={<span>{category}</span>} valueText={category} options={opts ? [opt(null, "(none)"), ...opts.categories.map((x) => opt(x.id, x.name))] : null} />))
    const start = rec?.startDate || dash
    rows.push(row("Start date", <TextProp t={t} field="startDate" label="Start date" type="date" session={session} enabled={ready} display={<span>{start}</span>} valueText={start} />))
    const due = rec?.dueDate || dash
    rows.push(row("Due date", <TextProp t={t} field="dueDate" label="Due date" type="date" session={session} enabled={ready} display={<span>{due}</span>} valueText={due} />))
    const done = `${rec?.doneRatio ?? 0} %`
    rows.push(row("Done", <TextProp t={t} field="doneRatio" label="Done ratio (%)" type="number" session={session} enabled={ready} display={<span>{done}</span>} valueText={done} />))
    const hours = rec?.estimatedHours == null ? dash : String(rec.estimatedHours)
    rows.push(row("Estimated hours", <TextProp t={t} field="estimatedHours" label="Estimated hours" type="number" session={session} enabled={ready} display={<span>{hours}</span>} valueText={hours} />))
    rows.push(row("Private", <SwitchProp t={t} field="isPrivate" label="Private issue" session={session} enabled={ready} />))
  } else if (t.pull) {
    rows.push(row("Draft", <SwitchProp t={t} field="isDraft" label="Draft" session={session} enabled={ready} />))
    const reviewers = (rec?.reviewers ?? []).join(", ") || dash
    rows.push(row("Reviewers", <ListProp t={t} field="reviewers" label="Reviewers" multi session={session} enabled={ready} display={<span>{reviewers}</span>} valueText={reviewers} options={opts ? opts.members.map((m) => opt(m.id, m.name)) : null} />))
  } else {
    if (t.host === "github" && (opts?.issueTypes ?? []).length > 0) {
      const type = rec?.issueType ?? "none"
      rows.push(row("Type", <ListProp t={t} field="type" label="Type" session={session} enabled={ready} display={<span>{type}</span>} valueText={type} options={opts ? [opt(null, "(no type)"), ...(opts.issueTypes ?? []).map((x) => opt(x.name, x.name))] : null} />))
    }
    if (t.host === "github") rows.push(row("Locked", <SwitchProp t={t} field="locked" label="Lock the conversation" session={session} enabled={ready} />))
    if (t.host === "gitlab") {
      const due = rec?.dueDate || dash
      rows.push(row("Due date", <TextProp t={t} field="dueDate" label="Due date" type="date" session={session} enabled={ready} display={<span>{due}</span>} valueText={due} />))
      rows.push(row("Confidential", <SwitchProp t={t} field="confidential" label="Confidential" session={session} enabled={ready} />))
      const weight = rec?.weight == null ? dash : String(rec.weight)
      rows.push(row("Weight", <TextProp t={t} field="weight" label="Weight" type="number" session={session} enabled={ready} display={<span>{weight}</span>} valueText={weight} />))
      rows.push(row("Time estimate", <TextProp t={t} field="timeEstimate" label="Time estimate" placeholder="for example 2h" session={session} enabled={ready} display={<span className="muted">set…</span>} valueText="not set" />))
      rows.push(row("Add time spent", <TextProp t={t} field="addSpent" label="Add time spent" placeholder="for example 30m" session={session} enabled={ready} display={<span className="muted">add…</span>} valueText="none" />))
    }
  }
  if (rows.length === 0) return null
  return (
    <section className="detail-section fields inline-props">
      <h3>Properties</h3>
      <dl>{rows}</dl>
    </section>
  )
}

/** A relation the issue has now, as the list shows it. */
interface RelRow { key: string; label: string; other: string; remove: Body }

/** The parent, the relations (remove each with a click and a confirm), and "+ Add" for a new one. Issues only. */
export function Relations({ t, item, issues, session, onSelect }: { t: Target; item: Item; issues: Issue[]; session: EditSession; onSelect?: (i: Item) => void }) {
  const { rec, opts } = session
  const ready = !!rec
  const idNum = Number(item.key)
  const act = useFieldEdit((body) => session.save(body))
  const [adding, setAdding] = useState(false)
  const [type, setType] = useState("")
  const [to, setTo] = useState("")
  const [bad, setBad] = useState(false)
  const listId = useId()
  const titleOf = (key: string) => issues.find((i) => i.key === key)?.title ?? ""

  const rows: RelRow[] = []
  if (t.host === "redmine") {
    for (const r of (rec?.relations ?? []) as { id: number; type: string; from: number; to: number }[]) {
      const mine = r.from === idNum
      const other = String(mine ? r.to : r.from)
      const label = mine ? RELATION_LABEL[r.type] ?? r.type : RELATION_INVERSE[r.type] ?? r.type
      rows.push({ key: `r${r.id}`, label, other, remove: relationRemove(t, { id: r.id }) })
    }
  } else {
    for (const k of (rec?.blockedBy ?? []) as string[]) rows.push({ key: `blockedBy-${k}`, label: "Blocked by", other: k, remove: relationRemove(t, { type: "blockedBy", to: k }) })
    for (const k of (rec?.blocking ?? []) as string[]) rows.push({ key: `blocks-${k}`, label: "Blocks", other: k, remove: relationRemove(t, { type: "blocks", to: k }) })
  }
  const types = t.host === "redmine"
    ? (opts?.relationTypes ?? []).map((x) => ({ value: x, label: RELATION_LABEL[x] ?? x }))
    : (t.host === "gitlab" ? ["blocks", "blockedBy", "relates"] : ["blocks", "blockedBy"]).map((x) => ({ value: x, label: LINK_LABEL[x] ?? x }))
  const chosenType = type || types[0]?.value || ""
  const parentKey = fieldValue(t, "parent", rec, opts)
  const parentText = !ready ? dash : parentKey ? `${refOf(String(parentKey))} ${titleOf(String(parentKey))}`.trim() : "(none)"
  const parentOptions = opts ? [opt(null, "(none)"), ...issues.filter((i) => i.key !== item.key).map((i) => opt(t.host === "redmine" ? Number(i.key) : i.key, `${refOf(i.key)} ${i.title}`))] : null

  const add = async () => {
    const n = to.trim()
    if (!/^\d+$/.test(n)) { setBad(true); return }
    setBad(false)
    if (await act.run(relationAdd(t, chosenType, n), true)) { setTo(""); setAdding(false) }
  }

  return (
    <section className="detail-section relations-section inline-relations">
      <h3>Relations</h3>
      <div className="field inline-row">
        <dt>Parent</dt>
        <dd>
          <ListProp t={t} field="parent" label="Parent" session={session} enabled={ready} display={<span>{parentText}</span>} valueText={parentText} options={parentOptions} />
        </dd>
      </div>
      {rows.length === 0 ? <p className="muted small">No relations.</p> : (
        <ul className="related">
          {rows.map((r) => (
            <li key={r.key}>
              {r.label}{" "}
              {onSelect ? <button className="link-btn" onClick={() => { const it = issues.find((i) => i.key === r.other); if (it) onSelect(it) }}>{refOf(r.other)}</button> : <strong>{refOf(r.other)}</strong>}{" "}
              <span className="muted">{titleOf(r.other)}</span>{" "}
              <ConfirmRemove label={`Remove: ${r.label} ${refOf(r.other)}`} disabled={act.busy} onConfirm={() => void act.run(r.remove, true)} />
            </li>
          ))}
        </ul>
      )}
      {adding ? (
        <form className="inline-add" onSubmit={(e) => { e.preventDefault(); void add() }} onKeyDown={(e) => { if (e.key === "Escape") { e.stopPropagation(); setAdding(false) } }}>
          <select aria-label="Relation type" value={chosenType} onChange={(e) => setType(e.target.value)}>
            {types.map((x) => <option key={x.value} value={x.value}>{x.label}</option>)}
          </select>
          <input aria-label="Other issue's number" inputMode="numeric" list={listId} placeholder="issue number" value={to} onChange={(e) => setTo(e.target.value)} className="inline-input" />
          <datalist id={listId}>{issues.filter((i) => i.key !== item.key).map((i) => <option key={i.key} value={i.key}>{i.title}</option>)}</datalist>
          <span className="inline-actions">
            <button type="button" onClick={() => setAdding(false)}>Cancel</button>
            <button type="submit" className="primary" disabled={act.busy || !to.trim()}>Add relation</button>
          </span>
          {bad && <span role="alert" className="inline-error">Give the number of the other issue.</span>}
        </form>
      ) : (
        <button type="button" className="inline-add-btn" disabled={!ready || act.busy} onClick={() => setAdding(true)}>+ Add</button>
      )}
      <FieldMessages edit={act} />
    </section>
  )
}

/** Read a picked file as the form did: base64 in the request, the name and its type. */
export function readFile(f: File): Promise<FilePayload> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.onload = () => resolve({ filename: f.name, contentType: f.type || "application/octet-stream", data: String(reader.result).split(",")[1] ?? "" })
    reader.onerror = () => reject(reader.error)
    reader.readAsDataURL(f)
  })
}

/** Watchers (who follows the issue) and files (Redmine): each added or removed on its own. */
export function WatchersAndFiles({ session }: { session: EditSession }) {
  const { rec, opts } = session
  const act = useFieldEdit((body) => session.save(body))
  const [pickError, setPickError] = useState<string | null>(null)
  const input = useRef<HTMLInputElement>(null)
  const watchers = (rec?.watchers ?? []) as { id: number; name: string }[]
  const files = (rec?.attachments ?? []) as { id: number; filename: string }[]
  const notWatching = (opts?.members ?? []).filter((m) => !watchers.some((w) => w.id === Number(m.id)))

  const addFiles = async (list: File[]) => {
    setPickError(null)
    if (list.length > ATTACHMENT_LIMIT.files) { setPickError(`At most ${ATTACHMENT_LIMIT.files} files at a time.`); return }
    const ok = list.filter((f) => {
      if (f.size > ATTACHMENT_LIMIT.bytes) { setPickError(`${f.name} is larger than 5 MB.`); return false }
      return true
    })
    if (ok.length === 0) return
    const payload = await Promise.all(ok.map(readFile))
    void act.run(attachmentsAdd(payload), true)
  }

  return (
    <>
      <section className="detail-section watchers-section inline-watchers">
        <h3>Watchers ({watchers.length})</h3>
        {watchers.length === 0 ? <p className="muted small">Nobody is watching.</p> : (
          <ul className="related">
            {watchers.map((w) => (
              <li key={w.id}>
                {w.name}{" "}
                <ConfirmRemove label={`Stop watching: ${w.name}`} question="Stop watching?" disabled={act.busy} onConfirm={() => void act.run(watcherRemove(w.id), true)} />
              </li>
            ))}
          </ul>
        )}
        <select aria-label="Add a watcher" value="" disabled={act.busy || !opts} onChange={(e) => { if (e.target.value) void act.run(watcherAdd(e.target.value), true) }}>
          <option value="">Add a watcher…</option>
          {notWatching.map((m) => <option key={String(m.id)} value={String(m.id)}>{m.name}</option>)}
        </select>
      </section>
      <section className="detail-section files-section inline-files">
        <h3>Files ({files.length})</h3>
        {files.length > 0 && (
          <ul className="related">
            {files.map((a) => (
              <li key={a.id}>
                {a.filename}{" "}
                <ConfirmRemove label={`Remove file: ${a.filename}`} disabled={act.busy} onConfirm={() => void act.run(attachmentRemove(a.id), true)} />
              </li>
            ))}
          </ul>
        )}
        <div
          className="inline-drop"
          onDragOver={(e) => e.preventDefault()}
          onDrop={(e) => { e.preventDefault(); void addFiles(Array.from(e.dataTransfer.files)) }}
        >
          <span className="muted small">Drop files here, up to 5 at a time, 5 MB each.</span>
          <button type="button" disabled={act.busy} onClick={() => input.current?.click()}>Choose files</button>
          <input ref={input} type="file" multiple hidden aria-label="Files to attach" onChange={(e) => { const list = Array.from(e.target.files ?? []); e.target.value = ""; void addFiles(list) }} />
        </div>
        {pickError && <span role="alert" className="inline-error">{pickError}</span>}
        <FieldMessages edit={act} />
      </section>
    </>
  )
}

/** A time entry (Redmine): hours, the activity and what was done, in a small form. */
export function TimeLog({ session }: { session: EditSession }) {
  const { opts } = session
  const [open, setOpen] = useState(false)
  const [hours, setHours] = useState("")
  const [activity, setActivity] = useState("")
  const [comment, setComment] = useState("")
  const [bad, setBad] = useState(false)
  const act = useFieldEdit((body) => session.save(body))
  const log = async () => {
    if (!(Number(hours) > 0)) { setBad(true); return }
    setBad(false)
    if (await act.run(timeEntryChange({ hours, activity, comment }), true)) { setOpen(false); setHours(""); setActivity(""); setComment("") }
  }
  return (
    <section className="detail-section time-section inline-time-section">
      <h3>Time</h3>
      {!open ? (
        <button type="button" disabled={!opts} onClick={() => setOpen(true)}>Log time</button>
      ) : (
        <form className="inline-time" onSubmit={(e) => { e.preventDefault(); void log() }} onKeyDown={(e) => { if (e.key === "Escape") { e.stopPropagation(); setOpen(false) } }}>
          <input aria-label="Hours" type="number" min={0} step="0.25" placeholder="hours" className="inline-input" value={hours} onChange={(e) => setHours(e.target.value)} />
          <select aria-label="Activity" value={activity} onChange={(e) => setActivity(e.target.value)}>
            <option value="">activity…</option>
            {(opts?.activities ?? []).map((a) => <option key={a.id} value={a.id}>{a.name}</option>)}
          </select>
          <input aria-label="What was done" placeholder="what was done" className="inline-input" value={comment} onChange={(e) => setComment(e.target.value)} />
          <span className="inline-actions">
            <button type="button" onClick={() => setOpen(false)}>Cancel</button>
            <button type="submit" className="primary" disabled={act.busy}>{act.busy ? "Logging…" : "Log time"}</button>
          </span>
          {bad && <span role="alert" className="inline-error">Give the hours (more than 0).</span>}
          <FieldMessages edit={act} />
        </form>
      )}
    </section>
  )
}

/** The composer under the comments: a comment for everyone, or (Redmine) a private note. */
export function CommentComposer({ t, session }: { t: Target; session: EditSession }) {
  const [text, setText] = useState("")
  const [priv, setPriv] = useState(false)
  const act = useFieldEdit((body) => session.save(body))
  const body = commentChange(text, priv && t.host === "redmine")
  const send = async () => {
    if (!body) return
    if (await act.run(body, false)) { setText(""); setPriv(false) }
  }
  return (
    <div className="inline-composer">
      <textarea
        aria-label="New comment"
        rows={3}
        placeholder="Write a comment…"
        value={text}
        onChange={(e) => setText(e.target.value)}
        onKeyDown={(e) => { if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) { e.preventDefault(); void send() } }}
      />
      {t.host === "redmine" && (
        <label className="check"><input type="checkbox" checked={priv} onChange={(e) => setPriv(e.target.checked)} /> Private note (only for those who can see private notes)</label>
      )}
      <span className="inline-actions">
        <button type="button" className="primary" disabled={!body || act.busy} onClick={() => void send()}>{act.busy ? "Sending…" : "Comment"}</button>
      </span>
      <FieldMessages edit={act} />
    </div>
  )
}

/** A trash can, for the icon buttons. */
export function Trash() {
  return (
    <svg viewBox="0 0 16 16" aria-hidden="true">
      <path d="M2.5 4.5h11M6 4.5V3h4v1.5M4 4.5l.7 8.5h6.6l.7-8.5" />
    </svg>
  )
}

/** One comment: its text as the panel shows it, and (where the host lets it) Edit in place and Delete with a confirm.
 *  The two icon buttons appear when the comment is hovered or focused. */
export function CommentRow({ c, when, session, editable, children }: {
  c: { id: string; author: string | null; body: string }; when: string; session: EditSession; editable: boolean; children: ReactNode
}) {
  const edit = useFieldEdit((draft) => session.save(editCommentChange(c.id, draft)))
  const del = useFieldEdit(() => session.save(deleteCommentChange(c.id)))
  const [asking, setAsking] = useState(false)
  const showActions = editable && !edit.editing
  return (
    <div className={`comment${editable ? " inline-reveal-host" : ""}`}>
      <div className="comment-header">
        <strong>{c.author}</strong> <span className="muted">· {when}</span>
        {asking && (
          <span className="inline-confirm comment-confirm">
            {" "}Delete?{" "}
            <button type="button" onClick={() => { setAsking(false); void del.run(true, true) }}>Yes</button>{" "}
            <button type="button" onClick={() => setAsking(false)}>No</button>
          </span>
        )}
      </div>
      {showActions && (
        <>
          <button type="button" className="inline-icon-btn" style={{ right: 34 }} aria-label={`Edit comment by ${c.author ?? "unknown"}`} title="Edit comment" disabled={edit.busy || del.busy} onClick={edit.begin}>
            <Pencil />
          </button>
          <button type="button" className="inline-icon-btn danger" style={{ right: 6 }} aria-label={`Delete comment by ${c.author ?? "unknown"}`} title="Delete comment" disabled={edit.busy || del.busy} onClick={() => setAsking(true)}>
            <Trash />
          </button>
        </>
      )}
      {edit.editing ? (
        <AreaControl
          label="Edit comment"
          initial={c.body}
          rows={4}
          onSave={(draft, refocus) => (draft.trim() === c.body.trim() ? edit.cancel(refocus) : void edit.run(draft, refocus))}
          onCancel={(r) => edit.cancel(r)}
        />
      ) : (
        <div className="comment-body">{children}</div>
      )}
      <FieldMessages edit={edit} />
      <FieldMessages edit={del} />
    </div>
  )
}

/** The description, editable in place: the whole text (the "Key: value" lines too) is what the editor shows. A pencil
 *  appears at the block's corner on hover or focus. */
export function DescriptionSection({ t, session, enabled, hasText, children }: { t: Target; session: EditSession; enabled: boolean; hasText: boolean; children: ReactNode }) {
  const { rec, opts } = session
  const edit = useFieldEdit((draft) => {
    const body = fieldChange(t, "body", draft, rec, opts)
    return body ? session.save(body) : undefined
  })
  const commit = (draft: string, refocus: boolean) => {
    if (!fieldChange(t, "body", draft, rec, opts)) return edit.cancel(refocus)
    void edit.run(draft, refocus)
  }
  return (
    <section className={`detail-section desc-section${enabled ? " inline-reveal-host" : ""}`}>
      <h3>Description{edit.busy && <span className="inline-spinner" role="status" aria-label="Saving description" style={{ marginLeft: 8 }} />}</h3>
      {enabled && !edit.editing && (
        <button type="button" className="inline-icon-btn" style={{ right: 6, top: 4 }} disabled={edit.busy} onClick={edit.begin} aria-label="Edit description" title="Edit description">
          <Pencil />
        </button>
      )}
      {edit.editing ? (
        <AreaControl label="Description" initial={rec?.body ?? ""} rows={10} onSave={commit} onCancel={(r) => edit.cancel(r)} />
      ) : hasText ? children : <p className="muted small">No description.</p>}
      <FieldMessages edit={edit} />
    </section>
  )
}

/** A custom field of the record, by the control its format needs (the form's own rules). */
export function CustomProp({ t, session, field, display, valueText, enabled }: {
  t: Target; session: EditSession; field: { id: number; name: string; format: string | null; multiple: boolean }
  display: ReactNode; valueText: string; enabled: boolean
}) {
  const opts = session.opts
  if (!enabled) return <>{display}</>
  const def = opts?.customFields.find((f) => f.id === field.id)
  const format = def?.format ?? field.format
  const multiple = def?.multiple ?? field.multiple
  const values = def?.values ?? []
  const name = `${field.name}`
  const key = `cf:${field.id}`
  if (format === "bool") return <SwitchProp t={t} field={key} label={name} session={session} custom />
  if (multiple && values.length) return <ListProp t={t} field={key} label={name} session={session} enabled={enabled} multi display={display} valueText={valueText} options={values.map((x) => opt(x, x))} />
  if (values.length) return <ListProp t={t} field={key} label={name} session={session} enabled={enabled} display={display} valueText={valueText} options={[opt("", "(none)"), ...values.map((x) => opt(x, x))]} />
  if (format === "user") return <ListProp t={t} field={key} label={name} session={session} enabled={enabled} display={display} valueText={valueText} options={opts ? [opt("", "(none)"), ...opts.members.map((m) => opt(String(m.id), m.name))] : null} />
  if (format === "text") return <AreaProp t={t} field={key} label={name} session={session} enabled={enabled} display={display} valueText={valueText} />
  const type = format === "int" || format === "float" ? "number" : format === "date" ? "date" : "text"
  const parse = multiple && type === "text" ? (d: string) => d.split(",").map((x) => x.trim()).filter(Boolean) : undefined
  return <TextProp t={t} field={key} label={name} type={type} parse={parse} session={session} enabled={enabled} display={display} valueText={valueText} />
}

/** Whether a field of this item is editable now (the record is read, and the host allows it). */
export const canEditItem = (repo: string, item: Item, writable: boolean) =>
  writable && !repo.startsWith("jira:") && (item.kind === "issue" || !repo.startsWith("redmine:"))

