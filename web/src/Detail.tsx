import { useEffect, useState } from "react"
import { CommitDiff } from "./Commits"
import { fetchDetail, fetchMergeMethod } from "./api"
import type { Detail as DetailT, Issue, Item, Link, Pull } from "./types"
import type { LinkIndex } from "./filters"
import { isDependencyKey, parseFields } from "./fields"
import { approvers } from "./reviews"
import { CiTrail, type CiSummary } from "./CiBadge"

interface Props {
  item: Item
  repo: string
  issues: Issue[]
  pulls: Pull[]
  links: Link[]
  idx: LinkIndex
  onSelect: (item: Item) => void
  onClose: () => void
  ci?: CiSummary // CI runs of the repo: each PR's last state and link
  expandRequest?: number // each increase opens the panel full screen (the timeline's "Open full screen")
}

const day = (s: string | null | undefined) => (s ? s.slice(0, 10) : "—")

export function DetailPanel({ item, repo, issues, pulls, links, idx, onSelect, onClose, expandRequest = 0, ci }: Props) {
  const [detail, setDetail] = useState<DetailT | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [allRelated, setAllRelated] = useState(false)
  const [expanded, setExpanded] = useState(false)
  // Commits opened from this item: each one is a tab next to the item's own, in the same panel (full screen).
  const [commitTabs, setCommitTabs] = useState<string[]>([])
  const [activeCommit, setActiveCommit] = useState<string | null>(null)
  useEffect(() => { setCommitTabs([]); setActiveCommit(null) }, [item.kind, item.number])
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
    fetchMergeMethod(repo, item.number).then((m) => { if (live) setMergeMethod(m) }, () => {})
    return () => { live = false }
  }, [mergedAt, item.number, repo])
  // A PR or issue named by a commit of this one: it becomes the item of the panel.
  const openLinked = (kind: "issue" | "pull", n: number) => {
    const it = kind === "pull" ? pulls.find((p) => p.number === n) : issues.find((i) => i.number === n)
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
    setDetail(null)
    setError(null)
    setAllRelated(false)
    fetchDetail(item.kind, item.number, repo).then((d) => live && setDetail(d), (e) => live && setError(String(e.message ?? e)))
    return () => { live = false }
  }, [item.kind, item.number, repo])

  const related: Item[] =
    item.kind === "issue"
      ? links.filter((l) => l.issue === item.number).map((l) => pulls.find((p) => p.number === l.pull)!).filter(Boolean)
      : links.filter((l) => l.pull === item.number).map((l) => issues.find((i) => i.number === l.issue)!).filter(Boolean)

  return (
    <aside className={`detail${expanded ? " expanded" : ""}`} onClick={(e) => e.stopPropagation()}>
      <header>
        <div className="detail-header-inner">
          <div className="detail-header-title">
            <span className={`pill ${item.state.toLowerCase()}`}>{item.state}</span>{" "}
            <a href={item.url} target="_blank" rel="noreferrer" className="detail-num-link">
              {item.kind === "issue" ? "Issue" : "PR"} #{item.number}
              <svg className="external-icon" viewBox="0 0 16 16" width="12" height="12" fill="none" stroke="currentColor" strokeWidth="1.5" aria-hidden="true">
                <path d="M6 3h7v7M13 3L7 9" />
              </svg>
            </a>
            <span className="detail-header-name" title={item.title}>{item.title}</span>
          </div>
          <div className="detail-actions">
            <button className="detail-icon" onClick={() => setExpanded((value) => !value)} aria-label={expanded ? "Exit full screen" : "Expand to full screen"} title={expanded ? "Exit full screen (Esc)" : "Expand to full screen"} aria-pressed={expanded}>
              {expanded ? <svg viewBox="0 0 16 16" aria-hidden="true"><path d="M5.5 2.5v2a1 1 0 0 1-1 1h-2m8-3v2a1 1 0 0 0 1 1h2m-8 8v-2a1 1 0 0 0-1-1h-2m8 3v-2a1 1 0 0 1 1-1h2" /></svg> : <svg viewBox="0 0 16 16" aria-hidden="true"><path d="M2.5 6V3.5a1 1 0 0 1 1-1H6m4 0h2.5a1 1 0 0 1 1 1V6m0 4v2.5a1 1 0 0 1-1 1H10m-4 0H3.5a1 1 0 0 1-1-1V10" /></svg>}
            </button>
            <button className="detail-icon close" onClick={onClose} aria-label="Close" title="Close">×</button>
          </div>
        </div>
      </header>
      {expanded && commitTabs.length > 0 && (
        <nav className="detail-tabs" aria-label="Open tabs">
          <button className={activeCommit === null ? "on" : ""} onClick={() => setActiveCommit(null)}>{item.kind === "issue" ? "Issue" : "PR"} #{item.number}</button>
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
        <div className="detail-hero">
          <p className="muted detail-meta">
            {item.author ? <span>by <strong>{item.author}</strong></span> : <span>unknown author</span>} · created {day(item.createdAt)}
            {item.updatedAt && item.updatedAt !== item.createdAt && <span> · updated {day(item.updatedAt)}</span>}
            {item.kind === "pull" ? ` · merged ${day(item.mergedAt)}` : item.closedAt ? ` · closed ${day(item.closedAt)}` : ""}
            {item.kind === "issue" && item.stateReason && (
              <span className="reason-badge inline">{item.stateReason === "COMPLETED" ? "✓ Completed" : item.stateReason === "NOT_PLANNED" ? "✕ Not planned" : item.stateReason}</span>
            )}
            {" "}· {item.commentsCount ?? 0} comments
            {item.assignees && item.assignees.length > 0 ? (
              <span> · assigned to <strong>{item.assignees.map((a) => `@${a}`).join(", ")}</strong></span>
            ) : (
              <span> · <span className="muted">(unassigned)</span></span>
            )}
          </p>
          <div className="hero-badges">
            {item.milestone && (
              <span className="milestone-badge" title={item.milestone.dueOn ? `Due ${day(item.milestone.dueOn)}` : undefined}>
                🏁 Milestone: <strong>{item.milestone.title}</strong>{item.milestone.dueOn ? ` (${day(item.milestone.dueOn)})` : ""}
              </span>
            )}
            {item.kind === "pull" && item.isDraft && <span className="reason-badge draft">Draft</span>}
            {item.kind === "pull" && item.reviewDecision && (
              <span className={`reason-badge review-${item.reviewDecision.toLowerCase()}`}>
                {item.reviewDecision === "APPROVED" ? "✓ Approved" : item.reviewDecision === "CHANGES_REQUESTED" ? "⚠ Changes requested" : item.reviewDecision}
              </span>
            )}
          </div>
          {item.labels && item.labels.length > 0 && (
            <div className="labels">
              {item.labels.map((l) => (
                <span key={l.name} className="label" style={{ borderColor: `#${l.color}` }}>
                  {l.name}
                </span>
              ))}
            </div>
          )}
          {item.kind === "pull" && (
            <p className="muted pr-meta">
              <span className="pr-branch">{item.head} → {item.base}</span> · +{item.additions} −{item.deletions} · {item.changedFiles} files · {item.commitsCount} commits · {item.reviewsCount} reviews
              {item.mergedBy && <span> · merged by <strong>@{item.mergedBy}</strong></span>}
              {detail && <span> · {approvers(detail.reviews ?? []).length > 0 ? <>approved by <strong>{approvers(detail.reviews ?? []).map((a) => `@${a}`).join(", ")}</strong></> : "no approvals"}</span>}
              {mergeMethod && <span> · merged as <strong>{mergeMethod}</strong></span>}
            </p>
          )}
        </div>

        {error && <p className="error">{error}</p>}
        {!detail && !error && <p className="muted">Loading…</p>}

        {expanded ? (
          <div className="detail-grid">
            <div className="detail-main">
              {item.kind === "issue" && <Family item={item} idx={idx} onSelect={onSelect} />}
              {detail && parseFields(detail.body).rest.trim() && (
                <section className="detail-section desc-section">
                  <h3>Description</h3>
                  <RichBody text={parseFields(detail.body).rest} item={item} issues={issues} pulls={pulls} idx={idx} onSelect={onSelect} />
                </section>
              )}
              {detail && detail.comments.length > 0 && (
                <section className="detail-section comments-section">
                  <h3>Comments ({detail.comments.length})</h3>
                  <div className="comments-list">
                    {detail.comments.map((c) => (
                      <div key={c.id} className="comment">
                        <div className="comment-header">
                          <strong>{c.author}</strong> <span className="muted">· {day(c.createdAt)}</span>
                        </div>
                        <div className="comment-body">
                          <RichBody text={c.body} item={item} issues={issues} pulls={pulls} idx={idx} onSelect={onSelect} />
                        </div>
                      </div>
                    ))}
                  </div>
                </section>
              )}
            </div>
            <aside className="detail-sidebar">
                {related.length > 0 && (
                <section className="detail-section related-section">
                  <h3>{item.kind === "issue" ? "Related pull requests" : "Related issues"} ({related.length})</h3>
                  <ul className="related">
                    {(allRelated ? related : related.slice(0, 6)).map((r) => (
                      <li key={`${r.kind}-${r.number}`}>
                        <button className="link-btn" onClick={() => onSelect(r)}>#{r.number}</button> {r.title} <span className={`pill ${r.state.toLowerCase()}`}>{r.state}</span>
                      </li>
                    ))}
                  </ul>
                  {related.length > 6 && <button className="link-btn" onClick={() => setAllRelated(!allRelated)}>{allRelated ? "Show less" : `Show all ${related.length}`}</button>}
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
              {item.kind === "pull" && ci?.lastRunAt && (
                <section className="detail-section ci-section">
                  <h3>CI</h3>
                  <CiTrail number={item.number} createdAt={item.createdAt} endAt={item.mergedAt ?? item.closedAt ?? null} endLabel={item.mergedAt ? "merged" : "closed"} commitOids={(item as Pull).commits?.map((c) => c.oid) ?? []} run={ci.byPr[item.number]} changes={ci.changes ?? []} />
                </section>
              )}
              <Fields item={item} body={detail?.body ?? ""} issues={issues} pulls={pulls} idx={idx} onSelect={onSelect} />
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
            </aside>
          </div>
        ) : (
          <div className="detail-stack">
              {item.kind === "pull" && ci?.lastRunAt && (
                <section className="detail-section ci-section">
                  <h3>CI</h3>
                <CiTrail number={item.number} createdAt={item.createdAt} endAt={item.mergedAt ?? item.closedAt ?? null} endLabel={item.mergedAt ? "merged" : "closed"} commitOids={(item as Pull).commits?.map((c) => c.oid) ?? []} run={ci.byPr[item.number]} changes={ci.changes ?? []} />
              </section>
            )}
            {item.kind === "issue" && <Family item={item} idx={idx} onSelect={onSelect} />}
            {related.length > 0 && (
              <section className="detail-section related-section">
                <h3>{item.kind === "issue" ? "Related pull requests" : "Related issues"} ({related.length})</h3>
                <ul className="related">
                  {(allRelated ? related : related.slice(0, 6)).map((r) => (
                    <li key={`${r.kind}-${r.number}`}>
                      <button className="link-btn" onClick={() => onSelect(r)}>#{r.number}</button> {r.title} <span className={`pill ${r.state.toLowerCase()}`}>{r.state}</span>
                    </li>
                  ))}
                </ul>
                {related.length > 6 && <button className="link-btn" onClick={() => setAllRelated(!allRelated)}>{allRelated ? "Show less" : `Show all ${related.length}`}</button>}
              </section>
            )}
            {detail && parseFields(detail.body).rest.trim() && (
              <section className="detail-section desc-section">
                <h3>Description</h3>
                <RichBody text={parseFields(detail.body).rest} item={item} issues={issues} pulls={pulls} idx={idx} onSelect={onSelect} />
              </section>
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
            <Fields item={item} body={detail?.body ?? ""} issues={issues} pulls={pulls} idx={idx} onSelect={onSelect} />
            {detail?.reviews && detail.reviews.length > 0 && (
              <section className="detail-section reviews-section">
                <h3>Reviews</h3>
                <ul className="events">{detail.reviews.map((r, i) => <li key={i}><span className="muted">{day(r.at)}</span> {r.author}: {r.state}</li>)}</ul>
              </section>
            )}
            {detail && detail.comments.length > 0 && (
              <section className="detail-section comments-section">
                <h3>Comments ({detail.comments.length})</h3>
                <div className="comments-list">
                  {detail.comments.map((c) => (
                    <div key={c.id} className="comment">
                      <div className="comment-header">
                        <strong>{c.author}</strong> <span className="muted">· {day(c.createdAt)}</span>
                      </div>
                      <div className="comment-body">
                        <RichBody text={c.body} item={item} issues={issues} pulls={pulls} idx={idx} onSelect={onSelect} />
                      </div>
                    </div>
                  ))}
                </div>
              </section>
            )}
          </div>
        )}
      </div>
      )}
    </aside>
  )
}

const state = (i: Issue) => <span className={`pill ${i.state.toLowerCase()}`}>{i.state}</span>

/** Children (a checklist with the real state of each), parents and dependencies of an issue, every one a click away. */
function Family({ item, idx, onSelect }: { item: Issue; idx: LinkIndex; onSelect: (i: Item) => void }) {
  const kids = idx.childrenOf.get(item.number) ?? []
  const parents = idx.parentsOf.get(item.number) ?? []
  const blockers = idx.blockersOf.get(item.number) ?? []
  const blocks = idx.blockedOf.get(item.number) ?? []
  if (!kids.length && !parents.length && !blockers.length && !blocks.length) return null
  const closed = kids.filter((k) => k.state === "CLOSED").length
  const li = (i: Issue, extra?: string) => (
    <li key={i.number} className={`${i.state === "CLOSED" ? "closed-row" : ""}${extra ? " current-item" : ""}`}>
      <span className="box">{i.state === "CLOSED" ? "☑" : "☐"}</span> <button className="link-btn" onClick={() => onSelect(i)}>#{i.number}</button> <span className="li-title">{i.title}</span> {state(i)}
      {extra && <strong className="muted small"> {extra}</strong>}
    </li>
  )
  return (
    <>
      {kids.length > 0 && (
        <section className="detail-section family-section">
          <h3>Children · {closed}/{kids.length} closed ({Math.round((closed / kids.length) * 100)} %)</h3>
          <div className="progress"><div style={{ width: `${(closed / kids.length) * 100}%` }} /></div>
          <ul className="related checklist">{[...kids].sort((a, b) => Number(a.state === "CLOSED") - Number(b.state === "CLOSED") || a.number - b.number).map((k) => li(k))}</ul>
        </section>
      )}
      {kids.length === 0 && parents.length > 0 && (
        <section className="detail-section family-section">
          <h3>
            Part of epic:{" "}
            {parents.map((p) => (
              <span key={p.number}>
                <button className="link-btn" onClick={() => onSelect(p)}>#{p.number}</button> {p.title} {state(p)}
              </span>
            ))}
          </h3>
          {parents.map((p) => {
            const pKids = idx.childrenOf.get(p.number) ?? []
            if (pKids.length <= 1) return null
            const pClosed = pKids.filter((k) => k.state === "CLOSED").length
            return (
              <div key={p.number} className="parent-context">
                <div className="muted small" style={{ marginBottom: 4 }}>
                  Epic progress: {pClosed}/{pKids.length} closed ({Math.round((pClosed / pKids.length) * 100)} %)
                </div>
                <div className="progress" style={{ marginBottom: 8 }}>
                  <div style={{ width: `${(pClosed / pKids.length) * 100}%` }} />
                </div>
                <ul className="related checklist">
                  {[...pKids]
                    .sort((a, b) => Number(a.state === "CLOSED") - Number(b.state === "CLOSED") || a.number - b.number)
                    .map((k) => li(k, k.number === item.number ? "(este issue)" : undefined))}
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
  const byNumber = new Map<number, Item>([...issues, ...pulls].map((i) => [i.number, i]))
  const rel = (other: Item): Rel => {
    const n = other.number
    if (item.kind === "issue" && other.kind === "issue") {
      if ((idx.blockersOf.get(item.number) ?? []).some((i) => i.number === n)) return "blocker"
      if ((idx.blockedOf.get(item.number) ?? []).some((i) => i.number === n)) return "blocked"
      if ((idx.childrenOf.get(item.number) ?? []).some((i) => i.number === n)) return "child"
      if ((idx.parentsOf.get(item.number) ?? []).some((i) => i.number === n)) return "parent"
    }
    const linked = item.kind === "issue" ? idx.pullsOf.get(item.number) : idx.issuesOf.get(item.number)
    if (other.kind !== item.kind && linked?.some((i) => i.number === n)) return "touches"
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
        title={`${other.kind === "issue" ? "Issue" : "PR"} #${other.number} · ${other.state}${r ? ` · ${relLabel[r]}` : ""} · ${other.title}`}
        onClick={(e) => { e.stopPropagation(); onSelect(other) }}
      >
        <span className="chip-glyph">{glyph}</span>#{other.number}
        {r && <span className="chip-rel">{blocking ? "blocks" : relLabel[r]}</span>}
      </button>
    )
  }
  /** Text with its #N references as chips (the ones that exist in this repo). */
  const text = (t: string): React.ReactNode[] => {
    const parts: React.ReactNode[] = []
    let last = 0
    for (const m of t.matchAll(/#(\d+)\b/g)) {
      const other = byNumber.get(Number(m[1]))
      if (!other || other.number === item.number) continue
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
  const chips = (list: Item[]) => list.map((i) => <span key={`${i.kind}-${i.number}`}>{refs.chip(i, i.number)} </span>)

  // Asignados
  rows.push({
    key: "Assignees",
    value: item.assignees && item.assignees.length > 0 ? (
      <span>{item.assignees.map((a) => `@${a}`).join(", ")}</span>
    ) : (
      <span className="muted">(unassigned)</span>
    ),
  })

  // Milestone
  if (item.milestone) {
    rows.push({
      key: "Milestone",
      value: (
        <span>
          {item.milestone.title}
          {item.milestone.dueOn && <span className="muted small"> (due {day(item.milestone.dueOn)})</span>}
        </span>
      ),
    })
  }

  if (item.kind === "issue") {
    const parents = idx.parentsOf.get(item.number) ?? []
    const kids = idx.childrenOf.get(item.number) ?? []
    const blockers = idx.blockersOf.get(item.number) ?? []
    const blocks = idx.blockedOf.get(item.number) ?? []
    const closers = (idx.pullsOf.get(item.number) ?? []).length
    if (parents.length) rows.push({ key: "Parent", value: chips(parents) })
    if (item.kind === "issue" && item.parentEpic) {
      rows.push({
        key: item.parentEpic.type ?? "Epic",
        value: <a href={item.parentEpic.url} target="_blank" rel="noreferrer">#{item.parentEpic.iid} {item.parentEpic.title} ↗</a>,
      })
    }
    if (kids.length) {
      const closed = kids.filter((k) => k.state === "CLOSED").length
      rows.push({
        key: "Children",
        value: (
          <>
            {chips(kids)}
            <span className="muted small">· {closed}/{kids.length} closed</span>
          </>
        ),
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
    if (closers) rows.push({ key: "PRs", value: <span className="muted">{closers} linked</span> })
    if (item.stateReason) {
      rows.push({
        key: "Closure",
        value: <span>{item.stateReason === "COMPLETED" ? "Completed" : item.stateReason === "NOT_PLANNED" ? "Not planned" : item.stateReason}</span>,
      })
    }
  } else {
    const closes = idx.issuesOf.get(item.number) ?? []
    if (closes.length) rows.push({ key: "Closes issues", value: chips(closes) })
    if (item.mergedBy) rows.push({ key: "Merged by", value: <span>@{item.mergedBy}</span> })
    if (item.reviewDecision) {
      rows.push({
        key: "Review",
        value: <span>{item.reviewDecision === "APPROVED" ? "Approved" : item.reviewDecision === "CHANGES_REQUESTED" ? "Changes requested" : item.reviewDecision}</span>,
      })
    }
  }
  // "Depends on #106, #107" says what "Blocked by" already shows as chips: when it is only blockers (and connective words),
  // drop it; one with a note of its own ("#97 (runner de plugins)") stays, the note is information
  const blockerNumbers = new Set((item.kind === "issue" ? idx.blockersOf.get(item.number) ?? [] : []).map((b) => b.number))
  const duplicatesBlockers = (value: string) => {
    const refs = [...value.matchAll(/#(\d+)\b/g)].map((m) => Number(m[1]))
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
