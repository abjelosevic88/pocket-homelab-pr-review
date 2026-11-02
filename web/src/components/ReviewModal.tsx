// Finish the review: what you think, your comments, and approve / ask for changes / just comment — sent to GitHub.
import { useState } from 'react'
import { api, V1 } from '../lib/api'
import type { Comment, Pull } from '../lib/types'
import { Icon } from './icons'
import { Modal, Segmented, errMsg, useToast } from './ui'

type Event = 'COMMENT' | 'APPROVE' | 'REQUEST_CHANGES'

export function ReviewModal({ pull, drafts, canSend, demo, onClose, onSent, onRemove }: {
  pull: Pull; drafts: Comment[]; canSend: boolean; demo: boolean; onClose: () => void; onSent: (url: string) => void; onRemove: (id: number) => Promise<void>
}) {
  const mine = pull.reason === 'authored'
  const [event, setEvent] = useState<Event>(drafts.length || mine ? 'COMMENT' : 'APPROVE')
  const [body, setBody] = useState('')
  const [busy, setBusy] = useState(false)
  const { toast } = useToast()
  const lines = drafts.filter(d => d.path)
  const notes = drafts.filter(d => !d.path)
  const send = async () => {
    setBusy(true)
    try {
      const r = await api.post<{ review: { gh_url: string } }>(`${V1}/pulls/${pull.id}/review`, { event, body })
      onSent(r.review.gh_url)
    } catch (e) { toast(errMsg(e), 'err'); setBusy(false) }
  }
  const empty = event !== 'APPROVE' && !body.trim() && drafts.length === 0
  return (
    <Modal title="Finish the review" onClose={onClose} size="lg" footer={<>
      <button type="button" className="btn ghost" onClick={onClose}>Not yet</button>
      <button type="button" className={`btn ${event === 'REQUEST_CHANGES' ? 'danger' : 'primary'}`} onClick={send} disabled={busy || !canSend || empty}>
        {busy ? 'Sending…' : event === 'APPROVE' ? 'Approve' : event === 'REQUEST_CHANGES' ? 'Ask for changes' : 'Send the comments'}
      </button>
    </>}>
      <div className="stack">
        {!canSend && <div className="callout warn"><Icon name="github" size={15} /><div>Connect GitHub (Settings → GitHub) to send reviews.</div></div>}
        {demo && <div className="callout info"><Icon name="info" size={15} /><div>This is the demo: the review is kept here and not sent to GitHub.</div></div>}
        <Segmented value={event} onChange={v => setEvent(v as Event)} options={[
          { value: 'COMMENT', label: 'Comment' },
          { value: 'APPROVE', label: 'Approve', disabled: !!mine, title: mine ? 'GitHub does not let you approve your own pull request' : undefined },
          { value: 'REQUEST_CHANGES', label: 'Ask for changes', disabled: !!mine },
        ]} />
        <textarea rows={4} value={body} onChange={e => setBody(e.target.value)} placeholder={event === 'APPROVE' ? 'Anything to add? (optional)' : 'What you think overall…'} aria-label="Review text" />
        {drafts.length > 0 && (
          <div className="review-drafts">
            <h4 className="sc-h">Goes with it: {drafts.length} comment{drafts.length === 1 ? '' : 's'}</h4>
            {lines.map(c => (
              <div key={c.id} className="rd-row">
                <code className="rd-where">{c.path}:{c.start_line ? `${c.start_line}–` : ''}{c.line}</code>
                <span className="rd-body">{c.body}</span>
                <button type="button" className="btn ghost xs icon-only" onClick={() => onRemove(c.id)} aria-label="Remove"><Icon name="x" size={13} /></button>
              </div>
            ))}
            {notes.map(c => (
              <div key={c.id} className="rd-row">
                <span className="rd-where badge">{c.step}</span>
                <span className="rd-body">{c.body}</span>
                <button type="button" className="btn ghost xs icon-only" onClick={() => onRemove(c.id)} aria-label="Remove"><Icon name="x" size={13} /></button>
              </div>
            ))}
            {notes.length > 0 && <p className="muted small">Notes on steps go into the review's text, under “By step”.</p>}
          </div>
        )}
      </div>
    </Modal>
  )
}
