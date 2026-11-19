// "Message for Slack": Pocket AI writes a line about the pull request, the
// person picks who to @mention, edits, and copies it with the link to paste
// in Slack. Nothing is sent from here; the people are kept in this app.
import { useEffect, useMemo, useState } from 'react'
import { api, V1 } from '../lib/api'
import { Icon } from './icons'
import type { GHStatus } from './PullStatus'
import { Field, Modal, Spinner, copyText, errMsg, useToast } from './ui'

export interface SlackPerson { name: string; handle?: string; github?: string }
interface SlackConf { people: SlackPerson[]; prompt: string; default_prompt: string }
interface Draft { text: string; link: string; note: string }
export interface SlackPull { id: number; title: string; url: string; repo: string; number: number }

const LAST = 'pr-review:slack-last'
const tag = (p: SlackPerson) => '@' + (p.handle || p.name)

export function slackMessage(people: SlackPerson[], text: string, link: string) {
  const m = people.map(tag).join(' ')
  return `${m ? m + ' ' : ''}${text.trim()}\n${link}`
}

export function SlackCopy({ pull, onClose }: { pull: SlackPull; onClose: () => void }) {
  const { toast } = useToast()
  const [conf, setConf] = useState<SlackConf | null>(null)
  const [draft, setDraft] = useState<Draft | null>(null)
  const [text, setText] = useState('')
  const [picked, setPicked] = useState<string[]>([])
  const [writing, setWriting] = useState(false)
  const [editing, setEditing] = useState(false)
  const [adding, setAdding] = useState(false)
  const [how, setHow] = useState<string | null>(null)
  const [err, setErr] = useState('')

  const write = async (again = false) => {
    setWriting(true)
    try {
      const d = await api.post<Draft>(`${V1}/pulls/${pull.id}/slack/draft`, { again })
      setDraft(d); setText(d.text)
    } catch (e) { setDraft({ text: pull.title, link: pull.url, note: errMsg(e) }); setText(t => t || pull.title) } finally { setWriting(false) }
  }

  useEffect(() => {
    let alive = true
    Promise.all([
      api.get<SlackConf>(`${V1}/slack`),
      api.get<{ status: GHStatus | null }>(`${V1}/pulls/${pull.id}/status`).catch(() => ({ status: null })),
    ]).then(([c, s]) => {
      if (!alive) return
      setConf(c)
      // who GitHub waits on comes first; else the people picked last time
      const waiting = new Set((s.status?.requested || []).map(x => x.toLowerCase()))
      const matched = c.people.filter(p => p.github && waiting.has(p.github.toLowerCase())).map(p => p.name)
      let last: string[] = []
      try { last = JSON.parse(localStorage.getItem(LAST) || '[]') } catch {}
      setPicked(matched.length ? matched : last.filter(n => c.people.some(p => p.name === n)))
    }).catch(e => alive && setErr(errMsg(e)))
    write()
    return () => { alive = false }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [pull.id])

  const people = conf?.people || []
  const chosen = useMemo(() => people.filter(p => picked.includes(p.name)), [people, picked])
  const link = draft?.link || pull.url
  const message = slackMessage(chosen, text, link)

  const savePeople = async (list: SlackPerson[]) => {
    try { const c = await api.put<SlackConf>(`${V1}/slack`, { people: list }); setConf(c); return c } catch (e) { toast(errMsg(e), 'err'); return null }
  }
  const saveHow = async (v: string) => {
    try { const c = await api.put<SlackConf>(`${V1}/slack`, { prompt: v }); setConf(c); setHow(null); toast('Saved: it writes this way from now on'); write(true) } catch (e) { toast(errMsg(e), 'err') }
  }
  const copy = async (what: 'message' | 'link') => {
    const ok = await copyText(what === 'message' ? message : link)
    if (!ok) { toast('Could not copy', 'err'); return }
    if (what === 'message') {
      try { localStorage.setItem(LAST, JSON.stringify(picked)) } catch {}
      toast('Copied: paste it in Slack', 'ok')
      onClose()
    } else toast('Link copied', 'ok')
  }

  return (
    <Modal title="Message for Slack" onClose={onClose} size="lg" footer={<>
      <button type="button" className="btn" onClick={onClose}>Cancel</button>
      <button type="button" className="btn" onClick={() => copy('link')}><Icon name="link" size={14} />Copy link</button>
      <button type="button" className="btn primary" disabled={!draft || !text.trim()} onClick={() => copy('message')}><Icon name="copy" size={14} />Copy message</button>
    </>}>
      {err && <div className="callout warn"><Icon name="alert" size={15} /><span>{err}</span></div>}
      <div className="stack">
        <Field group label={<span className="slack-lab">Mention
          {people.length > 0 && <button type="button" className="link-btn small" onClick={() => { setEditing(e => !e); setAdding(false) }}>{editing ? 'Done' : 'Edit people'}</button>}
        </span>} help={people.length > 0 && !editing ? 'Slack turns @Name into a mention when it matches their name; if one stays plain, give them their Slack handle under Edit people.' : undefined}>
          {!conf ? <div className="row muted small"><Spinner size={12} />…</div> : editing ? (
            <PeopleEditor people={people} onSave={async l => { if (await savePeople(l)) setEditing(false) }} />
          ) : (
            <div className="row wrap slack-chips" role="group" aria-label="People to mention">
              {people.map(p => {
                const on = picked.includes(p.name)
                return <button key={p.name} type="button" className={`chip-toggle ${on ? 'on' : ''}`} aria-pressed={on} title={[p.handle && `Slack @${p.handle}`, p.github && `GitHub ${p.github}`].filter(Boolean).join(' · ') || undefined}
                  onClick={() => setPicked(v => on ? v.filter(x => x !== p.name) : [...v, p.name])}>@{p.name}</button>
              })}
              {!adding && <button type="button" className="chip-toggle add" onClick={() => setAdding(true)}><Icon name="plus" size={13} />{people.length ? 'Someone else' : 'Add who to mention'}</button>}
            </div>
          )}
          {adding && !editing && <AddPerson onCancel={() => setAdding(false)} onAdd={async p => {
            const c = await savePeople([...people, p])
            if (c) { setAdding(false); setPicked(v => [...v, p.name]) }
          }} />}
        </Field>

        <Field group label={<span className="slack-lab">Message
          {draft && <button type="button" className="link-btn small" onClick={() => write(true)} disabled={writing}>{writing ? <><Spinner size={11} /> Writing…</> : 'Write it again'}</button>}
        </span>} help={draft?.note || (draft ? <>Written by Pocket AI from the pull request and its walkthrough: check it before sending. <button type="button" className="link-btn" onClick={() => setHow(h => h === null ? (conf?.prompt || conf?.default_prompt || '') : null)}>Change how it writes</button></> : undefined)}>
          {!draft ? <div className="row muted small"><Spinner size={12} />Pocket AI is writing a line about it…</div>
            : <textarea value={text} onChange={e => setText(e.target.value)} rows={4} maxLength={3000} autoFocus />}
        </Field>

        {how !== null && (
          <div className="slack-how">
            <Field label="How Pocket AI writes it" help="Used for every pull request.">
              <textarea value={how} onChange={e => setHow(e.target.value)} rows={3} maxLength={2000} />
            </Field>
            <div className="row">
              <button type="button" className="btn sm primary" onClick={() => saveHow(how)}>Save and write again</button>
              {conf?.prompt && <button type="button" className="btn sm" onClick={() => saveHow('')}>Back to the default</button>}
              <button type="button" className="btn sm ghost" onClick={() => setHow(null)}>Cancel</button>
            </div>
          </div>
        )}

        {draft && (
          <div className="slack-preview" aria-label="Preview">
            <div className="slack-preview-h">Preview</div>
            <p>{chosen.length > 0 && <><span className="slack-mention">{chosen.map(tag).join(' ')}</span> </>}{text}</p>
            <a href={link} target="_blank" rel="noreferrer">{link}</a>
          </div>
        )}
      </div>
    </Modal>
  )
}

function AddPerson({ onAdd, onCancel }: { onAdd: (p: SlackPerson) => void; onCancel: () => void }) {
  const [p, setP] = useState<SlackPerson>({ name: '', handle: '', github: '' })
  const ok = p.name.trim().length > 0
  return (
    <form className="slack-add" onSubmit={e => { e.preventDefault(); if (ok) onAdd(p) }}>
      <input value={p.name} onChange={e => setP({ ...p, name: e.target.value })} placeholder="Name as in Slack" aria-label="Name as in Slack" autoFocus />
      <input value={p.handle} onChange={e => setP({ ...p, handle: e.target.value })} placeholder="Slack handle (optional)" aria-label="Slack handle" />
      <input value={p.github} onChange={e => setP({ ...p, github: e.target.value })} placeholder="GitHub login (optional)" aria-label="GitHub login" title="Picked by itself when GitHub waits on their review" />
      <div className="row">
        <button type="submit" className="btn sm primary" disabled={!ok}>Add</button>
        <button type="button" className="btn sm ghost" onClick={onCancel}>Cancel</button>
      </div>
    </form>
  )
}

function PeopleEditor({ people, onSave }: { people: SlackPerson[]; onSave: (l: SlackPerson[]) => void }) {
  const [list, setList] = useState<SlackPerson[]>(people.map(p => ({ name: p.name, handle: p.handle || '', github: p.github || '' })))
  const set = (i: number, k: keyof SlackPerson, v: string) => setList(l => l.map((x, j) => j === i ? { ...x, [k]: v } : x))
  return (
    <div className="slack-people">
      <div className="slack-people-h muted small"><span>Name as in Slack</span><span>Slack handle</span><span>GitHub login</span><span /></div>
      {list.map((p, i) => (
        <div key={i} className="slack-person">
          <input value={p.name} onChange={e => set(i, 'name', e.target.value)} aria-label="Name as in Slack" />
          <input value={p.handle} onChange={e => set(i, 'handle', e.target.value)} placeholder="optional" aria-label="Slack handle" />
          <input value={p.github} onChange={e => set(i, 'github', e.target.value)} placeholder="optional" aria-label="GitHub login" />
          <button type="button" className="btn ghost icon-only sm" onClick={() => setList(l => l.filter((_, j) => j !== i))} title="Remove" aria-label={`Remove ${p.name}`}><Icon name="trash" size={14} /></button>
        </div>
      ))}
      <p className="muted small">With a GitHub login, they are picked by themselves when GitHub waits on their review.</p>
      <div className="row"><button type="button" className="btn sm primary" onClick={() => onSave(list)}>Save</button></div>
    </div>
  )
}
