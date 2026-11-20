import { useEffect, useMemo, useState } from "react"
import { editIssue, fetchEditOptions, type EditOptions } from "./api"
import { BULK_LABEL, bulkBody, bulkKinds, runBulk, type BulkKind, type BulkOp, type BulkResult } from "./bulk"
import { targetOf } from "./inline/changes"
import { ENTITY_NAME, NewEntityForm, entityOption, withCreated } from "./inline/NewEntity"
import type { EntityKind } from "./api"
import { refOf } from "./keys"
import type { Issue } from "./types"

/** The value of "(unassigned)" / "(none)": clearing is a choice of its own, never what an untouched control means. */
const CLEAR = "__clear__"

type Phase = { name: "idle" } | { name: "confirm" } | { name: "running"; settled: number; total: number } | { name: "done"; result: BulkResult }

/**
 * One change for the selected issues. It shows how many would change before it sends anything (the ones already that way are
 * skipped), asks to confirm, then sends one edit per issue a few at a time and reports what failed.
 */
export function BulkBar({ repo, items, onApplied, onClear }: {
  repo: string
  items: Issue[]
  /** Called when a run ends, with what it did: the page reads the repo again. */
  onApplied: (r: BulkResult) => void
  onClear: () => void
}) {
  const t = targetOf(repo)
  const kinds = bulkKinds(t)
  const [opts, setOpts] = useState<EditOptions | null>(null)
  const [loadError, setLoadError] = useState<string | null>(null)
  const [kind, setKind] = useState<BulkKind>("assign")
  const [value, setValue] = useState("") // the chosen option's key; "" is nothing chosen yet, CLEAR is "(unassigned)" or "(none)"
  const [date, setDate] = useState("")
  const [clearDate, setClearDate] = useState(false)
  const [phase, setPhase] = useState<Phase>({ name: "idle" })
  const [adding, setAdding] = useState(false)

  useEffect(() => {
    let live = true
    fetchEditOptions(repo).then((o) => { if (live) setOpts(o) }, (e) => { if (live) setLoadError(String((e as Error).message ?? e)) })
    return () => { live = false }
  }, [repo])
  // a new kind starts from a clean value, and a finished run is forgotten when the selection changes
  useEffect(() => { setValue(""); setDate(""); setClearDate(false); setAdding(false) }, [kind])
  // changing the controls forgets a pending confirmation and a finished run; changing the selection forgets only the confirmation
  // (a run's summary stays while its changed issues leave the selection)
  useEffect(() => { setPhase((p) => (p.name === "running" ? p : { name: "idle" })) }, [kind, value, date, clearDate])
  useEffect(() => { setPhase((p) => (p.name === "confirm" ? { name: "idle" } : p)) }, [items.length])

  const choices: { key: string; label: string }[] = useMemo(() => {
    if (!opts) return []
    switch (kind) {
      case "assign": return opts.members.map((m) => ({ key: String(m.id), label: m.name }))
      case "milestone": return (t.host === "redmine" ? opts.versions : opts.milestones ?? []).map((m) => ({ key: String(m.id), label: m.name }))
      case "addLabel":
      case "removeLabel": return (opts.labels ?? []).map((l) => ({ key: l.name, label: l.name }))
      default: return []
    }
  }, [opts, kind, t.host])

  /** The operation the controls describe, or null while they are incomplete. */
  const op: BulkOp | null = useMemo(() => {
    const picked = choices.find((c) => c.key === value)
    switch (kind) {
      case "assign": {
        if (value === CLEAR) return { kind, id: null, name: null }
        if (!picked) return null
        const member = opts?.members.find((m) => String(m.id) === value)
        return { kind, id: member!.id, name: member!.name }
      }
      case "milestone": {
        if (value === CLEAR) return { kind, id: null, name: null }
        return picked ? { kind, id: Number(value), name: picked.label } : null
      }
      case "addLabel":
      case "removeLabel": return picked ? { kind, name: picked.key } : null
      case "state": return value === "OPEN" || value === "CLOSED" ? { kind, to: value } : null
      case "dueDate": return clearDate ? { kind, date: null } : date ? { kind, date } : null
    }
  }, [kind, value, date, clearDate, choices, opts])

  /** What this action may create when the one it needs does not exist yet. */
  const createKind: EntityKind | null = kind === "addLabel" ? "label" : kind === "milestone" ? (t.host === "redmine" ? "version" : "milestone") : null

  const willChange = op ? items.filter((i) => bulkBody(t, op, i) !== null).length : 0

  const apply = async () => {
    if (!op) return
    setPhase({ name: "running", settled: 0, total: willChange })
    const result = await runBulk(items, t, op, (key, body) => editIssue(repo, key, body), {
      onProgress: (settled, total) => setPhase({ name: "running", settled, total }),
    })
    setPhase({ name: "done", result })
    onApplied(result)
  }

  const running = phase.name === "running"
  const needsChoice = kind === "assign" || kind === "milestone"

  return (
    <div className="bulk-bar" role="group" aria-label="Edit the selected issues">
      <strong>{items.length} selected</strong>
      <select value={kind} onChange={(e) => setKind(e.target.value as BulkKind)} disabled={running} aria-label="What to change">
        {kinds.map((k) => <option key={k} value={k}>{BULK_LABEL[k]}</option>)}
      </select>
      {kind === "state" && (
        <select value={value} onChange={(e) => setValue(e.target.value)} disabled={running} aria-label="State">
          <option value="">Choose…</option><option value="OPEN">Open</option><option value="CLOSED">Closed</option>
        </select>
      )}
      {kind === "dueDate" && (
        <>
          <input type="date" value={date} onChange={(e) => setDate(e.target.value)} disabled={running || clearDate} aria-label="Due date" />
          <label className="small"><input type="checkbox" checked={clearDate} onChange={(e) => setClearDate(e.target.checked)} disabled={running} /> clear it</label>
        </>
      )}
      {(needsChoice || kind === "addLabel" || kind === "removeLabel") && (
        <select value={value} onChange={(e) => setValue(e.target.value)} disabled={running || !opts} aria-label="Value">
          <option value="">Choose…</option>
          {needsChoice && <option value={CLEAR}>{kind === "assign" ? "(unassigned)" : "(none)"}</option>}
          {choices.map((c) => <option key={c.key} value={c.key}>{c.label}</option>)}
        </select>
      )}
      {createKind && !running && (adding ? (
        <NewEntityForm repo={repo} kind={createKind} onCancel={() => setAdding(false)}
          onCreated={(made) => { setOpts((o) => (o ? withCreated(o, createKind, made) : o)); setValue(entityOption(createKind, made).key); setAdding(false) }} />
      ) : (
        <button type="button" className="link-btn" onClick={() => setAdding(true)}>+ New {ENTITY_NAME[createKind]}…</button>
      ))}
      {phase.name === "confirm" ? (
        <>
          <span className="small">{willChange === 0 ? "Nothing would change." : `${willChange} of ${items.length} would change.`}</span>
          <button onClick={() => void apply()} disabled={willChange === 0}>Confirm</button>
          <button onClick={() => setPhase({ name: "idle" })}>Cancel</button>
        </>
      ) : (
        <button onClick={() => setPhase({ name: "confirm" })} disabled={!op || running || !opts}>Apply…</button>
      )}
      <button onClick={onClear} disabled={running} title="Clear the selection">Clear</button>
      {running && <span className="small muted" role="status">Sending {phase.settled} of {phase.total}…</span>}
      {loadError && <span className="small error">{loadError}</span>}
      {phase.name === "done" && <BulkSummary result={phase.result} />}
    </div>
  )
}

function BulkSummary({ result: r }: { result: BulkResult }) {
  const failed = r.failed.length + r.notTried.length
  return (
    <div className="bulk-summary small" role="status">
      <span className={failed ? "warn" : "muted"}>
        {r.changed.length} changed{r.skipped.length ? `, ${r.skipped.length} already were` : ""}{r.failed.length ? `, ${r.failed.length} failed` : ""}
        {r.notTried.length ? `, ${r.notTried.length} not tried (stopped after repeated failures)` : ""}.
      </span>
      {r.failed.length > 0 && (
        <ul>
          {r.failed.slice(0, 5).map((f) => <li key={f.key}>{refOf(f.key)}: {f.error}</li>)}
          {r.failed.length > 5 && <li>… and {r.failed.length - 5} more</li>}
        </ul>
      )}
    </div>
  )
}
