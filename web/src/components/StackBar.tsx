// A stack: pull requests built on each other (B merges into A's branch, C into
// B's). Shown bottom first, so each is reviewed on top of the one below it;
// one click opens a neighbour, or adds it to the list first.
import { useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { api, V1 } from '../lib/api'
import type { Pull, Stack, StackItem } from '../lib/types'
import { Icon } from './icons'
import { errMsg, useToast } from './ui'

export function StackBar({ pullId }: { pullId: number }) {
  const [stack, setStack] = useState<Stack | null>(null)
  const [busy, setBusy] = useState(0)
  const navigate = useNavigate()
  const { toast } = useToast()
  useEffect(() => {
    let live = true
    api.get<{ stack: Stack | null }>(`${V1}/pulls/${pullId}/stack`).then(r => { if (live) setStack(r.stack) }).catch(() => {})
    return () => { live = false }
  }, [pullId])
  if (!stack || stack.items.length < 2) return null
  const here = stack.items.findIndex(x => x.here)
  const below = stack.items.slice(0, here).filter(x => x.state === 'open' && !x.reviewed_current)
  const open = async (it: StackItem) => {
    if (it.here) return
    if (it.local_id) { navigate(`/pr/${it.local_id}`); return }
    setBusy(it.number)
    try {
      const r = await api.post<{ pull: Pull }>(`${V1}/pulls`, { url: it.url })
      navigate(`/pr/${r.pull.id}`)
    } catch (e) { toast(errMsg(e), 'err') } finally { setBusy(0) }
  }
  return (
    <div className="stack-bar" aria-label="The stack this pull request is in">
      <span className="stack-l" title="Pull requests built on each other, the bottom one first"><Icon name="layers" size={13} />Stack of {stack.items.length}</span>
      <ol className="stack-items">
        <li className="stack-base"><code>{stack.base}</code></li>
        {stack.items.map(it => (
          <li key={it.number}>
            <span className="stack-arrow" aria-hidden>←</span>
            <button type="button" className={`stack-item s-${it.state} ${it.here ? 'here' : ''}`} onClick={() => open(it)} disabled={busy === it.number}
              title={`#${it.number} ${it.title}\n${it.base_ref} ← ${it.head_ref}${it.local_id ? '' : '\n(not on your list yet: a click adds it)'}`} aria-current={it.here ? 'page' : undefined}>
              <span className="stack-state">{it.state === 'merged' ? <Icon name="check-circle" size={12} /> : it.reviewed_current ? <Icon name="check" size={12} strokeWidth={2.6} /> : <Icon name="pull" size={12} />}</span>
              <span className="tnum">#{it.number}</span>
              <span className="stack-t">{it.title}</span>
              {!it.local_id && !it.here && <Icon name="plus" size={12} />}
            </button>
          </li>
        ))}
      </ol>
      {below.length > 0 && <span className="stack-tip muted xsmall">Review the one{below.length === 1 ? '' : 's'} below first: {below.map(x => `#${x.number}`).join(', ')}</span>}
    </div>
  )
}
