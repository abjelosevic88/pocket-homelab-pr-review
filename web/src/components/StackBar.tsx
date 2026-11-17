// A stack: pull requests built on each other (B merges into A's branch, C into
// B's). Shown bottom first, so each is reviewed on top of the one below it;
// one click opens a neighbour (or adds it to the list first), or the page
// that reviews the whole stack.
import { createContext, useContext, useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { api, V1 } from '../lib/api'
import type { Pull, Stack, StackItem } from '../lib/types'
import { Icon } from './icons'
import { errMsg, useToast } from './ui'

export interface StackInfo { stack: Stack | null; later: Record<string, number[]> }

/** The stack of the pull request on screen, for whoever needs it (file chips, the last step). */
export const StackContext = createContext<StackInfo>({ stack: null, later: {} })
export const useStackInfo = () => useContext(StackContext)

export function useStack(pullId: number | undefined, enabled: boolean): StackInfo {
  const [info, setInfo] = useState<StackInfo>({ stack: null, later: {} })
  useEffect(() => {
    if (!pullId || !enabled) return
    let live = true
    api.get<{ stack: Stack | null; later?: Record<string, number[]> }>(`${V1}/pulls/${pullId}/stack`).then(r => { if (live) setInfo({ stack: r.stack, later: r.later || {} }) }).catch(() => {})
    return () => { live = false }
  }, [pullId, enabled])
  return info
}

/** Open a member of the stack: on the list already, or added first. */
export function useOpenMember() {
  const navigate = useNavigate()
  const { toast } = useToast()
  const [busy, setBusy] = useState(0)
  const open = async (it: StackItem) => {
    if (it.local_id) { navigate(`/pr/${it.local_id}`); return }
    setBusy(it.number)
    try {
      const r = await api.post<{ pull: Pull }>(`${V1}/pulls`, { url: it.url })
      navigate(`/pr/${r.pull.id}`)
    } catch (e) { toast(errMsg(e), 'err') } finally { setBusy(0) }
  }
  return { open, busy }
}

export function StackBar({ pullId, stack }: { pullId: number; stack: Stack | null }) {
  const { open, busy } = useOpenMember()
  const navigate = useNavigate()
  if (!stack || stack.items.length < 2) return null
  // my line: what I build on and what builds on me; side branches only counted
  const line = stack.items.filter(x => x.line)
  const others = stack.items.length - line.length
  const here = line.findIndex(x => x.here)
  const below = line.slice(0, here).filter(x => x.state === 'open' && !x.reviewed_current)
  return (
    <div className="stack-bar" aria-label="The stack this pull request is in">
      <button type="button" className="stack-l" onClick={() => navigate(`/pr/${pullId}/stack`)} title="Review the whole stack: how they build on each other, then each one"><Icon name="layers" size={13} />Stack of {stack.items.length}</button>
      <ol className="stack-items">
        <li className="stack-base"><code>{stack.base}</code></li>
        {line.map(it => (
          <li key={it.number}>
            <span className="stack-arrow" aria-hidden>←</span>
            <button type="button" className={`stack-item s-${it.state} ${it.here ? 'here' : ''}`} onClick={() => !it.here && open(it)} disabled={busy === it.number}
              title={`#${it.number} ${it.title}\n${it.base_ref} ← ${it.head_ref}${it.local_id ? '' : '\n(not on your list yet: a click adds it)'}`} aria-current={it.here ? 'page' : undefined}>
              <span className="stack-state">{it.state === 'merged' ? <Icon name="check-circle" size={12} /> : it.reviewed_current ? <Icon name="check" size={12} strokeWidth={2.6} /> : <Icon name="pull" size={12} />}</span>
              <span className="tnum">#{it.number}</span>
              <span className="stack-t">{it.title}</span>
              {!it.local_id && !it.here && <Icon name="plus" size={12} />}
            </button>
          </li>
        ))}
      </ol>
      <button type="button" className="link-btn small" onClick={() => navigate(`/pr/${pullId}/stack`)}>{others > 0 ? `+${others} on a side branch · ` : ''}Review the whole stack</button>
      {below.length > 0 && <span className="stack-tip muted xsmall">Review the one{below.length === 1 ? '' : 's'} below first: {below.map(x => `#${x.number}`).join(', ')}</span>}
    </div>
  )
}
