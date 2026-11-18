import { useEffect, useMemo, useState } from "react"
import { fetchCommit, fetchCommits, type CommitDetail, type CommitFile, type CommitRow } from "./api"
import { splitPatch } from "./diff"

const PAGE = 100
const when = (iso: string | null) => (iso ? iso.slice(0, 16).replace("T", " ") : "—")
const pathOf = (f: CommitFile) => (f.oldPath && f.oldPath !== f.path ? `${f.oldPath} → ${f.path}` : f.path)

export type OpenItem = (kind: "issue" | "pull", number: number) => void

/** The PR and the issues a commit belongs to; a click opens that item in the detail panel when a handler is given. */
export function LinkChips({ pr, issues, onOpenItem }: { pr?: number | null; issues?: number[]; onOpenItem?: OpenItem }) {
  if (!pr && !(issues && issues.length)) return null
  const chip = (kind: "issue" | "pull", n: number, label: string) =>
    onOpenItem
      ? <button key={`${kind}-${n}`} className="chip" onClick={(e) => { e.stopPropagation(); onOpenItem(kind, n) }} title={`Open ${kind === "pull" ? "PR" : "issue"} #${n}`}>{label}</button>
      : <span key={`${kind}-${n}`} className="chip">{label}</span>
  return (
    <span className="link-chips">
      {pr ? chip("pull", pr, `PR #${pr}`) : null}
      {(issues ?? []).map((n) => chip("issue", n, `#${n}`))}
    </span>
  )
}

/** The commits of the default branch (and a PR's own), searchable and filterable by PR or issue: the list on the left, the chosen commit's diff side by side on the right. */
export function Commits({ repo, onOpenItem }: { repo: string; onOpenItem?: OpenItem }) {
  const [q, setQ] = useState("")
  const [author, setAuthor] = useState("")
  const [pr, setPr] = useState("")
  const [issue, setIssue] = useState("")
  const [items, setItems] = useState<CommitRow[]>([])
  const [total, setTotal] = useState(0)
  const [synced, setSynced] = useState(0)
  const [lastSync, setLastSync] = useState<string | null>(null)
  const [listError, setListError] = useState<string | null>(null)
  const [sha, setSha] = useState<string | null>(null)

  // A new search starts again from the first page; typing waits a moment so each keystroke is not a request.
  useEffect(() => {
    let live = true
    const timer = setTimeout(() => {
      fetchCommits(repo, { q, author, pr, issue, offset: 0, limit: PAGE }).then(
        (p) => { if (!live) return; setItems(p.items); setTotal(p.total); setSynced(p.synced); setLastSync(p.lastSync); setListError(null) },
        (e) => { if (live) setListError(String(e.message ?? e)) },
      )
    }, 200)
    return () => { live = false; clearTimeout(timer) }
  }, [repo, q, author, pr, issue])

  const loadMore = () =>
    fetchCommits(repo, { q, author, pr, issue, offset: items.length, limit: PAGE })
      .then((p) => { setItems((cur) => [...cur, ...p.items]); setTotal(p.total) }, (e) => setListError(String(e.message ?? e)))

  return (
    <div className="commits">
      <section className="commit-list">
        <div className="commit-search">
          <input aria-label="Search commits" placeholder="Search title or sha" value={q} onChange={(e) => setQ(e.target.value)} />
          <input aria-label="Author" placeholder="Author" value={author} onChange={(e) => setAuthor(e.target.value)} />
          <div className="commit-filters">
            <input aria-label="Only the commits of PR number" placeholder="PR #" inputMode="numeric" value={pr} onChange={(e) => setPr(e.target.value.replace(/\D/g, ""))} />
            <input aria-label="Only the commits that touch issue number" placeholder="Issue #" inputMode="numeric" value={issue} onChange={(e) => setIssue(e.target.value.replace(/\D/g, ""))} />
          </div>
        </div>
        <div className="muted small">
          {total} of {synced} commits{lastSync ? ` · history read ${when(lastSync)}` : ""}
        </div>
        {listError && <p className="error">{listError}</p>}
        {synced === 0 && !listError && <p className="muted">No commits read yet: the first sync reads the whole history, it can take a while.</p>}
        <ul className="commit-rows">
          {items.map((c) => (
            <li key={c.sha} className="commit-item">
              <button className={`commit-row${sha === c.sha ? " on" : ""}`} onClick={() => setSha(c.sha)}>
                <span className="commit-title">{c.title}</span>
                <span className="muted small">
                  <code>{c.sha.slice(0, 7)}</code> · {c.author ?? "unknown"} · {when(c.at)}
                  {c.additions !== null && c.deletions !== null && <> · <span className="add">+{c.additions}</span> <span className="del">−{c.deletions}</span></>}
                  {c.inHistory === false && <> · <span className="muted">not on the default branch</span></>}
                </span>
              </button>
              <LinkChips pr={c.pr} issues={c.issues} onOpenItem={onOpenItem} />
            </li>
          ))}
        </ul>
        {items.length < total && <button className="more" onClick={loadMore}>Load more ({total - items.length} left)</button>}
      </section>

      <section className="commit-diff">
        {sha ? <CommitDiff repo={repo} sha={sha} onOpenItem={onOpenItem} /> : <p className="muted">Pick a commit to see its diff, old and new side by side.</p>}
      </section>
    </div>
  )
}

/** One commit's header and diffs. Used by the Commits tab and by a commit tab inside a PR or issue. */
export function CommitDiff({ repo, sha, onOpenItem }: { repo: string; sha: string; onOpenItem?: OpenItem }) {
  const [detail, setDetail] = useState<CommitDetail | null>(null)
  const [error, setError] = useState<string | null>(null)

  // The first call asks the host, the next ones read the cache.
  useEffect(() => {
    let live = true
    setDetail(null)
    setError(null)
    fetchCommit(repo, sha).then((d) => { if (live) setDetail(d) }, (e) => { if (live) setError(String(e.message ?? e)) })
    return () => { live = false }
  }, [repo, sha])

  if (error) return <p className="error">{error}</p>
  if (!detail) return <p className="muted">Reading the diff…</p>
  return <CommitView detail={detail} onOpenItem={onOpenItem} />
}

function CommitView({ detail, onOpenItem }: { detail: CommitDetail; onOpenItem?: OpenItem }) {
  const adds = detail.files.reduce((n, f) => n + f.additions, 0)
  const dels = detail.files.reduce((n, f) => n + f.deletions, 0)
  return (
    <div className="commit-view">
      <h3 className="commit-head">{detail.title}</h3>
      <div className="muted small">
        <code>{detail.sha}</code> · {detail.author ?? "unknown"} · {when(detail.at)} · {detail.files.length} files · <span className="add">+{adds}</span> <span className="del">−{dels}</span>
      </div>
      <div className="commit-links"><LinkChips pr={detail.pr} issues={detail.issues} onOpenItem={onOpenItem} /></div>
      {!detail.inHistory && <p className="muted small">Not on the default branch: a commit of a PR read from the host (it was squashed or never merged there).</p>}
      {detail.truncated && <p className="muted small">Some diffs are left out: the commit is larger than the cache keeps.</p>}
      {detail.files.length === 0 && <p className="muted">No file changes: a merge commit, or an empty one.</p>}
      {detail.files.map((f, i) => <FileDiff key={`${f.path}-${i}`} file={f} />)}
    </div>
  )
}

function FileDiff({ file }: { file: CommitFile }) {
  return (
    <details className="file-diff" open>
      <summary>
        <span className={`pill ${file.status}`}>{file.status}</span> <code>{pathOf(file)}</code>{" "}
        <span className="add">+{file.additions}</span> <span className="del">−{file.deletions}</span>
      </summary>
      {file.patch == null ? <p className="muted small">{file.note ?? "No text diff."}</p> : <SplitDiff patch={file.patch} />}
    </details>
  )
}

function SplitDiff({ patch }: { patch: string }) {
  const rows = useMemo(() => splitPatch(patch), [patch])
  return (
    <div className="scroll-x">
      <table className="split">
        <colgroup><col className="num" /><col /><col className="num" /><col /></colgroup>
        <tbody>
          {rows.map((r, i) =>
            r.type === "hunk" ? (
              <tr key={i} className="hunk"><td colSpan={4}>{r.text}</td></tr>
            ) : (
              <tr key={i}>
                <td className={`num ${r.left.kind}`}>{r.left.n ?? ""}</td>
                <td className={`code ${r.left.kind}`}>{r.left.text}</td>
                <td className={`num ${r.right.kind}`}>{r.right.n ?? ""}</td>
                <td className={`code ${r.right.kind}`}>{r.right.text}</td>
              </tr>
            ),
          )}
        </tbody>
      </table>
    </div>
  )
}
