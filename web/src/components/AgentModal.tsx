// Hand the review comments to a coding agent: Pocket AI prepares the
// conversation in the repository's folder; you read it there and press send.
import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { api, V1 } from '../lib/api'
import type { Pull } from '../lib/types'
import { Icon } from './icons'
import { Select } from './Select'
import { Field, Loading, Modal, Switch, errMsg, useToast } from './ui'

interface Options { connected: boolean; detail?: string; hosts?: { id: number; name: string }[]; providers?: { id: string; name: string }[]; default_provider?: string; home?: string; folder?: { host: number; cwd: string } }
interface Dirs { path: string; parent: string; dirs: { name: string; path: string; git?: boolean }[] }

export function AgentModal({ pull, onClose }: { pull: Pull; onClose: () => void }) {
  const [opts, setOpts] = useState<Options | null>(null)
  const [host, setHost] = useState(0)
  const [cwd, setCwd] = useState('')
  const [provider, setProvider] = useState('')
  const [text, setText] = useState('')
  const [title, setTitle] = useState('')
  const [count, setCount] = useState(0)
  const [worktree, setWorktree] = useState(true)
  const [remember, setRemember] = useState(true)
  const [browse, setBrowse] = useState<Dirs | null>(null)
  const [busy, setBusy] = useState(false)
  const { toast } = useToast()
  useEffect(() => {
    api.get<Options>(`${V1}/agent/options?repo=${encodeURIComponent(pull.repo)}`).then(o => {
      setOpts(o)
      if (o.folder?.cwd) { setHost(o.folder.host); setCwd(o.folder.cwd) }
      setProvider(o.default_provider || '')
    }).catch(e => setOpts({ connected: false, detail: errMsg(e) }))
    api.get<{ title: string; text: string; comments: number }>(`${V1}/pulls/${pull.id}/agent/prompt`).then(p => { setText(p.text); setTitle(p.title); setCount(p.comments) }).catch(() => {})
  }, [pull.id, pull.repo])
  const look = async (path: string) => {
    try { setBrowse(await api.get<Dirs>(`${V1}/agent/dirs?host=${host}&path=${encodeURIComponent(path)}`)) } catch (e) { toast(errMsg(e), 'err') }
  }
  const send = async () => {
    setBusy(true)
    try {
      const r = await api.post<{ url: string }>(`${V1}/pulls/${pull.id}/agent`, { host, cwd, provider, title, text, worktree, remember })
      window.open(r.url, '_blank', 'noopener')
      toast('Ready in Pocket AI — read it there and press send')
      onClose()
    } catch (e) { toast(errMsg(e), 'err'); setBusy(false) }
  }
  return (
    <Modal title="Hand it to a coding agent" onClose={onClose} size="lg" footer={opts?.connected ? <>
      <button type="button" className="btn ghost" onClick={onClose}>Cancel</button>
      <button type="button" className="btn primary" onClick={send} disabled={busy || !cwd.trim() || !text.trim()}><Icon name="bot" size={15} />{busy ? 'Preparing…' : 'Prepare in Pocket AI'}</button>
    </> : <button type="button" className="btn" onClick={onClose}>Close</button>}>
      {!opts ? <Loading /> : !opts.connected ? (
        <div className="callout info"><Icon name="bot" size={16} /><div>
          <b>Pocket AI is not connected.</b> {opts.detail}. With it, your comments go to Claude Code or Codex in the repository's folder, on this pull request's branch. <Link to="/settings/apps" onClick={onClose}>Connect Pocket AI</Link>
        </div></div>
      ) : (
        <div className="stack">
          <p className="muted small">Pocket AI prepares the conversation with {count ? `your ${count} comment${count === 1 ? '' : 's'}` : 'this pull request'}; nothing runs until you press send there.</p>
          <div className="two-col tight">
            <Field label="Machine">
              <Select ariaLabel="Machine" value={String(host)} onChange={v => { setHost(Number(v)); setBrowse(null) }} options={(opts.hosts || []).map(h => ({ value: String(h.id), label: h.name }))} />
            </Field>
            <Field label="Agent">
              <Select ariaLabel="Agent" value={provider} onChange={setProvider} options={[{ value: '', label: 'Pocket AI’s default' }, ...(opts.providers || []).map(p => ({ value: p.id, label: p.name }))]} />
            </Field>
          </div>
          <Field label="The repository's folder" help={`Where ${pull.repo} is checked out on that machine.`}>
            <div className="row">
              <input value={cwd} onChange={e => setCwd(e.target.value)} placeholder={(opts.home || '~') + '/Projects/' + pull.repo.split('/')[1]} />
              <button type="button" className="btn" onClick={() => look(cwd || '')}>Browse</button>
            </div>
          </Field>
          {browse && (
            <div className="dir-list">
              <div className="dir-head"><code className="ellipsis">{browse.path}</code>{browse.parent && <button type="button" className="btn ghost xs" onClick={() => look(browse.parent)}>Up</button>}<button type="button" className="btn primary xs" onClick={() => { setCwd(browse.path); setBrowse(null) }}>Use this folder</button></div>
              {browse.dirs.slice(0, 200).map(d => <button key={d.path} type="button" className="dir-row" onClick={() => look(d.path)}><Icon name={d.git ? 'branch' : 'archive'} size={14} className="muted" />{d.name}</button>)}
              {browse.dirs.length === 0 && <div className="muted small pad">No folders inside.</div>}
            </div>
          )}
          <div className="row wrap">
            <Switch checked={worktree} onChange={setWorktree} label={`Work in its own worktree on ${pull.head_ref}`} />
            <span className="muted small">{worktree ? `In its own worktree on ${pull.head_ref}` : 'In the folder as it is'}</span>
          </div>
          <div className="row wrap">
            <Switch checked={remember} onChange={setRemember} label="Remember the folder" />
            <span className="muted small">Remember this folder for {pull.repo}</span>
          </div>
          <Field label="The message">
            <textarea rows={10} value={text} onChange={e => setText(e.target.value)} className="mono-sm" />
          </Field>
        </div>
      )}
    </Modal>
  )
}
