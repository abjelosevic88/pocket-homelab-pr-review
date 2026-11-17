// A whole stack of pull requests: the stack in one minute (what each adds on
// top of the one below, what to watch across them), then each one, bottom
// first, with its walkthrough, how far you got and what you said.
import { useCallback, useEffect, useState } from 'react'
import { useParams } from 'react-router-dom'
import { api, V1 } from '../lib/api'
import type { Pull, StackMember, StackReviewPage, StackSummary } from '../lib/types'
import { useCrumb, announceChange } from '../components/Layout'
import { Icon } from '../components/icons'
import { Empty, Loading, Spinner, errMsg, useToast } from '../components/ui'
import { useOpenMember } from '../components/StackBar'
import { WalkPill } from './Inbox'

const careful = (m: StackMember) => m.budget?.find(b => b.focus === 'careful')?.lines || 0
/** The tree lines in front of a member: │ an ancestor's branch goes on, ├ a sibling follows, └ the last one, '' nothing. */
function guides(ms: StackMember[], i: number): string[] {
  const parentOf = new Map(ms.map(m => [m.number, m.parent]))
  const laterSibling = (n: number) => { const at = ms.findIndex(m => m.number === n); return ms.slice(at + 1).some(m => m.parent === parentOf.get(n)) }
  const chain: number[] = [] // from my depth-1 ancestor down to me
  for (let x = ms[i].number; parentOf.get(x); x = parentOf.get(x)!) chain.unshift(x)
  return chain.map((n, k) => k === chain.length - 1 ? (laterSibling(n) ? 'tee' : 'elbow') : (laterSibling(n) ? 'pipe' : ''))
}
const reviewedNow = (m: StackMember) => !!m.reviewed_current || m.state === 'merged'

export default function StackReview() {
  const { id } = useParams()
  const { toast } = useToast()
  const { open, busy } = useOpenMember()
  const [page, setPage] = useState<StackReviewPage | null>(null)
  const [err, setErr] = useState('')
  const [adding, setAdding] = useState(false)
  const [writing, setWriting] = useState(false)
  const load = useCallback(async () => {
    try { setPage(await api.get<StackReviewPage>(`${V1}/pulls/${id}/stack/review`)); setErr('') } catch (e) { setErr(errMsg(e)) }
  }, [id])
  useEffect(() => { load() }, [load])
  const members = page?.members || []
  useCrumb(page?.pull ? `${page.pull.repo} · stack of ${members.length || '…'}` : undefined)
  // while walkthroughs are being made, look again every few seconds
  const working = members.some(m => m.walkthrough && (m.walkthrough.status === 'queued' || m.walkthrough.status === 'working'))
  useEffect(() => {
    if (!working) return
    const t = setInterval(load, 4000)
    return () => clearInterval(t)
  }, [working, load])

  if (err) return <Empty icon="alert" title="Could not open the stack">{err}</Empty>
  if (!page) return <Loading />
  if (!page.stack || members.length < 2) return <Empty icon="layers" title="Not in a stack">{page.error || 'This pull request does not build on another one, and none builds on it.'}</Empty>

  const live = members.filter(m => m.state !== 'closed')
  const missing = live.filter(m => m.state === 'open' && (!m.local_id || !m.walkthrough || m.walkthrough.stale || m.walkthrough.status === 'failed'))
  const ready = live.filter(m => m.state !== 'open' || (m.walkthrough && m.walkthrough.units > 0)).length === live.length
  const next = members.find(m => m.state === 'open' && !reviewedNow(m))
  const addAll = async () => {
    setAdding(true)
    try { const r = await api.post<{ added: number; walkthroughs: number }>(`${V1}/pulls/${id}/stack/add`); toast(`${r.added ? `${r.added} added to your list, ` : ''}${r.walkthroughs} walkthrough${r.walkthroughs === 1 ? '' : 's'} being made`); announceChange(); load() } catch (e) { toast(errMsg(e), 'err') } finally { setAdding(false) }
  }
  const write = async () => {
    setWriting(true)
    try { const r = await api.post<{ summary: StackSummary }>(`${V1}/pulls/${id}/stack/summary`); setPage(p => p && { ...p, summary: r.summary, summary_stale: false }) } catch (e) { toast(errMsg(e), 'err') } finally { setWriting(false) }
  }
  const total = members.reduce((n, m) => n + m.additions + m.deletions, 0)
  const sum = page.summary

  return (
    <div className="stack loose stack-page">
      <header className="pr-head">
        <div className="pr-head-main">
          <h2 className="pr-h-title stack-h"><Icon name="layers" size={20} />A stack of {members.length} pull requests</h2>
          <div className="pr-h-meta muted small">
            <span>{page.pull?.repo}</span>
            <span>into <code>{page.stack.base}</code></span>
            <span className="tnum">{total.toLocaleString()} changed lines in all · {members.filter(reviewedNow).length} of {members.length} reviewed or merged</span>
          </div>
        </div>
        <div className="pr-head-actions">
          {next && <button type="button" className="btn primary" onClick={() => open(next)} disabled={busy === next.number}><Icon name="play" size={14} />Review #{next.number}</button>}
        </div>
      </header>

      {missing.length > 0 && (
        <div className="callout info">
          <Icon name="info" size={15} />
          <div>{missing.filter(m => !m.local_id).length > 0 ? `${missing.filter(m => !m.local_id).length} of them are not on your list yet` : `${missing.length} of them need a walkthrough`}. Add the whole stack and the walkthroughs are made by themselves.
            <div className="mt"><button type="button" className="btn sm primary" onClick={addAll} disabled={adding}>{adding ? <Spinner size={13} /> : <Icon name="plus" size={13} />}Add the stack and make the walkthroughs</button></div></div>
        </div>
      )}

      <section className="ov-card ov-hero">
        <span className="ov-kicker">The stack in one minute</span>
        {sum ? (
          <>
            <p className="ov-tldr">{sum.tldr}</p>
            {page.summary_stale && <p className="muted small">Written before the newest commits. <button type="button" className="link-btn" onClick={write} disabled={writing || !ready}>{writing ? 'Writing…' : 'Write it again'}</button></p>}
            {sum.risks.length > 0 && <>
              <h3 className="mt">Across the stack</h3>
              <ul className="risk-list">{sum.risks.map((r, i) => (
                <li key={i} className={`risk ${r.level}`}><span className={`risk-lvl ${r.level}`}>{r.level}</span><div className="risk-body"><span>{r.text}</span>
                  {r.numbers.length > 0 && <span className="risk-files">{r.numbers.map(n => <span key={n} className="file-chip">#{n}</span>)}</span>}</div></li>
              ))}</ul>
            </>}
            {sum.questions.length > 0 && <>
              <h3 className="mt">Questions for the author</h3>
              <ul className="q-list">{sum.questions.map((q, i) => <li key={i}>{q}</li>)}</ul>
            </>}
          </>
        ) : (
          <div className="muted small">
            {ready ? 'How they build on each other and what to watch across them, from their walkthroughs.' : 'Written from the walkthroughs once every pull request in the stack has one.'}
            <div className="mt"><button type="button" className="btn sm" onClick={write} disabled={writing || !ready}>{writing ? <Spinner size={13} /> : <Icon name="sparkles" size={13} />}{writing ? 'Writing…' : 'Write it'}</button></div>
          </div>
        )}
      </section>

      <section className="ov-card">
        <h3>Review them in this order</h3>
        <p className="muted small">From the bottom of the stack up: each one comes right after the one it builds on, so you read it on top of what you already know{members.some(m => m.depth > 0 && members.filter(x => x.parent === m.parent).length > 1) ? '; two built on the same one are side branches, indented the same' : ''}. A file that a later one changes again is marked: read it there for the idea, and for the details in the last one that changes it.</p>
        <ol className="stack-list">
          <li className="stack-card base stack-base-row"><span className="step-num"><Icon name="commit" size={12} /></span><span className="muted small">merges into</span> <code>{page.stack.base}</code></li>
          {members.map((m, i) => {
            const story = sum?.story.find(x => x.number === m.number)?.adds
            const later = Object.keys(m.later).length
            const laterIn = [...new Set(Object.values(m.later).flat())]
            const earlier = Object.keys(m.earlier).length
            const c = careful(m)
            return (
              <li key={m.number} className="stack-row">
              {guides(members, i).map((g, k) => <span key={k} className={`tg ${g}`} aria-hidden />)}
              <div className={`stack-card s-${m.state} ${m.number === next?.number ? 'next' : ''} ${reviewedNow(m) ? 'done' : ''}`}>
                <span className="step-num">{reviewedNow(m) ? <Icon name="check" size={13} strokeWidth={2.6} /> : i + 1}</span>
                <div className="stack-card-main">
                  <div className="stack-card-head">
                    <button type="button" className="link-btn stack-card-title" onClick={() => open(m)} disabled={busy === m.number}>#{m.number} {m.title}</button>
                    {m.here && <span className="badge sm">opened from</span>}
                    {m.parent > 0 && <span className="muted xsmall">on #{m.parent}</span>}
                  </div>
                  <div className="muted small row wrap">
                    <span className={`state-chip ${m.state} ${m.draft ? 'draft' : ''}`}>{m.draft ? 'Draft' : m.state === 'open' ? 'Open' : m.state === 'merged' ? 'Merged' : 'Closed'}</span>
                    <span className="tnum"><span className="add">+{m.additions}</span> <span className="del">−{m.deletions}</span> · {m.changed_files} files</span>
                    {c > 0 && <span className="careful-ink tnum">{c} lines to read carefully</span>}
                    <span>{m.author}</span>
                  </div>
                  {(story || m.tldr) && <p className="stack-card-text">{story || m.tldr}</p>}
                  {(later > 0 || earlier > 0) && <div className="xsmall muted later-note">
                    {later > 0 && <span className="later-ink" title={Object.entries(m.later).map(([f, ns]) => `${f} → ${ns.map(n => '#' + n).join(', ')}`).join('\n')}><Icon name="alert" size={11} />{later} file{later === 1 ? '' : 's'} changed again in {laterIn.map(n => `#${n}`).join(', ')}</span>}
                    {later > 0 && earlier > 0 && ' · '}
                    {earlier > 0 && <span title={Object.keys(m.earlier).join('\n')}>{earlier} file{earlier === 1 ? '' : 's'} first changed lower down</span>}
                  </div>}
                  <div className="row wrap stack-card-foot">
                    {m.local_id ? <WalkPill p={{ walkthrough: m.walkthrough } as Pull} /> : <span className="walk-pill none">Not on your list</span>}
                    {m.my_review && <span className={`badge ${m.my_review === 'APPROVED' ? 'ok' : m.my_review === 'CHANGES_REQUESTED' ? 'warn' : ''}`}>{m.my_review === 'APPROVED' ? 'You approved' : m.my_review === 'CHANGES_REQUESTED' ? 'You asked for changes' : 'You commented'}{m.reviewed_current ? '' : ' · new commits'}</span>}
                    {m.steps && m.steps.length > 0 && <span className="muted xsmall stack-steps">{m.steps.slice(0, 4).join(' · ')}{m.steps.length > 4 ? ` · +${m.steps.length - 4}` : ''}</span>}
                  </div>
                </div>
                <button type="button" className={`btn sm ${m.number === next?.number ? 'primary' : ''}`} onClick={() => open(m)} disabled={busy === m.number}>{m.local_id ? 'Open' : 'Add'}<Icon name="chevron-right" size={13} /></button>
              </div>
              </li>
            )
          })}
        </ol>
      </section>
    </div>
  )
}
