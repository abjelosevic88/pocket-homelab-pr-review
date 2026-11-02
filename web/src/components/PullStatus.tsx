// Where the pull request stands on GitHub now, in one line: CI, merging,
// reviews and open conversations. The CI chip opens the list of checks.
import { useCallback, useEffect, useState } from 'react'
import { api, V1 } from '../lib/api'
import { ago } from '../lib/format'
import { Icon } from './icons'
import { Spinner } from './ui'

interface Check { name: string; result: 'passed' | 'failed' | 'running' | 'skipped'; detail: string; url: string }
export interface GHStatus {
  state: string; draft: boolean; mergeable: string; merge_state: string; behind: number; commits: number
  review_decision: string; approved: string[]; changes_requested: string[]; commented: string[]; requested: string[]
  threads: number; unresolved: number; unresolved_outdated: number; comments: number
  checks: { known: boolean; state: string; passed: number; failed: number; running: number; skipped: number; items: Check[] }
  warnings: string[]
}

type Tone = 'ok' | 'bad' | 'warn' | 'muted'
const names = (xs: string[], n = 2) => xs.length <= n ? xs.join(', ') : `${xs.slice(0, n).join(', ')} +${xs.length - n}`

export function PullStatus({ pullId, base, prUrl }: { pullId: number; base: string; prUrl: string }) {
  const [st, setSt] = useState<GHStatus | null>(null)
  const [err, setErr] = useState('')
  const [at, setAt] = useState('')
  const [busy, setBusy] = useState(false)
  const [open, setOpen] = useState(false)
  const load = useCallback(async (fresh = false) => {
    setBusy(true)
    try {
      const r = await api.get<{ status: GHStatus | null; error?: string; checked_at?: string }>(`${V1}/pulls/${pullId}/status${fresh ? '?fresh=1' : ''}`)
      setSt(r.status); setErr(r.error || ''); setAt(r.checked_at || '')
    } catch (e) { setErr(String(e)) } finally { setBusy(false) }
  }, [pullId])
  useEffect(() => { load() }, [load])
  // while checks run, look again every half minute
  useEffect(() => {
    if (!st || st.checks.running === 0) return
    const t = setInterval(() => load(true), 30_000)
    return () => clearInterval(t)
  }, [st, load])

  if (!st) {
    if (busy) return <div className="ghs muted small"><Spinner size={12} /> GitHub status…</div>
    return err ? <div className="ghs muted small" title={err}><Icon name="alert" size={13} /> GitHub status unavailable: {err.length > 90 ? err.slice(0, 90) + '…' : err}</div> : null
  }

  const ck = st.checks
  const ci: [Tone, string, string] | null = !ck.known ? ['muted', 'CI not readable', 'The GitHub token cannot read checks: give it “Commit statuses: read” and “Checks: read” (fine-grained) or repo access (classic).']
    : ck.items.length === 0 ? ['muted', 'No CI', 'No checks reported on the newest commit.']
    : ck.failed > 0 ? ['bad', `${ck.failed} failing${ck.running ? ` · ${ck.running} running` : ''}`, '']
    : ck.running > 0 ? ['warn', `${ck.running} running · ${ck.passed} passed`, '']
    : ['ok', `${ck.passed} passed`, '']

  const behind = st.behind > 0 ? `${st.behind} commit${st.behind === 1 ? '' : 's'} behind ${base}` : ''
  const merge: [Tone, string, string] = st.state === 'MERGED' ? ['ok', 'Merged', '']
    : st.state === 'CLOSED' ? ['muted', 'Closed', '']
    : st.mergeable === 'CONFLICTING' || st.merge_state === 'DIRTY' ? ['bad', `Conflicts with ${base}`, behind]
    : st.draft || st.merge_state === 'DRAFT' ? ['muted', 'Draft', behind]
    : st.merge_state === 'BEHIND' ? ['warn', behind || `Behind ${base}`, 'The branch must be updated before it can merge.']
    : st.merge_state === 'BLOCKED' ? ['warn', st.behind > 0 ? `Blocked · ${st.behind} behind ${base}` : 'Blocked', 'A required review or check is missing.']
    : st.merge_state === 'UNSTABLE' ? ['warn', 'Can merge, checks not green', behind]
    : st.merge_state === 'CLEAN' || st.merge_state === 'HAS_HOOKS' ? ['ok', behind ? `No conflicts · ${behind}` : 'Ready to merge', '']
    : ['muted', 'Merge state unknown', 'GitHub is still working it out; refresh in a moment.']

  const rv: [Tone, string, string] = st.changes_requested.length ? ['bad', `Changes requested by ${names(st.changes_requested)}`, '']
    : st.review_decision === 'APPROVED' || (st.approved.length && st.review_decision !== 'REVIEW_REQUIRED') ? ['ok', `Approved${st.approved.length ? ` by ${names(st.approved)}` : ''}`, '']
    : st.requested.length ? ['warn', `Waiting on ${names(st.requested)}`, st.approved.length ? `Approved by ${st.approved.join(', ')}` : '']
    : st.review_decision === 'REVIEW_REQUIRED' ? ['warn', st.approved.length ? `${st.approved.length} approval, more required` : 'Review required', '']
    : ['muted', st.approved.length ? `Approved by ${names(st.approved)}` : 'No reviews yet', '']

  const conv: [Tone, string, string] | null = st.threads === 0 ? null
    : st.unresolved > 0 ? ['warn', `${st.unresolved} unresolved conversation${st.unresolved === 1 ? '' : 's'}`, st.unresolved_outdated ? `${st.unresolved_outdated} of them on lines changed since` : '']
    : ['ok', `All ${st.threads} conversation${st.threads === 1 ? '' : 's'} resolved`, '']

  const icon = (t: Tone) => t === 'ok' ? 'check-circle' : t === 'bad' ? 'x-circle' : t === 'warn' ? 'clock' : 'circle'
  return (
    <div className="ghs-wrap">
      <div className="ghs" role="list" aria-label="Status on GitHub">
        {ci && (
          <button type="button" role="listitem" className={`ghs-chip ${ci[0]}`} onClick={() => ck.items.length && setOpen(o => !o)} aria-expanded={open} title={ci[2] || 'The checks on the newest commit'} disabled={!ck.items.length}>
            <Icon name={ck.running && !ck.failed ? 'clock' : icon(ci[0])} size={13} /><span className="ghs-k">CI</span>{ci[1]}{ck.items.length > 0 && <Icon name={open ? 'chevron-up' : 'chevron-down'} size={12} />}
          </button>
        )}
        <span role="listitem" className={`ghs-chip ${merge[0]}`} title={merge[2]}><Icon name={st.state === 'MERGED' ? 'check-circle' : 'branch'} size={13} />{merge[1]}</span>
        <span role="listitem" className={`ghs-chip ${rv[0]}`} title={rv[2]}><Icon name="users" size={13} />{rv[1]}</span>
        {conv && <a role="listitem" className={`ghs-chip ${conv[0]}`} href={prUrl} target="_blank" rel="noreferrer" title={conv[2] || 'Open the conversation on GitHub'}><Icon name="message" size={13} />{conv[1]}</a>}
        <button type="button" className="ghs-refresh" onClick={() => load(true)} disabled={busy} title={`Checked ${at ? ago(at) : 'now'} — check again`}>
          {busy ? <Spinner size={12} /> : <Icon name="refresh" size={13} />}
        </button>
        {st.warnings.length > 0 && <span className="ghs-chip muted plain" title={st.warnings.join('\n')}><Icon name="info" size={13} />{st.warnings.length} not readable</span>}
      </div>
      {open && ck.items.length > 0 && (
        <ul className="ghs-checks">
          {ck.items.map((c, i) => (
            <li key={i} className={c.result}>
              <Icon name={c.result === 'passed' ? 'check-circle' : c.result === 'failed' ? 'x-circle' : c.result === 'running' ? 'clock' : 'circle'} size={14} />
              {c.url ? <a href={c.url} target="_blank" rel="noreferrer">{c.name}</a> : <span>{c.name}</span>}
              <span className="muted small">{c.detail.toLowerCase().replace(/_/g, ' ')}</span>
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}
