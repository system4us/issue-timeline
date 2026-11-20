// CI state of a pull request: a dot on its bar (opens the run) and a line in its detail (with the link).

export interface CiRun {
  state: string // passed | failed | skipped | running | cancelled | pending | unknown
  url: string
  at: string | null
  count: number // CI runs the PR's commits had
}

/** A commit that changed the CI definition (a workflow file on GitHub, .gitlab-ci.yml on GitLab). */
export interface CiChange {
  sha: string
  title: string
  author: string | null
  at: string | null
  url: string | null
}

export interface CiSummary {
  byPr: Record<string, CiRun>
  changes: CiChange[]
  other: Record<string, { count: number; last: { url: string; at: string; state: string; name: string } | null }>
  lastRunAt: string | null
}

export const CI_LABEL: Record<string, string> = {
  passed: "passed", failed: "failed", skipped: "skipped (created, did not run)", running: "running",
  cancelled: "cancelled", pending: "waiting for approval", unknown: "unknown",
}

/** The CI history of a PR as a vertical line: the CI definition's changes while the PR was open, and its last CI run. */
/** Whether a change of the CI definition is the PR's own: one of its commits, or its squash ("title (#N)") or merge commit. */
export function changeIsOf(c: CiChange, number: string, commitOids: string[]): boolean {
  if (commitOids.includes(c.sha)) return true
  return new RegExp(`\\(#${number}\\)\\s*$|^Merge pull request #${number}\\b`).test(c.title)
}

export function CiTrail({ number, createdAt, endAt, endLabel, commitOids, run, changes }: { number: string; createdAt: string; endAt: string | null; endLabel: string; commitOids: string[]; run?: CiRun; changes: CiChange[] }) {
  const start = Date.parse(createdAt)
  const end = endAt ? Date.parse(endAt) : Date.now()
  // The PR's own changes to the CI, and the ones the repo made while the PR was open.
  const relevant = changes.filter((c) => changeIsOf(c, number, commitOids) || (c.at && Date.parse(c.at) >= start && Date.parse(c.at) <= end))
  // The line always has its ends (opened, and merged or closed), so a PR without CI changes still shows the line.
  const events = [
    { kind: "open" as const, at: createdAt },
    ...relevant.map((c) => ({ kind: "change" as const, at: c.at ?? createdAt, change: c, own: changeIsOf(c, number, commitOids) })),
    ...(run ? [{ kind: "run" as const, at: run.at ?? createdAt, run }] : [{ kind: "none" as const, at: endAt ?? createdAt }]),
    ...(endAt ? [{ kind: "end" as const, at: endAt }] : []),
  ].sort((a, b) => a.at.localeCompare(b.at))

  return (
    <div className="ci-trail-wrap">
      <ol className="ci-trail">
        {events.map((e, i) => {
          if (e.kind === "open") return <li key={`o-${i}`} className="ci-ev ci-ev-open"><span className="ci-when">{e.at.slice(0, 10)}</span> PR opened</li>
          if (e.kind === "end") return <li key={`e-${i}`} className="ci-ev ci-ev-open"><span className="ci-when">{e.at.slice(0, 10)}</span> PR {endLabel}</li>
          if (e.kind === "none") return <li key={`n-${i}`} className="ci-ev ci-ev-none"><span className="ci-when">{e.at.slice(0, 10)}</span> no CI run for this PR</li>
          if (e.kind === "change") {
            return (
              <li key={`c-${i}`} className={`ci-ev ci-ev-change${e.own ? " own" : ""}`}>
                <span className="ci-when">{e.at.slice(0, 10)}</span> {e.own ? "this PR changed the CI" : "CI definition changed meanwhile"} · <span>{e.change.title}</span>
                {e.change.author && <span className="muted"> · {e.change.author}</span>}
                {e.change.url && <> · <a href={e.change.url} target="_blank" rel="noreferrer">commit ↗</a></>}
              </li>
            )
          }
          return (
            <li key={`r-${i}`} className={`ci-ev ci-ev-run ci-${e.run.state}`}>
              <span className="ci-when">{e.at.slice(0, 10)}</span> CI {CI_LABEL[e.run.state] ?? e.run.state} · <a href={e.run.url} target="_blank" rel="noreferrer">last run ↗</a>
              {e.run.count > 1 && <span className="muted"> · {e.run.count} runs</span>}
            </li>
          )
        })}
      </ol>
      <div className="ci-legend" aria-label="Legend">
        <span><i className="lg lg-open" /> PR opened / ended</span>
        <span><i className="lg lg-change" /> CI definition changed</span>
        <span><i className="lg lg-own" /> by this PR</span>
        <span><i className="lg lg-run" /> CI run (last one)</span>
        <span><i className="lg lg-none" /> no CI run</span>
      </div>
    </div>
  )
}
