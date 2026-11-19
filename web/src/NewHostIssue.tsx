import { useEffect, useState } from "react"
import { createPortal } from "react-dom"
import { createHostIssue, fetchEditOptions, type EditOptions } from "./api"
import type { Issue } from "./types"
import { refOf } from "./keys"
import { Picker, opt } from "./inline/field"
import "./inline.css"

/**
 * A new issue on a GitHub, GitLab or Redmine repo, as a side panel with the look of the detail panel: the title (focused),
 * the description, and the fields the create form had (labels, assignees and milestone on GitHub and GitLab, as the same
 * popover pickers the detail panel uses; tracker, priority, version, category, parent, dates, privacy and assignee on
 * Redmine). Create and Cancel are in its header. It is rendered into the page's main area, where the detail panel is,
 * so it takes the same place and size.
 */
export function NewHostIssue({ repo, issues = [], onClose, onCreated }: { repo: string; issues?: Issue[]; onClose: () => void; onCreated: () => void }) {
  const gitlab = repo.startsWith("gitlab:")
  const redmine = repo.startsWith("redmine:")
  // Redmine's own fields (a new issue there can be a subtask of another)
  const [tracker, setTracker] = useState<number | "">("")
  const [priority, setPriority] = useState<number | "">("")
  const [parent, setParent] = useState("")
  const [category, setCategory] = useState<number | "">("")
  const [startDate, setStartDate] = useState("")
  const [isPrivate, setIsPrivate] = useState(false)
  const [options, setOptions] = useState<EditOptions | null>(null)
  const [title, setTitle] = useState("")
  const [body, setBody] = useState("")
  const [labels, setLabels] = useState<string[]>([])
  const [assignees, setAssignees] = useState<(string | number)[]>([])
  const [milestone, setMilestone] = useState<number | null>(null)
  const [dueDate, setDueDate] = useState("")
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  useEffect(() => { fetchEditOptions(repo).then(setOptions, (e) => setError(String(e.message ?? e))) }, [repo])
  useEffect(() => {
    const esc = (e: KeyboardEvent) => { if (e.key === "Escape" && !busy) onClose() }
    document.addEventListener("keydown", esc)
    return () => document.removeEventListener("keydown", esc)
  }, [onClose, busy])

  const create = async () => {
    setBusy(true)
    setError(null)
    try {
      if (redmine) {
        await createHostIssue(repo, { title: title.trim(), body, tracker: tracker || null, priority: priority || null, parent: parent || null, category: category || null, version: milestone, startDate: startDate || null, dueDate: dueDate || null, isPrivate, assignee: assignees[0] ?? null } as any)
      } else {
        await createHostIssue(repo, { title: title.trim(), body, labels, assignees, milestone, dueDate: gitlab && dueDate ? dueDate : null })
      }
      onCreated()
      onClose()
    } catch (e) {
      setError(String((e as Error).message ?? e))
    } finally {
      setBusy(false)
    }
  }

  const host = redmine ? "Redmine" : gitlab ? "GitLab" : "GitHub"
  const mainArea = useState<HTMLElement | null>(() => document.querySelector<HTMLElement>("main"))[0]
  const labelList = (options?.labels ?? []).map((l) => ({ ...opt(l.name, l.name), color: l.color }))
  const memberList = (options?.members ?? []).map((m) => opt(m.id, m.name))
  const milestoneList = [opt(null, "(none)"), ...(options?.milestones ?? []).map((m) => opt(m.id, m.name))]
  const namesOf = (keys: string[], list: { key: string; label: string }[]) => list.filter((o) => keys.includes(o.key)).map((o) => o.label)
  const panel = (
    <aside className="detail new-issue" aria-labelledby="new-issue-title" onClick={(e) => e.stopPropagation()}>
      <header>
        <div className="detail-header-inner">
          <div className="detail-header-title">
            <h2 id="new-issue-title" className="new-issue-title">New issue <span className="muted small">· {host}</span></h2>
          </div>
          <div className="detail-actions">
            <button type="button" onClick={onClose} disabled={busy}>Cancel</button>
            <button type="submit" form="new-issue-form" className="primary" disabled={busy || !title.trim()}>{busy ? "Creating…" : "Create"}</button>
          </div>
        </div>
      </header>
      <form
        id="new-issue-form"
        className="detail-content new-issue-body"
        onSubmit={(e) => { e.preventDefault(); if (title.trim() && !busy) void create() }}
      >
        <label className="new-field">
          <span>Title</span>
          <input autoFocus className="inline-input" value={title} onChange={(e) => setTitle(e.target.value)} />
        </label>
        <label className="new-field">
          <span>Description</span>
          <textarea rows={8} value={body} onChange={(e) => setBody(e.target.value)} />
        </label>
        {!options ? <p className="muted small">Loading the lists…</p> : redmine ? (
          <>
            <div className="new-grid">
              <label className="new-field"><span>Tracker</span><select value={tracker} onChange={(e) => setTracker(e.target.value === "" ? "" : Number(e.target.value))}><option value="">(default)</option>{(options.trackers ?? []).map((t) => <option key={t.id} value={t.id}>{t.name}</option>)}</select></label>
              <label className="new-field"><span>Priority</span><select value={priority} onChange={(e) => setPriority(e.target.value === "" ? "" : Number(e.target.value))}><option value="">(default)</option>{(options.priorities ?? []).map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}</select></label>
              <label className="new-field"><span>Version</span><select value={milestone ?? ""} onChange={(e) => setMilestone(e.target.value === "" ? null : Number(e.target.value))}><option value="">(none)</option>{(options.versions ?? []).map((m) => <option key={m.id} value={m.id}>{m.name}</option>)}</select></label>
              <label className="new-field"><span>Category</span><select value={category} onChange={(e) => setCategory(e.target.value === "" ? "" : Number(e.target.value))}><option value="">(none)</option>{(options.categories ?? []).map((c) => <option key={c.id} value={c.id}>{c.name}</option>)}</select></label>
              <label className="new-field"><span>Parent (makes it a subtask)</span><select value={parent} onChange={(e) => setParent(e.target.value)}><option value="">(none)</option>{issues.map((i) => <option key={i.key} value={i.key}>{refOf(i.key)} {i.title}</option>)}</select></label>
              <label className="new-field"><span>Start date</span><input type="date" value={startDate} onChange={(e) => setStartDate(e.target.value)} /></label>
              <label className="new-field"><span>Due date</span><input type="date" value={dueDate} onChange={(e) => setDueDate(e.target.value)} /></label>
              <label className="new-field"><span>Assignee</span><select value={assignees[0] ?? ""} onChange={(e) => setAssignees(e.target.value === "" ? [] : [Number(e.target.value)])}><option value="">(none)</option>{options.members.map((m) => <option key={String(m.id)} value={m.id}>{m.name}</option>)}</select></label>
            </div>
            <label className="check"><input type="checkbox" checked={isPrivate} onChange={(e) => setIsPrivate(e.target.checked)} /> Private issue</label>
          </>
        ) : (
          <>
            <div className="new-grid">
              <div className="new-field">
                <span>Milestone</span>
                <Picker label="Milestone" options={milestoneList} selected={[milestone == null ? "" : String(milestone)]} onChange={(keys) => setMilestone(keys[0] ? Number(keys[0]) : null)} text={(options?.milestones ?? []).find((m) => m.id === milestone)?.name ?? "(none)"} />
              </div>
              {gitlab && <label className="new-field"><span>Due date</span><input type="date" value={dueDate} onChange={(e) => setDueDate(e.target.value)} /></label>}
            </div>
            <div className="new-field">
              <span>Labels</span>
              <Picker label="Labels" multi options={labelList} selected={labels} onChange={setLabels} text={labels.length ? labels.join(", ") : "(none)"} />
            </div>
            <div className="new-field">
              <span>Assignees</span>
              <Picker label="Assignees" multi options={memberList} selected={assignees.map(String)} onChange={(keys) => setAssignees(memberList.filter((o) => keys.includes(o.key)).map((o) => o.value as string | number))} text={namesOf(assignees.map(String), memberList).join(", ") || "(none)"} />
            </div>
          </>
        )}
        {error && <p className="error" role="alert">{error}</p>}
      </form>
    </aside>
  )
  return mainArea ? createPortal(panel, mainArea) : panel
}
