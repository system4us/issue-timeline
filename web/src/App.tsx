import { useCallback, useEffect, useMemo, useState, type FormEvent } from "react"
import { addRepo, fetchData, fetchMe, fetchRepos, setRepoMode, triggerSync, type Me, type RepoMode } from "./api"
import { SignIn } from "./SignIn"
import { NewHostIssue } from "./NewHostIssue"
import { SearchBox } from "./SearchBox"
import type { Item, Payload } from "./types"
import { Timeline, type ColorBy } from "./Timeline"
import { Analysis } from "./Analysis"
import { useLiveEvents } from "./live"
import { Commits } from "./Commits"
import { RepoSwitcher } from "./RepoSwitcher"
import { DetailPanel } from "./Detail"
import { setVocabulary } from "./fields"
import { MultiSelect, type Option } from "./MultiSelect"
import { EMPTY_FILTERS, NONE, activeChips, applyFilters, fromQuery, isEmpty, isOverdue, linkIndex, toQuery, type Filters } from "./filters"
import { ZOOM_RANGE, fitZoom, makeScale, type Axis } from "./timeScale"
import type { TimelineGroup } from "./grouping"

type Tab = "timeline" | "analisis" | "commits"

const ago = (iso: string | null) => {
  if (!iso) return "never"
  const s = Math.round((Date.now() - Date.parse(iso)) / 1000)
  return s < 90 ? `${s}s ago` : s < 5400 ? `${Math.round(s / 60)} min ago` : `${Math.round(s / 3600)} h ago`
}

const repoLabel = (key: string) => {
  if (!key.startsWith("gitlab:")) return key
  try {
    const url = new URL(key.slice("gitlab:".length))
    const path = decodeURIComponent(url.pathname.replace(/^\/+|\/+$/g, ""))
    return url.hostname === "gitlab.com" ? path : `${url.host}/${path}`
  } catch { return key }
}

/** The day n days back, as the date inputs write it (yyyy-mm-dd, local time). */
const daysAgo = (n: number) => {
  const d = new Date(Date.now() - n * 86_400_000)
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`
}

const countBy = <T,>(xs: T[], keys: (x: T) => string[]) => {
  const m = new Map<string, number>()
  for (const x of xs) for (const k of keys(x)) m.set(k, (m.get(k) ?? 0) + 1)
  return m
}

export function App() {
  const [data, setData] = useState<Payload | null>(null)
  const [repos, setRepos] = useState<string[]>([])
  const [repoModes, setRepoModes] = useState<Record<string, RepoMode>>({})
  const [canConfigure, setCanConfigure] = useState(true)
  const [me, setMe] = useState<Me | null>(null)
  const [meChecked, setMeChecked] = useState(false)
  const [loaded, setLoaded] = useState(false)
  const [signInPrompt, setSignInPrompt] = useState(false)
  const [newOpen, setNewOpen] = useState(false)
  useEffect(() => { void fetchMe().then(setMe, () => setMe(null)).finally(() => setMeChecked(true)) }, [])
  const [repo, setRepo] = useState(() => localStorage.getItem("issue-timeline-repo") ?? "")
  const [repoDraft, setRepoDraft] = useState("")
  const [repoProvider, setRepoProvider] = useState<"github" | "gitlab">("github")
  useEffect(() => {
    if (me?.provider === "github" || me?.provider === "gitlab") setRepoProvider(me.provider)
  }, [me?.provider])
  const [addingRepo, setAddingRepo] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [tab, setTab] = useState<Tab>("timeline")
  const [selected, setSelected] = useState<Item | null>(null)
  const [filters, setFilters] = useState<Filters>(() => fromQuery(window.location.search))
  const [showIssues, setShowIssues] = useState(true)
  const [showPulls, setShowPulls] = useState(true)
  // A tracker (Jira, Redmine) has no code of its own: its pull requests, commits and CI come from the code repos linked to it.
  const isTracker = /^(jira|redmine):/.test(repo)
  const hasCode = !isTracker || (data?.linked?.length ?? 0) > 0
  useEffect(() => { if (!hasCode && tab === "commits") setTab("timeline") }, [hasCode, tab])
  const [axis, setAxis] = useState<Axis>("actividad")
  const [zoom, setZoom] = useState<Record<Axis, number | null>>({ tiempo: null, actividad: null })
  const [colorBy, setColorBy] = useState<ColorBy>("estado")
  const [groupBy, setGroupBy] = useState<TimelineGroup>("ninguno")
  const [allLinks, setAllLinks] = useState(false)
  const [openToNow, setOpenToNow] = useState(false)
  const [showTree, setShowTree] = useState(false)
  const [showDeps, setShowDeps] = useState(false)
  // Bumped to open the detail full screen right away (the timeline's right-click "Open full screen").
  const [expandRequest, setExpandRequest] = useState(0)
  const openExpanded = useCallback((item: Item) => { setSelected(item); setExpandRequest((n) => n + 1) }, [])

  const patch = useCallback((p: Partial<Filters>) => setFilters((f) => ({ ...f, ...p })), [])
  // A PR or issue named by a commit: opens it in the detail panel, whatever tab is showing.
  const openItem = useCallback((kind: "issue" | "pull", number: string) => {
    const item = kind === "pull" ? data?.pulls.find((p) => p.key === number) : data?.issues.find((i) => i.key === number)
    if (item) setSelected(item)
  }, [data])

  // The filters live in the URL: a reload or a copied link keeps the view.
  useEffect(() => {
    const q = toQuery(filters)
    window.history.replaceState(null, "", q ? `?${q}` : window.location.pathname)
  }, [filters])

  const load = useCallback(async () => {
    try {
      const { repos: configured, modes: modeMap, canConfigure: configurable } = await fetchRepos()
      setRepos(configured)
      setRepoModes(modeMap)
      setCanConfigure(configurable)
      const active = configured.includes(repo) ? repo : (configured[0] ?? "")
      if (active !== repo) {
        setRepo(active)
        if (active) localStorage.setItem("issue-timeline-repo", active)
        else localStorage.removeItem("issue-timeline-repo")
      }
      if (active) {
        const payload = await fetchData(active)
        setVocabulary(payload.vocabulary)
        setData(payload)
      }
      else setData(null)
      setError(null)
    } catch (e) {
      const msg = String((e as Error).message ?? e)
      // Off this machine the API asks for a sign-in: open the dialog instead of an error.
      if (/ 401$/.test(msg)) setSignInPrompt(true)
      setError(msg)
    } finally {
      setLoaded(true)
    }
  }, [repo])

  // Adds a repository and shows it. The header's dialog and the start screen both use it; a refusal is thrown back to them.
  const addRepoNow = async (input: string, provider: "github" | "gitlab" | "redmine" | "jira", mode: RepoMode = "read") => {
    setAddingRepo(true)
    try {
      const result = await addRepo(input.trim(), provider, mode)
      setRepos(result.repos)
      setRepo(result.repo)
      localStorage.setItem("issue-timeline-repo", result.repo)
      setData(null)
      setSelected(null)
      setError(null)
    } finally { setAddingRepo(false) }
  }
  const submitRepo = async (e: FormEvent) => {
    e.preventDefault()
    try { await addRepoNow(repoDraft, repoProvider); setRepoDraft("") } catch (err) { setError(String((err as Error).message ?? err)) }
  }
  const pickRepo = (next: string) => { setRepo(next); localStorage.setItem("issue-timeline-repo", next); setData(null); setSelected(null) }

  // Live: the server tells the page when something changes (an edit by anyone, a sync, a webhook from the host), and
  // the page reads the data again. While that stream is open the page only polls during a sync, for its progress;
  // without it (a proxy that drops it, an old browser) it falls back to reading every 30 s.
  const { live } = useLiveEvents(repo, () => { void load() })
  useEffect(() => {
    load()
    if (live && !data?.status.syncing) return
    const t = setInterval(load, data?.status.syncing ? 3000 : 30_000)
    return () => clearInterval(t)
  }, [load, data?.status.syncing, repo, live])

  // Each option's count is taken with every other filter applied, but not its own group: picking a label must not
  // zero out the other labels, and the counts follow the state, dates, etc. (a label on 49 issues, 12 of them open).
  const options = useMemo(() => {
    const all = data?.issues ?? []
    const allPulls = data?.pulls ?? []
    const without = (key: "milestones" | "labels" | "authors" | "assignees" | "epics") =>
      data ? applyFilters(data.issues, data.pulls, data.links, { ...filters, [key]: [] }, data.tree, data.deps) : { issues: [], pulls: [] }
    const msBase = without("milestones")
    const labelBase = without("labels")
    const authorBase = without("authors")
    const assigneeBase = without("assignees")
    const epicBase = without("epics")
    const colors = new Map([...all, ...allPulls].flatMap((i) => i.labels.map((l) => [l.name, l.color] as const)))
    const milestones = countBy(msBase.issues, (i) => [i.milestone?.title ?? NONE])
    const labels = countBy(labelBase.issues, (i) => i.labels.map((l) => l.name))
    const authors = countBy([...authorBase.issues, ...authorBase.pulls], (i) => (i.author ? [i.author] : []))
    const assignees = countBy(assigneeBase.issues, (i) => (i.assignees.length ? i.assignees : [NONE]))
    const toOpts = (m: Map<string, number>, withColor = false): Option[] =>
      [...m].sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0])).map(([value, count]) => ({ value, count, color: withColor ? colors.get(value) : undefined, label: value === NONE ? "(unassigned / no milestone)" : undefined }))
    // An epic counts the children that pass the other filters; the list itself still offers every parent with children.
    const idx = data ? linkIndex(data.issues, data.pulls, data.links, data.tree, data.deps) : linkIndex([], [], [])
    const shown = new Set(epicBase.issues.map((i) => i.key))
    const epics: Option[] = [...idx.childrenOf]
      .map(([n, kids]) => ({ value: String(n), label: `#${n} ${all.find((i) => i.key === n)?.title ?? ""}`, count: kids.filter((k) => shown.has(k.key)).length }))
      .sort((a, b) => b.count - a.count)
    return { milestones: toOpts(milestones), labels: toOpts(labels, true), authors: toOpts(authors), assignees: toOpts(assignees), epics }
  }, [data, filters])

  const filtered = useMemo(
    () => (data ? applyFilters(data.issues, data.pulls, data.links, filters, data.tree, data.deps) : { issues: [], pulls: [], idx: linkIndex([], [], []) }),
    [data, filters],
  )
  const issues = showIssues ? filtered.issues : []
  const pulls = showPulls ? filtered.pulls : []

  // The axis is built from the items on screen: a filtered view is as dense as the whole one, instead of the few
  // items that match being spread over the history of everything that does not.
  const now = useMemo(() => Date.now(), [])
  const times = useMemo(
    () => [now, ...[...issues, ...pulls].flatMap((i) => [i.createdAt, i.closedAt, i.kind === "pull" ? i.mergedAt : null]).filter((s): s is string => !!s).map(Date.parse)],
    [issues, pulls, now],
  )
  const viewport = Math.max(600, window.innerWidth - 80)
  const fit = useMemo(() => fitZoom(axis, times, viewport, ZOOM_RANGE[axis]), [axis, times, viewport])
  // By default the activity axis opens at a readable size (scroll for the rest); "Ajustar" fits everything.
  // The activity axis opens at a readable size when there is a lot to scroll through; with a few items the whole
  // view fits the width instead, so they are not drawn small in a corner of an empty canvas.
  const denseView = issues.length + pulls.length > 40
  const z = zoom[axis] ?? (axis === "actividad" && denseView ? 12 : fit)
  const scale = useMemo(() => makeScale(axis, times, z), [axis, times, z])
  const [zmin, zmax] = ZOOM_RANGE[axis]
  const setZ = (v: number) => setZoom((cur) => ({ ...cur, [axis]: Math.max(zmin, Math.min(zmax, v)) }))
  // The slider is logarithmic: the useful zoom runs from "the whole history" to "an hour per few pixels".
  const toSlider = (v: number) => Math.log(v / zmin) / Math.log(zmax / zmin)
  const fromSlider = (f: number) => zmin * Math.pow(zmax / zmin, f)

  const needsSignIn = meChecked && !me && error !== null && / 401$/.test(error)
  if (!data && (!loaded || !meChecked)) return (
    <div className="app">
      <header className="top">
        <div className="top-start"><h1>issue-timeline</h1></div>
      </header>
      <main className="boot"><p>Loading…</p></main>
    </div>
  )
  if (!data) return <div className="boot">
    <div style={{ display: "flex", justifyContent: "space-between", alignItems: "center", gap: 12 }}>
      <h1>issue-timeline</h1>
      <SignIn me={me} repo={repo} onChange={(m) => { setMe(m); void load() }} prompt={signInPrompt} onClosePrompt={() => setSignInPrompt(false)} />
    </div>
    {error && !needsSignIn && <p className="error">{error}</p>}
    {needsSignIn ? (
      <p>Sign in to continue.</p>
    ) : repos.length === 0 ? (
      canConfigure ? (
        <>
          <p>Add a repository to get started.</p>
          <form onSubmit={submitRepo}><select aria-label="Provider" value={repoProvider} onChange={(e) => setRepoProvider(e.target.value as "github" | "gitlab")}><option value="github">GitHub</option><option value="gitlab">GitLab</option></select><input aria-label="Repository" placeholder={repoProvider === "github" ? "owner/repo" : "group/project or URL"} value={repoDraft} onChange={(e) => setRepoDraft(e.target.value)} /><button disabled={addingRepo || !repoDraft.trim()}>{addingRepo ? "Adding…" : "Add repo"}</button></form>
        </>
      ) : (
        <p>No repositories are configured on this server yet.</p>
      )
    ) : (
      <>
        <p>Loading {repo}…</p>
        {canConfigure && <form onSubmit={submitRepo}><select aria-label="Provider" value={repoProvider} onChange={(e) => setRepoProvider(e.target.value as "github" | "gitlab")}><option value="github">GitHub</option><option value="gitlab">GitLab · glab</option></select><input aria-label="Repository" placeholder={repoProvider === "github" ? "owner/repo" : "group/project or URL"} value={repoDraft} onChange={(e) => setRepoDraft(e.target.value)} /><button disabled={addingRepo || !repoDraft.trim()}>{addingRepo ? "Adding…" : "Add repo"}</button></form>}
        <ul>{repos.map((r) => <li key={r}><button onClick={() => { setRepo(r); localStorage.setItem("issue-timeline-repo", r); setData(null) }}>{repoLabel(r)}</button></li>)}</ul>
      </>
    )}
  </div>

  const { status, meta } = data
  const chips = activeChips(filters)
  const activeCount = Object.entries(filters).filter(([k, v]) => JSON.stringify(v) !== JSON.stringify((EMPTY_FILTERS as unknown as Record<string, unknown>)[k])).length

  return (
    <div className="app" onClick={() => setSelected(null)}>
      <header className="top" onClick={(e) => e.stopPropagation()}>
        <div className="top-start">
          <RepoSwitcher repos={repos} modes={repoModes} onMode={async (r, m) => { await setRepoMode(r, m); await load() }} current={repo} label={repoLabel} onPick={pickRepo} onAdd={addRepoNow} linked={data?.linked ?? []} codeRepos={repos.filter((r) => !/^(jira|redmine):/.test(r))} onLinksSaved={() => { void load() }} canConfigure={canConfigure} />
          <nav>
            <button className={tab === "timeline" ? "on" : ""} onClick={() => setTab("timeline")}>Timeline</button>
            <button className={tab === "analisis" ? "on" : ""} onClick={() => setTab("analisis")}>Analysis</button>
            {hasCode && <button className={tab === "commits" ? "on" : ""} onClick={() => setTab("commits")}>Commits</button>}
          </nav>
        </div>
        <SearchBox repo={repo} onPick={(kind, key) => { const item = [...(data?.issues ?? []), ...(data?.pulls ?? [])].find((i) => i.key === key && i.kind === kind); if (item) setSelected(item) }} />
        <div className="top-end">
          <div className="sync">
            <span className={`dot ${status.syncing ? "busy" : status.lastError ? "err" : "ok"}`} />
            {status.syncing ? <>
              <span>{status.progress?.phase ?? "syncing…"}</span>
              <span className="sync-progress" role="progressbar" aria-label={`Sync: ${status.progress?.phase ?? "in progress"}`} aria-valuemin={0} aria-valuemax={status.progress?.total ?? undefined} aria-valuenow={status.progress?.total ? status.progress.done : undefined} title={status.progress?.total ? `${status.progress.done} / ${status.progress.total}` : "In progress"}>
                <span className={status.progress?.total == null ? "indeterminate" : ""} style={status.progress?.total ? { width: `${Math.min(100, (status.progress.done / status.progress.total) * 100)}%` } : undefined} />
              </span>
              {status.progress?.total != null && <small>{status.progress.done}/{status.progress.total}</small>}
            </> : <span className="sync-ago">sync {ago(meta.lastSync)}</span>}
            {status.lastError && <span className="error" title={status.lastError}> · error</span>}
            <span className={`live-badge${live ? " on" : ""}`} title={live ? "Live: changes by anyone, and the host's webhooks, show here as they happen" : "Not live: the page reads the data every 30 s"}>{live ? "live" : "polling"}</span>
            <button className="quiet" onClick={() => triggerSync(false, repo).then(load)} disabled={status.syncing} title={`Fetch what changed since the last sync · ${status.syncing ? "syncing" : `sync ${ago(meta.lastSync)}`}`}>Sync</button>
            {/* A full re-read goes through the whole history and can take many minutes: it sits behind a menu, not next to Sync. */}
            <details className="sync-more">
              <summary title="More sync options">⋯</summary>
              <div className="sync-menu">
                <p className="muted small">Re-reads the whole history. Can take many minutes on a large repo.</p>
                <button onClick={(e) => { e.currentTarget.closest("details")?.removeAttribute("open"); triggerSync(true, repo).then(load) }} disabled={status.syncing}>Full re-read</button>
              </div>
            </details>
          </div>
          {(data?.mode ?? repoModes[repo]) === "write" && (!isTracker || repo.startsWith("redmine:")) && <button className="signin-btn" onClick={() => setNewOpen(true)}>New issue</button>}
          {newOpen && <NewHostIssue repo={repo} issues={data?.issues ?? []} onClose={() => setNewOpen(false)} onCreated={() => { void load() }} />}
          {meChecked && <SignIn me={me} repo={repo} onChange={(m) => { setMe(m); void load() }} prompt={signInPrompt} onClosePrompt={() => setSignInPrompt(false)} />}
        </div>
      </header>

      {/* The filters apply to the issues and PRs; the commits have their own search. */}
      <div className="filters" hidden={tab === "commits"} onClick={(e) => e.stopPropagation()}>
        <div className="fgroup fgroup-search">
          <input placeholder="Search title or #number" value={filters.query} onChange={(e) => patch({ query: e.target.value })} />
        </div>
        <div className="fgroup" role="group" aria-label="Filter by">
          <MultiSelect title="Milestones" options={options.milestones} selected={filters.milestones} onChange={(milestones) => patch({ milestones })} />
          <MultiSelect title="Labels" options={options.labels} selected={filters.labels} onChange={(labels) => patch({ labels })} />
          {filters.labels.length > 1 && (
            <select className="active" value={filters.labelMode} onChange={(e) => patch({ labelMode: e.target.value as Filters["labelMode"] })} title="How the labels combine">
              <option value="any">match any</option><option value="all">match all</option>
            </select>
          )}
          <MultiSelect title="Epics" options={options.epics} selected={filters.epics} onChange={(epics) => patch({ epics })} />
          <MultiSelect title="Authors" options={options.authors} selected={filters.authors} onChange={(authors) => patch({ authors })} />
          <MultiSelect title="Assignees" options={options.assignees} selected={filters.assignees} onChange={(assignees) => patch({ assignees })} />
        </div>
        <div className="fgroup" role="group" aria-label="Status">
          <label className={filters.overdue ? "active" : undefined} title="Only open issues whose own due date has passed">
            <input type="checkbox" checked={filters.overdue} onChange={(e) => patch({ overdue: e.target.checked })} /> Overdue ({data.issues.filter((i) => isOverdue(i, now)).length})
          </label>
          <select className={filters.state !== "all" ? "active" : undefined} value={filters.state} onChange={(e) => patch({ state: e.target.value as Filters["state"] })}>
            <option value="all">All states</option><option value="open">Open</option><option value="closed">Closed / merged</option>
          </select>
          {hasCode && <select className={filters.linkage !== "any" ? "active" : undefined} value={filters.linkage} onChange={(e) => patch({ linkage: e.target.value as Filters["linkage"] })} title="Whether there is a PR behind the issue (or an issue behind the PR)">
            <option value="any">With or without link</option><option value="with">With linked PR / issue</option><option value="without">No link</option>
          </select>}
          <label className={`check${filters.parentsOnly ? " active" : ""}`} title="Issues that have children or are children of another"><input type="checkbox" checked={filters.parentsOnly} onChange={(e) => patch({ parentsOnly: e.target.checked })} /> With children / as tree</label>
          <select className={filters.blocking !== "any" ? "active" : undefined} value={filters.blocking} onChange={(e) => patch({ blocking: e.target.value as Filters["blocking"] })} title="Dependencies: blocked by something still open, or blockers">
            <option value="any">With or without blocks</option><option value="blocked">Blocked now</option><option value="blockers">Open blockers</option>
          </select>
        </div>
        <div className="fgroup" role="group" aria-label="Created">
          <label className="dates">Created <input type="date" value={filters.createdFrom} onChange={(e) => patch({ createdFrom: e.target.value })} /> – <input type="date" value={filters.createdTo} onChange={(e) => patch({ createdTo: e.target.value })} /></label>
          <div className="presets" role="group" aria-label="Created in the last">
            {[[30, "30 d"], [90, "90 d"], [365, "1 year"]].map(([days, label]) => (
              <button key={label} className={filters.createdFrom === daysAgo(days as number) && !filters.createdTo ? "on" : ""} onClick={() => patch({ createdFrom: daysAgo(days as number), createdTo: "" })}>{label}</button>
            ))}
            {(filters.createdFrom || filters.createdTo) && <button onClick={() => patch({ createdFrom: "", createdTo: "" })} title="Any date">all</button>}
          </div>
        </div>
        <div className="fgroup fgroup-end">
          {chips.length > 0 && (
            <div className="achips-inline" role="list" aria-label="Active filters">
              {chips.map((c) => (
                <button key={c.key} role="listitem" className="achip" onClick={() => patch(c.patch)} title="Remove this filter">
                  {c.text} <span aria-hidden="true">×</span>
                </button>
              ))}
            </div>
          )}
          <div className="seg" role="group" aria-label="Show">
            <label className={showIssues ? "on" : ""}><input type="checkbox" checked={showIssues} onChange={(e) => setShowIssues(e.target.checked)} /> Issues ({filtered.issues.length}/{data.issues.length})</label>
            {hasCode && <label className={showPulls ? "on" : ""}><input type="checkbox" checked={showPulls} onChange={(e) => setShowPulls(e.target.checked)} /> PRs ({filtered.pulls.length}/{data.pulls.length})</label>}
          </div>
          {!isEmpty(filters) && <button className="clear-btn" onClick={() => setFilters(EMPTY_FILTERS)} title="Remove all filters">Clear filters ({activeCount})</button>}
        </div>
      </div>

      {tab === "timeline" && (
        <div className="filters view" onClick={(e) => e.stopPropagation()}>
          {(data.ci?.changes?.length ?? 0) > 0 && (
            <span className="ci-legend-chip" title="Each dashed line in the timeline is a moment the CI definition changed">
              <i className="lg lg-change" /> CI definition changed · {data.ci!.changes.length}
            </span>
          )}
          <select value={groupBy} onChange={(e) => setGroupBy(e.target.value as TimelineGroup)} title="Grouped rows">
            <option value="ninguno">No grouping</option><option value="milestone">Group by milestone</option><option value="etiqueta">Group by 1st label</option><option value="autor">Group by author</option><option value="epica">Group by epic (parent)</option>
          </select>
          <select value={axis} onChange={(e) => setAxis(e.target.value as Axis)} title="What the horizontal axis measures">
            <option value="actividad">Axis: order of events</option>
            <option value="tiempo">Axis: time (without idle periods)</option>
          </select>
          <label>Zoom <input type="range" min={0} max={1} step={0.002} value={toSlider(z)} onChange={(e) => setZ(fromSlider(Number(e.target.value)))} /></label>
          <button onClick={() => setZoom((c) => ({ ...c, [axis]: null }))} title="Fit the whole history on screen">Fit</button>
          <button onClick={() => setZ(z * 2)}>＋</button>
          <button onClick={() => setZ(z / 2)}>－</button>
          <select value={colorBy} onChange={(e) => setColorBy(e.target.value as ColorBy)} title="Bar color">
            <option value="estado">Color: status</option><option value="etiqueta">Color: 1st label</option><option value="autor">Color: author</option>
          </select>
          <label><input type="checkbox" checked={allLinks} onChange={(e) => setAllLinks(e.target.checked)} /> All links</label>
          <label title="Sorts each epic into its own block: the parent on top like a bracket, its children staggered below"><input type="checkbox" checked={showTree} onChange={(e) => setShowTree(e.target.checked)} /> Hierarchy</label>
          <label title="Draws every arrow blocker → blocked (red: the blocker is still open)"><input type="checkbox" checked={showDeps} onChange={(e) => setShowDeps(e.target.checked)} /> Dependencies</label>
          <label title="Open items are drawn up to today; otherwise they fade out shortly after they start"><input type="checkbox" checked={openToNow} onChange={(e) => setOpenToNow(e.target.checked)} /> Open until today</label>
        </div>
      )}

      <main>
        {tab === "timeline" ? (
          <Timeline ciChanges={data.ci?.changes} ci={data.ci?.byPr} issues={issues} pulls={pulls} links={data.links} scale={scale} now={now} colorBy={colorBy} showAllLinks={allLinks} openToNow={openToNow} deps={data.deps} tree={data.tree} showDeps={showDeps} showTree={showTree} groupBy={groupBy} prefer={groupBy === "milestone" ? filters.milestones : groupBy === "etiqueta" ? filters.labels : groupBy === "epica" ? filters.epics : []} focusKey={`${toQuery(filters)}|${groupBy}|${showTree}|${showIssues}${showPulls}`} idx={filtered.idx} selected={selected} onSelect={setSelected} onExpand={openExpanded} onFilters={patch} />
        ) : tab === "commits" ? (
          <Commits repo={repo} onOpenItem={openItem} />
        ) : (
          <Analysis repo={repo} meta={data.meta} hasCode={hasCode} ci={data.ci} issues={filtered.issues} pulls={filtered.pulls} links={data.links} tree={data.tree} deps={data.deps} all={{ issues: data.issues, pulls: data.pulls }} filters={filters} onFilters={patch} onSelect={setSelected} />
        )}
        {selected && <DetailPanel ci={data.ci} item={selected} repo={repo} issues={data.issues} pulls={data.pulls} links={data.links} idx={filtered.idx} onSelect={setSelected} onClose={() => setSelected(null)} expandRequest={expandRequest} onChanged={() => { void load() }} writable={(data?.mode ?? repoModes[repo]) === "write"} />}
      </main>
    </div>
  )
}
