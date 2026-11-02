// How data moves through what a pull request adds: boxes in layers, arrows
// with what moves along them. Left to right; top to bottom on a phone.
import { useEffect, useMemo, useRef, useState } from 'react'
import type { Diagram, DNode } from '../lib/types'

const KIND_LABEL: Record<string, string> = { actor: 'person', event: 'event', code: 'code', store: 'stored', api: 'API', ui: 'screen', external: 'other system' }
const LINE = 16
const PAD_X = 12
const CHARS = 18

/** Words into lines of at most CHARS characters. */
function wrap(s: string): string[] {
  const out: string[] = []
  let cur = ''
  for (const w of s.split(/\s+/)) {
    if (cur && (cur + ' ' + w).length > CHARS) { out.push(cur); cur = w } else cur = cur ? cur + ' ' + w : w
  }
  if (cur) out.push(cur)
  return out.slice(0, 3)
}

interface Box { n: DNode; lines: string[]; rank: number; x: number; y: number; w: number; h: number; virtual?: boolean }
interface Route { label: string; back: boolean; pts: { x: number; y: number }[] }

const W = 156

function layout(d: Diagram, vertical: boolean) {
  const GAP_R = vertical ? 66 : 96 // between layers (room for the arrow's words)
  const GAP_N = vertical ? 16 : 18 // between boxes of one layer
  const ids = new Set(d.nodes.map(n => n.id))
  const edges = d.edges.filter(e => ids.has(e.from) && ids.has(e.to))
  const inn = new Map<string, string[]>()
  for (const e of edges) inn.set(e.to, [...(inn.get(e.to) || []), e.from])

  // layer = longest path from a source; arrows that close a loop are ignored
  const rank = new Map<string, number>()
  const onPath = new Set<string>()
  const visit = (id: string): number => {
    if (rank.has(id)) return rank.get(id)!
    if (onPath.has(id)) return -1
    onPath.add(id)
    let r = 0
    for (const p of inn.get(id) || []) { const pr = visit(p); if (pr >= 0) r = Math.max(r, pr + 1) }
    onPath.delete(id)
    rank.set(id, r)
    return r
  }
  d.nodes.forEach(n => visit(n.id))

  // an arrow across several layers passes through a small invisible box in each
  const chains: string[][] = []
  const vnodes: DNode[] = []
  edges.forEach((e, i) => {
    const r1 = rank.get(e.from)!, r2 = rank.get(e.to)!
    const chain = [e.from]
    for (let r = r1 + 1; r < r2; r++) {
      const id = `\u0000${i}:${r}`
      vnodes.push({ id, label: '', kind: 'virtual', new: false, step: 0 })
      rank.set(id, r)
      chain.push(id)
    }
    chain.push(e.to)
    chains.push(chain)
  })
  // inputs one layer up, through the invisible boxes too
  const adj = new Map<string, string[]>()
  for (const c of chains) for (let k = 1; k < c.length; k++) if (rank.get(c[k])! - rank.get(c[k - 1])! === 1) adj.set(c[k], [...(adj.get(c[k]) || []), c[k - 1]])
  const preds = (id: string) => adj.get(id) || []

  const layers: DNode[][] = []
  for (const n of [...d.nodes, ...vnodes]) (layers[rank.get(n.id)!] ||= []).push(n)
  // order inside a layer by where its inputs sit
  const pos = new Map<string, number>()
  layers.forEach(l => l.forEach((n, i) => pos.set(n.id, i)))
  for (let sweep = 0; sweep < 3; sweep++) {
    for (const l of layers.slice(1)) {
      const bary = (n: DNode) => { const ps = preds(n.id).map(p => pos.get(p)).filter((x): x is number => x !== undefined); return ps.length ? ps.reduce((a, b) => a + b, 0) / ps.length : pos.get(n.id)! }
      const by = new Map(l.map(n => [n.id, bary(n)]))
      l.sort((a, b) => by.get(a.id)! - by.get(b.id)!).forEach((n, i) => pos.set(n.id, i))
    }
  }

  const VW = 14 // an invisible box's room
  const sizes = layers.map(l => l.map(n => {
    const virtual = n.kind === 'virtual'
    const lines = virtual ? [] : wrap(n.label)
    return { n, lines, virtual, h: virtual ? (vertical ? 52 : VW) : 30 + lines.length * LINE, w: virtual ? (vertical ? VW : W) : W }
  }))
  const along = sizes.map(l => l.reduce((a, b) => a + (vertical ? b.w : b.h), 0) + (l.length - 1) * GAP_N)
  const span = Math.max(...along)
  const boxes = new Map<string, Box>()
  let across = 0
  sizes.forEach((l, r) => {
    let at = (span - along[r]) / 2
    const thick = vertical ? Math.max(...l.map(b => b.virtual ? 0 : b.h), 30) : W
    for (const b of l) {
      const box: Box = { n: b.n, lines: b.lines, rank: r, w: b.w, h: b.h, x: 0, y: 0, virtual: b.virtual }
      if (vertical) { box.x = at; box.y = across + (thick - b.h) / 2; at += b.w + GAP_N } else { box.x = across; box.y = at; at += b.h + GAP_N }
      boxes.set(b.n.id, box)
    }
    across += thick + GAP_R
  })
  across -= GAP_R

  const routes: Route[] = chains.map((c, i) => {
    const a = boxes.get(c[0])!, b = boxes.get(c[c.length - 1])!
    const back = b.rank <= a.rank
    const pts: { x: number; y: number }[] = []
    if (vertical) {
      pts.push({ x: a.x + a.w / 2, y: back ? a.y : a.y + a.h })
      for (const v of c.slice(1, -1)) { const x = boxes.get(v)!; pts.push({ x: x.x + x.w / 2, y: x.y }, { x: x.x + x.w / 2, y: x.y + x.h }) }
      pts.push({ x: b.x + b.w / 2, y: back ? b.y + b.h : b.y })
    } else {
      pts.push({ x: back ? a.x : a.x + a.w, y: a.y + a.h / 2 })
      for (const v of c.slice(1, -1)) { const x = boxes.get(v)!; pts.push({ x: x.x, y: x.y + x.h / 2 }, { x: x.x + x.w, y: x.y + x.h / 2 }) }
      pts.push({ x: back ? b.x + b.w : b.x, y: b.y + b.h / 2 })
    }
    return { label: edges[i].label, back, pts }
  })
  return { boxes: [...boxes.values()].filter(b => !b.virtual), routes, width: vertical ? span : across, height: vertical ? across : span }
}

/** A smooth path through the points: curves between layers, straight through a passed layer. */
function pathOf(r: Route, vertical: boolean) {
  const p = r.pts
  let d = `M${p[0].x},${p[0].y}`
  for (let i = 1; i < p.length; i++) {
    const a = p[i - 1], b = p[i]
    if (i % 2 === 0) { d += ` L${b.x},${b.y}`; continue } // inside a passed layer
    const k = Math.max(24, Math.abs(vertical ? b.y - a.y : b.x - a.x) / 2) * (r.back ? -1 : 1)
    d += vertical ? ` C${a.x},${a.y + k} ${b.x},${b.y - k} ${b.x},${b.y}` : ` C${a.x + k},${a.y} ${b.x - k},${b.y} ${b.x},${b.y}`
  }
  return d
}

/** Where an arrow's words go: the middle of its first curve. */
function labelAt(r: Route, vertical: boolean) {
  const a = r.pts[0], b = r.pts[1]
  const k = Math.max(24, Math.abs(vertical ? b.y - a.y : b.x - a.x) / 2) * (r.back ? -1 : 1)
  const [c1, c2] = vertical ? [{ x: a.x, y: a.y + k }, { x: b.x, y: b.y - k }] : [{ x: a.x + k, y: a.y }, { x: b.x - k, y: b.y }]
  return { x: (a.x + 3 * c1.x + 3 * c2.x + b.x) / 8, y: (a.y + 3 * c1.y + 3 * c2.y + b.y) / 8 }
}

/** The width of an element, as it changes. */
function useWidth() {
  const [w, setW] = useState(0)
  const ref = useRef<HTMLDivElement>(null)
  useEffect(() => {
    const el = ref.current
    if (!el) return
    const ro = new ResizeObserver(([e]) => setW(e.contentRect.width))
    ro.observe(el)
    return () => ro.disconnect()
  }, [])
  return [ref, w] as const
}

export function DataFlow({ diagram, vertical: phone, onStep }: { diagram: Diagram; vertical: boolean; onStep: (n: number) => void }) {
  const [ref, avail] = useWidth()
  const across = useMemo(() => layout(diagram, false), [diagram])
  // left to right when it fits without shrinking the words much; else top to bottom
  const vertical = phone || (avail > 0 && across.width * 0.85 > avail)
  const down = useMemo(() => layout(diagram, true), [diagram])
  const { boxes, routes, width, height } = vertical ? down : across
  const M = 8
  const anyOld = diagram.nodes.some(n => !n.new)
  return (
    <div className="df" ref={ref}>
      <div className={`df-scroll ${vertical ? 'v' : ''}`}>
        <svg className="df-svg" viewBox={`${-M} ${-M} ${width + 2 * M} ${height + 2 * M}`} width={Math.min(width + 2 * M, avail || width + 2 * M)} role="img" aria-label="How data moves through this change">
          <defs>
            <marker id="df-arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse"><path d="M0,0 L10,5 L0,10 z" className="df-head" /></marker>
          </defs>
          {routes.map((r, i) => <path key={i} className={`df-edge ${r.back ? 'back' : ''}`} d={pathOf(r, vertical)} markerEnd="url(#df-arrow)" />)}
          {boxes.map(b => {
            const go = b.n.step > 0 ? () => onStep(b.n.step) : undefined
            return (
              <g key={b.n.id} className={`df-node k-${b.n.kind} ${b.n.new ? 'new' : 'old'} ${go ? 'link' : ''}`} transform={`translate(${b.x},${b.y})`} onClick={go}
                role={go ? 'button' : undefined} tabIndex={go ? 0 : undefined} onKeyDown={go ? e => { if (e.key === 'Enter') go() } : undefined}>
                <title>{b.n.label}{go ? ` — step ${b.n.step}` : ''}</title>
                <rect width={b.w} height={b.h} rx={b.n.kind === 'store' ? 14 : 9} />
                <rect className="df-band" width={4} height={b.h - 12} x={0} y={6} rx={2} />
                <text x={PAD_X} y={15} className="df-kind">{(KIND_LABEL[b.n.kind] || b.n.kind).toUpperCase()}{b.n.new ? ' · NEW' : ''}</text>
                {b.lines.map((l, i) => <text key={i} x={PAD_X} y={34 + i * LINE} className="df-label">{l}</text>)}
                {go && <text x={b.w - 8} y={15} textAnchor="end" className="df-step">step {b.n.step}</text>}
              </g>
            )
          })}
          {routes.map((r, i) => { const at = labelAt(r, vertical); return r.label && <text key={i} x={at.x} y={at.y} dy="0.35em" textAnchor="middle" className="df-elabel">{r.label}</text> })}
        </svg>
      </div>
      <div className="df-legend muted small">
        <span><i className="df-key new" />added by this pull request</span>
        {anyOld && <span><i className="df-key old" />already there</span>}
        <span>Click a box to open its step.</span>
      </div>
    </div>
  )
}
