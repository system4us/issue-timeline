import { useEffect, useState } from "react"
import { CommitDiff } from "./Commits"
import { fetchDetail, fetchMergeMethod, refreshRecord } from "./api"
import type { Detail as DetailT, Issue, Item, Link, Pull } from "./types"
import type { LinkIndex } from "./filters"
import { isDependencyKey, parseFields } from "./fields"
import { approvers } from "./reviews"
import { CiTrail, type CiSummary } from "./CiBadge"
import { refOf } from "./keys"
import { byKey } from "./keys"
import { isOverdue } from "./metrics"
import { fieldText, isLink, type CustomField } from "./customFields"
import { targetOf } from "./inline/changes"
import { useEditSession, type Saved } from "./inline/session"
import { opt } from "./inline/field"
import { ListProp, TextProp } from "./inline/fields"
import { canEditItem, CommentComposer, CommentRow, CustomProp, DescriptionSection, Properties, Relations, TimeLog, WatchersAndFiles } from "./inline/sections"
import type { EditSession } from "./inline/session"
import "./inline.css"

interface Props {
  item: Item
  repo: string
  issues: Issue[]
  /** Called after an edit is saved: the app reloads its data, and the panel reads the issue again. */
  onChanged?: () => void
  /** The repo is in write mode: only then is an issue editable. */
  writable?: boolean
  pulls: Pull[]
  links: Link[]
  idx: LinkIndex
  onSelect: (item: Item) => void
  onClose: () => void
  ci?: CiSummary // CI runs of the repo: each PR's last state and link
  expandRequest?: number // each increase opens the panel full screen (the timeline's "Open full screen")
}

const day = (s: string | null | undefined) => (s ? s.slice(0, 10) : "—")

export function DetailPanel({ item: itemProp, repo, issues, pulls, links, idx, onSelect, onClose, expandRequest = 0, ci, onChanged = () => {}, writable = false }: Props) {
  const [detail, setDetail] = useState<DetailT | null>(null)
  const [reread, setReread] = useState(0)
  const [error, setError] = useState<string | null>(null)
  const [allRelated, setAllRelated] = useState(false)
  const [expanded, setExpanded] = useState(false)
  // Edit mode: the item as the panel shows it (what a save answered, until the list reads again), its record and lists.
  const [patch, setPatch] = useState<{ base: Item; fields: Partial<Item> } | null>(null)
  const item = patch && patch.base === itemProp ? ({ ...itemProp, ...patch.fields } as Item) : itemProp
  const t = targetOf(repo, item.kind === "pull")
  const canEdit = canEditItem(repo, item, writable)
  const session = useEditSession({
    repo, key: item.key, pull: item.kind === "pull", enabled: canEdit,
    onSaved: (out) => { setPatch({ base: itemProp, fields: shownFields(out) }); setReread((n) => n + 1); onChanged() },
  })
  const editable = canEdit && session.rec !== null
  const opts = session.opts
  const stateOptions = t.host === "github" && !t.pull
    ? [opt("OPEN", "Open"), opt("CLOSED:completed", "Closed as completed"), opt("CLOSED:not_planned", "Closed as not planned")]
    : [opt("OPEN", "Open"), opt("CLOSED", "Closed")]
  // Commits opened from this item: each one is a tab next to the item's own, in the same panel (full screen).
  const [commitTabs, setCommitTabs] = useState<string[]>([])
  const [activeCommit, setActiveCommit] = useState<string | null>(null)
  useEffect(() => { setCommitTabs([]); setActiveCommit(null) }, [item.kind, item.key])
  // another item: its own detail, from empty (a re-read of the same item keeps the old one on screen until the new one lands)
  useEffect(() => { setDetail(null); setAllRelated(false) }, [item.kind, item.key, repo])
  const openCommit = (sha: string) => {
    setCommitTabs((tabs) => (tabs.includes(sha) ? tabs : [...tabs, sha]))
    setActiveCommit(sha)
    setExpanded(true)
  }
  // How a merged PR landed: asked of the host once; the server keeps the answer.
  const [mergeMethod, setMergeMethod] = useState<string | null>(null)
  const mergedAt = item.kind === "pull" ? item.mergedAt : null
  useEffect(() => {
    setMergeMethod(null)
    if (!mergedAt) return
    let live = true
    fetchMergeMethod(repo, item.key).then((m) => { if (live) setMergeMethod(m) }, () => {})
    return () => { live = false }
  }, [mergedAt, item.key, repo])
  // A PR or issue named by a commit of this one: it becomes the item of the panel.
  const openLinked = (kind: "issue" | "pull", n: string) => {
    const it = kind === "pull" ? pulls.find((p) => p.key === n) : issues.find((i) => i.key === n)
    if (it) onSelect(it)
  }
  const closeCommit = (sha: string) => {
    setCommitTabs((tabs) => tabs.filter((t) => t !== sha))
    setActiveCommit((active) => (active === sha ? null : active))
  }

  useEffect(() => { if (expandRequest > 0) setExpanded(true) }, [expandRequest])

  useEffect(() => {
    if (!expanded) return
    const onKeyDown = (event: KeyboardEvent) => { if (event.key === "Escape") setExpanded(false) }
    window.addEventListener("keydown", onKeyDown)
    return () => window.removeEventListener("keydown", onKeyDown)
  }, [expanded])

  useEffect(() => {
    let live = true
    setError(null)
    fetchDetail(item.kind, item.key, repo).then((d) => live && setDetail(d), (e) => live && setError(String(e.message ?? e)))
    return () => { live = false }
    // updatedAt: a sync that brought a newer copy of this item (the history after an edit, someone else's comment) reads it again
  }, [item.kind, item.key, repo, reread, itemProp.updatedAt])

  // Opening a record reads it again from its host (once a minute at most): if something moved, the page and this panel refresh.
  useEffect(() => {
    let live = true
    refreshRecord(repo, item.kind, item.key).then((r) => { if (live && r.changed) { setReread((n) => n + 1); onChanged() } }, () => {})
    return () => { live = false }
  }, [item.kind, item.key, repo])

  const related: Item[] =
    item.kind === "issue"
      ? links.filter((l) => l.issue === item.key).map((l) => pulls.find((p) => p.key === l.pull)!).filter(Boolean)
      : links.filter((l) => l.pull === item.key).map((l) => issues.find((i) => i.key === l.issue)!).filter(Boolean)

  const assigned = item.assignees && item.assignees.length > 0 ? (
    <span> · assigned to <strong>{item.assignees.map((a) => `@${a}`).join(", ")}</strong></span>
  ) : (
    <span> · <span className="muted">(unassigned)</span></span>
  )
  const assigneeNames = (item.assignees ?? []).join(", ")
  const assigneesNode = !canEdit ? assigned : t.host === "redmine" ? (
    <ListProp t={t} field="assignee" label="Assignee" session={session} enabled={editable} popover display={assigned} valueText={assigneeNames || "unassigned"} options={opts ? [opt(null, "(none)"), ...opts.members.map((m) => opt(m.id, m.name))] : null} />
  ) : (
    <ListProp t={t} field="assignees" label="Assignees" multi session={session} enabled={editable} popover display={assigned} valueText={assigneeNames || "unassigned"} options={opts ? opts.members.map((m) => opt(m.id, m.name)) : null} />
  )
  const milestoneNode = item.milestone ? (
    <span className="milestone-badge" title={item.milestone.dueOn ? `Due ${day(item.milestone.dueOn)}` : undefined}>
      🏁 Milestone: <strong>{item.milestone.title}</strong>{item.milestone.dueOn ? ` (${day(item.milestone.dueOn)})` : ""}
    </span>
  ) : null
  const milestoneDisplay = milestoneNode ?? <span className="milestone-badge muted">🏁 Milestone: none</span>
  const labelSpans = (item.labels ?? []).map((l) => (
    <span key={l.name} className="label" style={{ borderColor: `#${l.color}` }}>
      {l.name}
    </span>
  ))
  const labelsBlock = canEdit && t.host !== "redmine" ? (
    <div className="labels">
      <ListProp t={t} field="labels" label="Labels" multi session={session} enabled={editable} popover
        valueText={(item.labels ?? []).map((l) => l.name).join(", ") || "none"}
        display={labelSpans.length ? <>{labelSpans}</> : <span className="muted small">+ label</span>}
        options={opts ? (opts.labels ?? []).map((l) => ({ ...opt(l.name, l.name), color: l.color })) : null} />
    </div>
  ) : (item.labels ?? []).length > 0 ? <div className="labels">{labelSpans}</div> : null
  const customNode = (f: CustomField) => {
    const link = isLink(f)
    if (!canEdit) return link ? <a href={f.value as string} target="_blank" rel="noreferrer">{f.value as string}</a> : fieldText(f)
    return (
      <CustomProp t={t} session={session} field={f} enabled={editable} valueText={fieldText(f)}
        display={link ? <span>{f.value as string}</span> : fieldText(f)} />
    )
  }

  const titleNode = (
    <span className="detail-title-slot"><TextProp t={t} field="title" label="Title" required session={session} enabled={editable} valueText={item.title} display={<span className="detail-header-name" title={item.title}>{item.title}</span>} /></span>
  )
  const hero = (
    <div className="detail-hero">
      <p className="muted detail-meta">
        {item.author ? <span>by <strong>{item.author}</strong></span> : <span>unknown author</span>} · created {day(item.createdAt)}
        {item.updatedAt && item.updatedAt !== item.createdAt && <span> · updated {day(item.updatedAt)}</span>}
        {item.kind === "pull" ? ` · merged ${day(item.mergedAt)}` : item.closedAt ? ` · closed ${day(item.closedAt)}` : ""}
        {item.startDate && <span> · starts {day(item.startDate)}</span>}
        {item.dueDate && <span className={isOverdue(item, Date.now()) ? "error" : undefined}> · due {day(item.dueDate)}{isOverdue(item, Date.now()) ? " (overdue)" : ""}</span>}
        {item.kind === "issue" && item.stateReason && (
          <span className="reason-badge inline">{reasonLabel(item.stateReason)}</span>
        )}
        {" "}· {item.commentsCount ?? 0} comments
        {assigneesNode}
      </p>
      {item.customFields && item.customFields.length > 0 && (
        <dl className="custom-fields">
          {item.customFields.map((f) => (
            <div key={f.id} className="custom-field">
              <dt className="muted small">{f.name}</dt>
              <dd>{customNode(f)}</dd>
            </div>
          ))}
        </dl>
      )}
      <div className="hero-badges">
        {canEdit && t.host !== "redmine" ? (
          <ListProp t={t} field="milestone" label="Milestone" session={session} enabled={editable} popover valueText={item.milestone?.title ?? "none"} display={milestoneDisplay} options={opts ? [opt(null, "(none)"), ...opts.milestones?.map((m) => opt(m.id, m.name)) ?? []] : null} />
        ) : milestoneNode}
        {item.kind === "pull" && item.isDraft && <span className="reason-badge draft">Draft</span>}
        {item.kind === "pull" && item.reviewDecision && (
          <span className={`reason-badge review-${item.reviewDecision.toLowerCase()}`}>
            {item.reviewDecision === "APPROVED" ? "✓ Approved" : item.reviewDecision === "CHANGES_REQUESTED" ? "⚠ Changes requested" : item.reviewDecision}
          </span>
        )}
      </div>
      {labelsBlock}
      {item.kind === "pull" && (
        <p className="muted pr-meta">
          <span className="pr-branch">{item.head} → {item.base}</span> · +{item.additions} −{item.deletions} · {item.changedFiles} files · {item.commitsCount} commits · {item.reviewsCount} reviews
          {item.mergedBy && <span> · merged by <strong>@{item.mergedBy}</strong></span>}
          {detail && <span> · {approvers(detail.reviews ?? []).length > 0 ? <>approved by <strong>{approvers(detail.reviews ?? []).map((a) => `@${a}`).join(", ")}</strong></> : "no approvals"}</span>}
          {mergeMethod && <span> · merged as <strong>{mergeMethod}</strong></span>}
        </p>
      )}
    </div>
  )

  return (
    <aside className={`detail${expanded ? " expanded" : ""}`} onClick={(e) => e.stopPropagation()}>
      <header>
        <div className="detail-header-inner">
          <div className="detail-header-title">
            <ListProp t={t} field="state" label="State" session={session} enabled={editable && item.state !== "MERGED"} popover options={stateOptions} valueText={item.state} display={<span className={`pill ${item.state.toLowerCase()}`}>{item.state}</span>} />{" "}
            <a href={item.url} target="_blank" rel="noreferrer" className="detail-num-link">
              {item.kind === "issue" ? "Issue" : "PR"} {refOf(item.key)}
              <svg className="external-icon" viewBox="0 0 16 16" width="12" height="12" fill="none" stroke="currentColor" strokeWidth="1.5" aria-hidden="true">
                <path d="M6 3h7v7M13 3L7 9" />
              </svg>
            </a>
            {(!expanded || activeCommit) && titleNode}
          </div>
          <div className="detail-actions">
            <button className="detail-icon" onClick={() => setExpanded((value) => !value)} aria-label={expanded ? "Exit full screen" : "Expand to full screen"} title={expanded ? "Exit full screen (Esc)" : "Expand to full screen"} aria-pressed={expanded}>
              {expanded ? <svg viewBox="0 0 16 16" aria-hidden="true"><path d="M5.5 2.5v2a1 1 0 0 1-1 1h-2m8-3v2a1 1 0 0 0 1 1h2m-8 8v-2a1 1 0 0 0-1-1h-2m8 3v-2a1 1 0 0 1 1-1h2" /></svg> : <svg viewBox="0 0 16 16" aria-hidden="true"><path d="M2.5 6V3.5a1 1 0 0 1 1-1H6m4 0h2.5a1 1 0 0 1 1 1V6m0 4v2.5a1 1 0 0 1-1 1H10m-4 0H3.5a1 1 0 0 1-1-1V10" /></svg>}
              {expanded && <span className="detail-icon-text">Exit full page <kbd>Esc</kbd></span>}
            </button>
            <button className="detail-icon close" onClick={onClose} aria-label="Close" title="Close">×{expanded && <span className="detail-icon-text">Close</span>}</button>
          </div>
        </div>
      </header>
      {expanded && commitTabs.length > 0 && (
        <nav className="detail-tabs" aria-label="Open tabs">
          <button className={activeCommit === null ? "on" : ""} onClick={() => setActiveCommit(null)}>{item.kind === "issue" ? "Issue" : "PR"} {refOf(item.key)}</button>
          {commitTabs.map((sha) => (
            <span key={sha} className={`detail-tab${activeCommit === sha ? " on" : ""}`}>
              <button onClick={() => setActiveCommit(sha)} title={`Commit ${sha}`}>commit <code>{sha.slice(0, 7)}</code></button>
              <button className="tab-close" onClick={() => closeCommit(sha)} aria-label={`Close commit ${sha.slice(0, 7)}`} title="Close this tab">×</button>
            </span>
          ))}
        </nav>
      )}
      {expanded && activeCommit ? (
        <div className="detail-content commit-tab">
          <CommitDiff repo={repo} sha={activeCommit} onOpenItem={openLinked} />
        </div>
      ) : (
      <div className="detail-content">
        {!expanded && hero}

        {error && <p className="error">{error}</p>}
        {!detail && !error && <p className="muted">Loading…</p>}

        {expanded ? (
          <div className="bento">
            <section className="tile tile-hero">
              <div className="tile-kicker">
                <span className={`pill ${item.state.toLowerCase()}`}>{item.state}</span>
                <span>{item.kind === "issue" ? "Issue" : "Pull request"} {refOf(item.key)}</span>
              </div>
              <h1 className="bento-title">{titleNode}</h1>
              {hero}
            </section>
            <Lifecycle item={item} events={detail?.events ?? []} comments={detail?.comments ?? []} />
            <Numbers item={item} related={related.length} idx={idx} events={detail?.events.length ?? null} />
            <div className="bento-col bento-main">
              {(canEdit || (detail && parseFields(detail.body).rest.trim())) && (
<DescriptionSection t={t} session={session} enabled={editable} hasText={!!detail && parseFields(detail.body).rest.trim() !== ""}>
<RichBody text={detail ? parseFields(detail.body).rest : ""} item={item} issues={issues} pulls={pulls} idx={idx} onSelect={onSelect} />
</DescriptionSection>
)}
              {detail && (detail.comments.length > 0 || canEdit) && (
<section className="detail-section comments-section">
<h3>Comments ({detail.comments.length})</h3>
<div className="comments-list">
{detail.comments.map((c) => (
<CommentRow key={c.id} c={c} when={day(c.createdAt)} session={session} editable={canEdit && item.kind === "issue" && t.host !== "redmine"}>
<RichBody text={c.body} item={item} issues={issues} pulls={pulls} idx={idx} onSelect={onSelect} />
</CommentRow>
))}
</div>
{canEdit && <CommentComposer t={t} session={session} />}
</section>
)}
            </div>
            <div className="bento-col bento-links">
              {item.kind === "issue" && <Family item={item} idx={idx} onSelect={onSelect} />}
                {related.length > 0 && (
                <section className="detail-section related-section">
                  <h3>{item.kind === "issue" ? "Related pull requests" : "Related issues"} ({related.length})</h3>
                  <ul className="related">
                    {(allRelated ? related : related.slice(0, 6)).map((r) => (
                      <li key={`${r.kind}-${r.key}`}>
                        <button className="link-btn" onClick={() => onSelect(r)}>{refOf(r.key)}</button> {r.title} <span className={`pill ${r.state.toLowerCase()}`}>{r.state}</span>
                      </li>
                    ))}
                  </ul>
                  {related.length > 6 && <button className="link-btn" onClick={() => setAllRelated(!allRelated)}>{allRelated ? "Show less" : `Show all ${related.length}`}</button>}
                </section>
              )}
              {item.kind === "pull" && ci?.lastRunAt && (
                <section className="detail-section ci-section">
                  <h3>CI</h3>
                  <CiTrail number={item.key} createdAt={item.createdAt} endAt={item.mergedAt ?? item.closedAt ?? null} endLabel={item.mergedAt ? "merged" : "closed"} commitOids={(item as Pull).commits?.map((c) => c.oid) ?? []} run={ci.byPr[item.key]} changes={ci.changes ?? []} />
                </section>
              )}
              {detail?.reviews && detail.reviews.length > 0 && (
                <section className="detail-section reviews-section">
                  <h3>Reviews</h3>
                  <ul className="events">{detail.reviews.map((r, i) => <li key={i}><span className="muted">{day(r.at)}</span> {r.author}: {r.state}</li>)}</ul>
                </section>
              )}
              {detail?.commits && detail.commits.length > 0 && (
                <section className="detail-section commits-section">
                  <h3>Commits ({detail.commits.length})</h3>
                  <ul className="events">{detail.commits.map((c) => <li key={c.oid}><span className="muted">{day(c.at)}</span> <button className="link-btn" onClick={() => openCommit(c.oid)} title="Open this commit's diff here">{c.message}</button></li>)}</ul>
                </section>
              )}
            </div>
            <div className="bento-col bento-side">
              <EditBlocks t={t} item={item} issues={issues} session={session} canEdit={canEdit} onSelect={onSelect} />
              <Fields item={item} body={detail?.body ?? ""} issues={issues} pulls={pulls} idx={idx} onSelect={onSelect} />
              {detail && detail.events.length > 0 && (
                <section className="detail-section history-section">
                  <h3>History ({detail.events.length})</h3>
                  <ol className="trail">
                    {detail.events.map((e, i) => (
                      <li key={i} className={`trail-${String(e.type).toLowerCase()}`}>
                        <span className="trail-when">{day(e.at)}</span>
                        <span className="trail-what"><b>{e.type}</b>{e.actor ? ` by ${e.actor}` : ""}{typeof e.label === "string" ? ` · ${e.label}` : ""}{typeof e.source === "number" ? ` · #${e.source}` : ""}</span>
                      </li>
                    ))}
                  </ol>
                </section>
              )}
            </div>
          </div>
        ) : (
          <div className="detail-stack">
              {item.kind === "pull" && ci?.lastRunAt && (
                <section className="detail-section ci-section">
                  <h3>CI</h3>
                <CiTrail number={item.key} createdAt={item.createdAt} endAt={item.mergedAt ?? item.closedAt ?? null} endLabel={item.mergedAt ? "merged" : "closed"} commitOids={(item as Pull).commits?.map((c) => c.oid) ?? []} run={ci.byPr[item.key]} changes={ci.changes ?? []} />
              </section>
            )}
            {item.kind === "issue" && <Family item={item} idx={idx} onSelect={onSelect} />}
            {related.length > 0 && (
              <section className="detail-section related-section">
                <h3>{item.kind === "issue" ? "Related pull requests" : "Related issues"} ({related.length})</h3>
                <ul className="related">
                  {(allRelated ? related : related.slice(0, 6)).map((r) => (
                    <li key={`${r.kind}-${r.key}`}>
                      <button className="link-btn" onClick={() => onSelect(r)}>{refOf(r.key)}</button> {r.title} <span className={`pill ${r.state.toLowerCase()}`}>{r.state}</span>
                    </li>
                  ))}
                </ul>
                {related.length > 6 && <button className="link-btn" onClick={() => setAllRelated(!allRelated)}>{allRelated ? "Show less" : `Show all ${related.length}`}</button>}
              </section>
            )}
            {(canEdit || (detail && parseFields(detail.body).rest.trim())) && (
<DescriptionSection t={t} session={session} enabled={editable} hasText={!!detail && parseFields(detail.body).rest.trim() !== ""}>
<RichBody text={detail ? parseFields(detail.body).rest : ""} item={item} issues={issues} pulls={pulls} idx={idx} onSelect={onSelect} />
</DescriptionSection>
)}
            {detail && detail.events.length > 0 && (
              <section className="detail-section history-section">
                <h3>History ({detail.events.length})</h3>
                <ul className="events">
                  {detail.events.map((e, i) => (
                    <li key={i}>
                      <span className="muted">{day(e.at)}</span> {e.type}
                      {e.actor ? ` by ${e.actor}` : ""}
                      {typeof e.label === "string" ? ` · ${e.label}` : ""}
                      {typeof e.source === "number" ? ` · #${e.source}` : ""}
                    </li>
                  ))}
                </ul>
              </section>
            )}
            {detail?.commits && detail.commits.length > 0 && (
              <section className="detail-section commits-section">
                <h3>Commits ({detail.commits.length})</h3>
                <ul className="events">{detail.commits.map((c) => <li key={c.oid}><span className="muted">{day(c.at)}</span> <button className="link-btn" onClick={() => openCommit(c.oid)} title="Open this commit's diff here">{c.message}</button></li>)}</ul>
              </section>
            )}
            <EditBlocks t={t} item={item} issues={issues} session={session} canEdit={canEdit} onSelect={onSelect} />
            <Fields item={item} body={detail?.body ?? ""} issues={issues} pulls={pulls} idx={idx} onSelect={onSelect} />
            {detail?.reviews && detail.reviews.length > 0 && (
              <section className="detail-section reviews-section">
                <h3>Reviews</h3>
                <ul className="events">{detail.reviews.map((r, i) => <li key={i}><span className="muted">{day(r.at)}</span> {r.author}: {r.state}</li>)}</ul>
              </section>
            )}
            {detail && (detail.comments.length > 0 || canEdit) && (
<section className="detail-section comments-section">
<h3>Comments ({detail.comments.length})</h3>
<div className="comments-list">
{detail.comments.map((c) => (
<CommentRow key={c.id} c={c} when={day(c.createdAt)} session={session} editable={canEdit && item.kind === "issue" && t.host !== "redmine"}>
<RichBody text={c.body} item={item} issues={issues} pulls={pulls} idx={idx} onSelect={onSelect} />
</CommentRow>
))}
</div>
{canEdit && <CommentComposer t={t} session={session} />}
</section>
)}
          </div>
        )}
      </div>
      )}
    </aside>
  )
}

/** The fields a save's answer changes on the item the panel shows (the rest is read again from the list). */
const SHOWN_FIELDS = ["title", "state", "stateReason", "labels", "assignees", "milestone", "dueDate", "startDate", "customFields", "isDraft"] as const
function shownFields(out: Saved): Partial<Item> {
  const fields: Record<string, unknown> = {}
  for (const k of SHOWN_FIELDS) if (k in out) fields[k] = out[k]
  return fields as Partial<Item>
}

/** The sections an item gets in edit mode, before its fields: its properties, relations, watchers, files and time. */
function EditBlocks({ t, item, issues, session, canEdit, onSelect }: {
  t: ReturnType<typeof targetOf>; item: Item; issues: Issue[]; session: EditSession; canEdit: boolean; onSelect: (i: Item) => void
}) {
  if (!canEdit) return null
  return (
    <>
      {session.loadError && <p className="error">{session.loadError}</p>}
      <Properties t={t} item={item} session={session} />
      {item.kind === "issue" && <Relations t={t} item={item} issues={issues} session={session} onSelect={onSelect} />}
      {t.host === "redmine" && item.kind === "issue" && <WatchersAndFiles session={session} />}
      {t.host === "redmine" && item.kind === "issue" && <TimeLog session={session} />}
    </>
  )
}

/** The closure reason as a person reads it; the host gives it in either case (the record "NOT_PLANNED", the read-back "not_planned"). */
const DAY_MS = 86_400_000
const spanText = (d: number) => (d < 1 ? `${Math.max(1, Math.round(d * 24))} h` : d < 60 ? `${d < 10 ? d.toFixed(1) : Math.round(d)} d` : `${Math.round(d / 30.4)} mo`)

/** The item's life as one bar: opened, how long it has been (or was) open, and how it ended. */
function Lifecycle({ item, events, comments }: { item: Item; events: { type: string; at: string }[]; comments: { createdAt: string }[] }) {
  const start = Date.parse(item.createdAt)
  const endIso = item.kind === "pull" ? item.mergedAt ?? item.closedAt : item.closedAt
  const end = endIso ? Date.parse(endIso) : Date.now()
  const span = (end - start) / DAY_MS
  const due = item.dueDate ? Date.parse(item.dueDate) : null
  const ending = item.kind === "pull" ? (item.mergedAt ? "merged" : item.closedAt ? "closed" : "open") : item.closedAt ? "closed" : "open"
  // where the due date falls on the bar, when there is one inside a sensible range
  const total = Math.max(end, due ?? 0) - start
  const duePct = due !== null && total > 0 ? Math.min(100, Math.max(0, ((due - start) / total) * 100)) : null
  const endPct = total > 0 ? ((end - start) / total) * 100 : 100
  const at = (iso: string) => (total > 0 ? Math.min(100, Math.max(0, ((Date.parse(iso) - start) / total) * 100)) : 0)
  // what happened along the way, as ticks over the bar: events by kind, comments in their own row
  const marks = [
    ...events.filter((e) => e.at).map((e) => ({ pct: at(e.at), kind: String(e.type).toLowerCase(), text: `${day(e.at)} ${e.type}` })),
    ...comments.map((c) => ({ pct: at(c.createdAt), kind: "comment", text: `${day(c.createdAt)} comment` })),
  ]
  return (
    <section className={`tile tile-life life-${ending}`}>
      <h3>{ending === "open" ? "Open for" : ending === "merged" ? "Merged after" : "Closed after"}</h3>
      <div className="life-big">{spanText(span)}</div>
      {marks.length > 0 && (
        <div className="life-marks" aria-label={`${marks.length} events along the way`}>
          {marks.map((m, i) => <span key={i} className={`life-mark mark-${m.kind}`} style={{ left: `${m.pct}%` }} title={m.text} />)}
        </div>
      )}
      <div className="life-bar" aria-hidden>
        <div className="life-fill" style={{ width: `${endPct}%` }} />
        {duePct !== null && <div className="life-due" style={{ left: `${duePct}%` }} title={`due ${day(item.dueDate!)}`} />}
      </div>
      <div className="life-ends">
        <span><span className="muted">opened</span> {day(item.createdAt)}</span>
        <span>{ending === "open" ? <span className="muted">still open</span> : <><span className="muted">{ending}</span> {day(endIso)}</>}</span>
      </div>
      {due !== null && <p className={`small ${isOverdue(item, Date.now()) ? "error" : "muted"}`}>due {day(item.dueDate!)}{isOverdue(item, Date.now()) ? " · overdue" : ""}</p>}
    </section>
  )
}

/** A few counts at a glance, each in its own cell: what the item is made of and what hangs from it. */
function Numbers({ item, related, idx, events }: { item: Item; related: number; idx: LinkIndex; events: number | null }) {
  const cells: [string, string | number, string?][] = item.kind === "pull"
    ? [["Lines", `+${item.additions} −${item.deletions}`, "add"], ["Files", item.changedFiles], ["Commits", item.commitsCount], ["Reviews", item.reviewsCount, item.reviewsCount ? undefined : "warn"], ["Issues", related], ["Comments", item.commentsCount ?? 0]]
    : [
      ["Comments", item.commentsCount ?? 0], ["Pull requests", related],
      ["Children", new Set((idx.childrenOf.get(item.key) ?? []).map((c) => c.key)).size],
      ["Blocked by", (idx.blockersOf.get(item.key) ?? []).filter((b) => b.state === "OPEN").length, (idx.blockersOf.get(item.key) ?? []).some((b) => b.state === "OPEN") ? "error" : undefined],
      ["Blocks", (idx.blockedOf.get(item.key) ?? []).filter((b) => b.state === "OPEN").length],
      ["Events", events ?? "…"],
    ]
  return (
    <section className="tile tile-numbers">
      {cells.map(([label, value, tone]) => (
        <div key={label} className="num-cell">
          <div className={`num-value${tone ? ` ${tone}` : ""}`}>{value}</div>
          <div className="num-label">{label}</div>
        </div>
      ))}
    </section>
  )
}

export function reasonLabel(reason: string | null | undefined): string {
  const r = (reason ?? "").toLowerCase()
  return r === "completed" ? "✓ Completed" : r === "not_planned" ? "✕ Not planned" : reason ?? ""
}

const state = (i: Issue) => <span className={`pill ${i.state.toLowerCase()}`}>{i.state}</span>

/** Children (a checklist with the real state of each), parents and dependencies of an issue, every one a click away. */
function Family({ item, idx, onSelect }: { item: Issue; idx: LinkIndex; onSelect: (i: Item) => void }) {
  const kids = idx.childrenOf.get(item.key) ?? []
  const parents = idx.parentsOf.get(item.key) ?? []
  const blockers = idx.blockersOf.get(item.key) ?? []
  const blocks = idx.blockedOf.get(item.key) ?? []
  if (!kids.length && !parents.length && !blockers.length && !blocks.length) return null
  const closed = kids.filter((k) => k.state === "CLOSED").length
  const li = (i: Issue, extra?: string) => (
    <li key={i.key} className={`${i.state === "CLOSED" ? "closed-row" : ""}${extra ? " current-item" : ""}`}>
      <span className="box">{i.state === "CLOSED" ? "☑" : "☐"}</span> <button className="link-btn" onClick={() => onSelect(i)}>{refOf(i.key)}</button> <span className="li-title">{i.title}</span> {state(i)}
      {extra && <strong className="muted small"> {extra}</strong>}
    </li>
  )
  return (
    <>
      {kids.length > 0 && (
        <section className="detail-section family-section">
          <h3>Children · {closed}/{kids.length} closed ({Math.round((closed / kids.length) * 100)} %)</h3>
          <div className="progress"><div style={{ width: `${(closed / kids.length) * 100}%` }} /></div>
          <ul className="related checklist">{[...kids].sort((a, b) => Number(a.state === "CLOSED") - Number(b.state === "CLOSED") || byKey(a.key, b.key)).map((k) => li(k))}</ul>
        </section>
      )}
      {kids.length === 0 && parents.length > 0 && (
        <section className="detail-section family-section">
          <h3>
            Part of epic:{" "}
            {parents.map((p) => (
              <span key={p.key}>
                <button className="link-btn" onClick={() => onSelect(p)}>{refOf(p.key)}</button> {p.title} {state(p)}
              </span>
            ))}
          </h3>
          {parents.map((p) => {
            const pKids = idx.childrenOf.get(p.key) ?? []
            if (pKids.length <= 1) return null
            const pClosed = pKids.filter((k) => k.state === "CLOSED").length
            return (
              <div key={p.key} className="parent-context">
                <div className="muted small" style={{ marginBottom: 4 }}>
                  Epic progress: {pClosed}/{pKids.length} closed ({Math.round((pClosed / pKids.length) * 100)} %)
                </div>
                <div className="progress" style={{ marginBottom: 8 }}>
                  <div style={{ width: `${(pClosed / pKids.length) * 100}%` }} />
                </div>
                <ul className="related checklist">
                  {[...pKids]
                    .sort((a, b) => Number(a.state === "CLOSED") - Number(b.state === "CLOSED") || byKey(a.key, b.key))
                    .map((k) => li(k, k.key === item.key ? "(este issue)" : undefined))}
                </ul>
              </div>
            )
          })}
        </section>
      )}
    </>
  )
}

type Rel = "blocker" | "blocked" | "child" | "parent" | "closes" | "touches" | ""

const relLabel: Record<Exclude<Rel, "">, string> = {
  blocker: "resolved blocker", blocked: "blocked by this", child: "child", parent: "parent", closes: "closes", touches: "linked PR",
}

/** Chips for what a description points at (#N): the real state, how it is tied to `item`, and a click to open it. */
function makeRefs({ item, issues, pulls, idx, onSelect }: { item: Item; issues: Issue[]; pulls: Pull[]; idx: LinkIndex; onSelect: (i: Item) => void }) {
  const byNumber = new Map<string, Item>([...issues, ...pulls].map((i) => [i.key, i]))
  const rel = (other: Item): Rel => {
    const n = other.key
    if (item.kind === "issue" && other.kind === "issue") {
      if ((idx.blockersOf.get(item.key) ?? []).some((i) => i.key === n)) return "blocker"
      if ((idx.blockedOf.get(item.key) ?? []).some((i) => i.key === n)) return "blocked"
      if ((idx.childrenOf.get(item.key) ?? []).some((i) => i.key === n)) return "child"
      if ((idx.parentsOf.get(item.key) ?? []).some((i) => i.key === n)) return "parent"
    }
    const linked = item.kind === "issue" ? idx.pullsOf.get(item.key) : idx.issuesOf.get(item.key)
    if (other.kind !== item.kind && linked?.some((i) => i.key === n)) return "touches"
    return ""
  }
  const chip = (other: Item, key: string | number) => {
    const r = rel(other)
    const open = other.state === "OPEN"
    const blocking = r === "blocker" && open
    const glyph = other.state === "MERGED" ? "⎇" : open ? "○" : "✓"
    return (
      <button
        key={key}
        className={`chip ${other.kind} ${other.state.toLowerCase()} ${r ? `rel-${r}` : ""} ${blocking ? "blocking" : ""}`}
        title={`${other.kind === "issue" ? "Issue" : "PR"} ${refOf(other.key)} · ${other.state}${r ? ` · ${relLabel[r]}` : ""} · ${other.title}`}
        onClick={(e) => { e.stopPropagation(); onSelect(other) }}
      >
        <span className="chip-glyph">{glyph}</span>{refOf(other.key)}
        {r && <span className="chip-rel">{blocking ? "blocks" : relLabel[r]}</span>}
      </button>
    )
  }
  /** Text with its #N references as chips (the ones that exist in this repo). */
  const text = (t: string): React.ReactNode[] => {
    const parts: React.ReactNode[] = []
    let last = 0
    for (const m of t.matchAll(/#(\d+)\b/g)) {
      const other = byNumber.get(m[1])
      if (!other || other.key === item.key) continue
      parts.push(t.slice(last, m.index))
      last = m.index! + m[0].length
      parts.push(chip(other, m.index!))
    }
    parts.push(t.slice(last))
    return parts
  }
  return { chip, text }
}

/** A description with its #N references as chips, so an epic's checklist shows which boxes are really done. */
function RichBody({ text, item, issues, pulls, idx, onSelect }: { text: string; item: Item; issues: Issue[]; pulls: Pull[]; idx: LinkIndex; onSelect: (i: Item) => void }) {
  return <pre className="body">{makeRefs({ item, issues, pulls, idx, onSelect }).text(text)}</pre>
}

/**
 * The item's fields: what the repo knows of how it is tied to the rest (parent, blockers, what it blocks, the pull
 * requests that close it) and the "Key: value" lines of its description (Depende de, Diseño, Refs...), each reference
 * a chip with its real state. A blocker that is still open is marked; a resolved one says so.
 */
function Fields({ item, body, issues, pulls, idx, onSelect }: { item: Item; body: string; issues: Issue[]; pulls: Pull[]; idx: LinkIndex; onSelect: (i: Item) => void }) {
  const refs = makeRefs({ item, issues, pulls, idx, onSelect })
  const rows: { key: string; cls?: string; value: React.ReactNode }[] = []
  const chips = (list: Item[]) => list.map((i) => <span key={`${i.kind}-${i.key}`}>{refs.chip(i, i.key)} </span>)

  if (item.kind === "issue") {
    const parents = idx.parentsOf.get(item.key) ?? []
    const blockers = idx.blockersOf.get(item.key) ?? []
    const blocks = idx.blockedOf.get(item.key) ?? []
    if (parents.length) rows.push({ key: "Parent", value: chips(parents) })
    if (item.kind === "issue" && item.parentEpic) {
      rows.push({
        key: item.parentEpic.type ?? "Epic",
        value: <a href={item.parentEpic.url} target="_blank" rel="noreferrer">#{item.parentEpic.iid} {item.parentEpic.title} ↗</a>,
      })
    }
    if (blockers.length) {
      const open = blockers.filter((b) => b.state === "OPEN")
      rows.push({
        key: "Blocked by",
        cls: open.length ? "blocked" : "",
        value: <>{chips([...blockers].sort((a, b) => Number(a.state === "CLOSED") - Number(b.state === "CLOSED")))}<span className="muted small">{open.length ? `${open.length} open` : "all resolved"}</span></>,
      })
    }
    if (blocks.length) rows.push({ key: "Blocks", value: chips(blocks) })
  }
  // "Depends on #106, #107" says what "Blocked by" already shows as chips: when it is only blockers (and connective words),
  // drop it; one with a note of its own ("#97 (runner de plugins)") stays, the note is information
  const blockerNumbers = new Set((item.kind === "issue" ? idx.blockersOf.get(item.key) ?? [] : []).map((b) => b.key))
  const duplicatesBlockers = (value: string) => {
    const refs = [...value.matchAll(/#(\d+)\b/g)].map((m) => m[1])
    return refs.length > 0 && refs.every((n) => blockerNumbers.has(n)) && value.replace(/#\d+/g, "").replace(/\b(?:y|and|et|und|e)\b|[\s,;.&]/gi, "") === ""
  }
  for (const f of parseFields(body).fields) {
    if (isDependencyKey(f.key) && duplicatesBlockers(f.value)) continue
    rows.push({ key: f.key, value: <Clamp long={f.value.length > 180}><span className="field-text">{refs.text(f.value)}</span></Clamp> })
  }
  if (rows.length === 0) return null
  return (
    <section className="detail-section fields">
      <h3>Fields</h3>
      <dl>
        {rows.map((r, i) => (
          <div key={i} className={`field ${r.cls ?? ""}`}>
            <dt>{r.key}</dt>
            <dd>{r.value}</dd>
          </div>
        ))}
      </dl>
    </section>
  )
}

/** A long field value shows three lines and opens on a click: a field is for scanning, the full text is one click away. */
function Clamp({ long, children }: { long: boolean; children: React.ReactNode }) {
  const [open, setOpen] = useState(false)
  if (!long) return <>{children}</>
  return (
    <>
      <div className={open ? "" : "clamp"}>{children}</div>
      <button className="link-btn small" onClick={() => setOpen(!open)}>{open ? "show less" : "show more"}</button>
    </>
  )
}
