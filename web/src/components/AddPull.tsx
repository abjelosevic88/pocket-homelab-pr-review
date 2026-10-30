// Add a pull request by its address; its walkthrough starts right away.
import { useEffect, useRef, useState } from 'react'
import { api, V1 } from '../lib/api'
import type { Pull } from '../lib/types'
import { Modal, errMsg } from './ui'

const looksLikePR = (s: string) => /\/pull\/\d+|^[\w.-]+\/[\w.-]+#\d+$/.test(s.trim())

export function AddPull({ initial = '', onClose, onAdded }: { initial?: string; onClose: () => void; onAdded: (id: number) => void }) {
  const [url, setUrl] = useState(initial)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const auto = useRef(looksLikePR(initial))
  const submit = async () => {
    if (!url.trim() || busy) return
    setBusy(true); setErr('')
    try {
      const r = await api.post<{ pull: Pull }>(`${V1}/pulls`, { url: url.trim() })
      onAdded(r.pull.id)
    } catch (e) { setErr(errMsg(e)); setBusy(false) }
  }
  // a link from another app (…/?add=https://github.com/…/pull/12) adds at once
  useEffect(() => { if (auto.current) { auto.current = false; submit() } }, []) // eslint-disable-line react-hooks/exhaustive-deps
  const paste = async () => { try { const t = await navigator.clipboard.readText(); if (t) setUrl(t.trim()) } catch { /* not allowed */ } }
  return (
    <Modal title="Add a pull request" onClose={onClose} footer={<>
      <button type="button" className="btn ghost" onClick={onClose}>Cancel</button>
      <button type="button" className="btn primary" onClick={submit} disabled={busy || !url.trim()}>{busy ? 'Adding…' : 'Add and make the walkthrough'}</button>
    </>}>
      <div className="stack">
        <p className="muted small">Paste its address from GitHub. The walkthrough starts at once; pull requests that ask for your review come in by themselves.</p>
        <div className="row">
          <input autoFocus value={url} onChange={e => setUrl(e.target.value)} onKeyDown={e => { if (e.key === 'Enter') submit() }} placeholder="https://github.com/owner/repo/pull/12" aria-label="Pull request address" />
          <button type="button" className="btn" onClick={paste}>Paste</button>
        </div>
        {err && <p className="bad small">{err}</p>}
      </div>
    </Modal>
  )
}
