// How data moves through what a pull request adds: boxes in layers, arrows
// with what moves along them. Left to right; top to bottom on a phone.
import { useEffect, useMemo, useRef, useState } from 'react'
import type { Diagram, DNode } from '../lib/types'

const KIND_LABEL: Record<string, string> = { actor: 'person', event: 'event', code: 'code', store: 'stored', api: 'API', ui: 'screen', external: 'other system' }
const LINE = 16
const PAD_X = 12

/** Words into lines of at most CHARS characters; a long name breaks after _ / . - */
function wrap(s: string, CHARS: number): string[] {
  const words = s.split(/\s+/).flatMap(w => w.length <= CHARS ? [w] : (w.match(/[^_/.-]*[_/.-]?/g) || [w]).filter(Boolean).map((p, i, a) => i < a.length - 1 ? p + '\u0001' : p))
  const out: string[] = []
  let cur = ''
  for (const w of words) {
    const glue = cur.endsWith('\u0001') ? '' : ' '
    if (cur && (cur + glue + w).replace(/\u0001/g, '').length > CHARS) { out.push(cur); cur = w } else cur = cur ? cur + glue + w : w
  }
  if (cur) out.push(cur)
  const lines = out.map(l => l.replace(/\u0001/g, ''))
  return lines.length > 3 ? [...lines.slice(0, 2), lines.slice(2).join('').slice(0, CHARS - 1) + '…'] : lines
}

interface Box { n: DNode; lines: string[]; rank: number; x: number; y: number; w: number; h: number; virtual?: boolean }
type Pt = { x: number; y: number }
interface Seg { a: Pt; b: Pt; vert: boolean; sign: number; side?: number } // sign: which way the curve leaves a; side: x of a bracket around the column
interface Route { label: string; back: boolean; segs: Seg[] }

const W = 156
const GAP_N = 18 // between boxes of one layer
const GAP_L = 92 // between layers in a row (room for the arrow's words)
const GAP_ROW = 64 // between rows, and between layers top to bottom

/** How many layers fit in a row of this width without shrinking the words much. */
export function perRow(avail: number) { return Math.max(1, Math.floor((avail / 0.9 + GAP_L) / (W + GAP_L))) }

/**
 * Boxes in layers by the longest path to them. cols = layers per row: they
 * run left to right, then the next row right to left (a snake), so a long
 * chain reads like a page; cols = 0 puts each layer in a row of its own
 * (top to bottom, for a phone).
 */
function layout(d: Diagram, cols: number) {
  const down = cols === 0
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

  // an arrow across several layers passes through an invisible point in each
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
  const adj = new Map<string, string[]>()
  for (const c of chains) for (let k = 1; k < c.length; k++) if (rank.get(c[k])! - rank.get(c[k - 1])! === 1) adj.set(c[k], [...(adj.get(c[k]) || []), c[k - 1]])

  const layers: DNode[][] = []
  for (const n of [...d.nodes, ...vnodes]) (layers[rank.get(n.id)!] ||= []).push(n)
  // order inside a layer by where its inputs sit
  const pos = new Map<string, number>()
  layers.forEach(l => l.forEach((n, i) => pos.set(n.id, i)))
  for (let sweep = 0; sweep < 3; sweep++) {
    for (const l of layers.slice(1)) {
      const bary = (n: DNode) => { const ps = (adj.get(n.id) || []).map(p => pos.get(p)).filter((x): x is number => x !== undefined); return ps.length ? ps.reduce((a, b) => a + b, 0) / ps.length : pos.get(n.id)! }
      const by = new Map(l.map(n => [n.id, bary(n)]))
      l.sort((a, b) => by.get(a.id)! - by.get(b.id)!).forEach((n, i) => pos.set(n.id, i))
    }
  }

  const VW = 12 // an invisible point's room
  const sizes = layers.map(l => l.map(n => {
    const virtual = n.kind === 'virtual'
    const lines = virtual ? [] : wrap(n.label, 18)
    return { n, lines, virtual, w: virtual ? (down ? VW : W) : W, h: virtual ? (down ? 0 : VW) : 30 + lines.length * LINE }
  }))
  const boxes = new Map<string, Box>()
  const place = (r: number, x0: number, y0: number, along: number) => {
    // a layer's boxes side by side (top to bottom) or stacked (across), centered on `along`
    const l = sizes[r]
    const total = l.reduce((a, b) => a + (down ? b.w : b.h), 0) + (l.length - 1) * GAP_N
    let at = (along - total) / 2
    const thick = down ? Math.max(...l.map(b => b.h), 30) : W
    for (const b of l) {
      const box: Box = { n: b.n, lines: b.lines, rank: r, w: b.w, h: b.h, x: 0, y: 0, virtual: b.virtual }
      if (down) { box.x = x0 + at; box.y = y0 + (thick - b.h) / 2; at += b.w + GAP_N } else { box.x = x0; box.y = y0 + at; at += b.h + GAP_N }
      boxes.set(b.n.id, box)
    }
  }
  const lengthOf = (r: number) => sizes[r].reduce((a, b) => a + (down ? b.w : b.h), 0) + (sizes[r].length - 1) * GAP_N
  const rowOf = (r: number) => down ? r : Math.floor(r / cols)
  const dirOf = (r: number) => down ? 1 : rowOf(r) % 2 === 0 ? 1 : -1
  let width = 0, height = 0
  if (down) {
    width = Math.max(...layers.map((_, r) => lengthOf(r)))
    let y = 0
    layers.forEach((_, r) => { const thick = Math.max(...sizes[r].map(b => b.h), 30); place(r, 0, y, width); y += thick + GAP_ROW })
    height = y - GAP_ROW
  } else {
    const n = Math.min(cols, layers.length)
    width = n * W + (n - 1) * GAP_L
    let y = 0
    for (let row = 0; row * cols < layers.length; row++) {
      const rs = layers.map((_, r) => r).filter(r => rowOf(r) === row)
      const tall = Math.max(...rs.map(lengthOf))
      for (const r of rs) {
        const col = r % cols
        const slot = dirOf(r) === 1 ? col : n - 1 - col
        place(r, slot * (W + GAP_L), y, tall)
      }
      y += tall + GAP_ROW
    }
    height = y - GAP_ROW
  }

  // each piece of an arrow: across within a row, down between rows
  const order = new Map<string, [number, number]>() // place in its layer, boxes in the layer
  sizes.forEach(l => l.forEach((b, i) => order.set(b.n.id, [i, l.length])))
  const segOf = (u: Box, v: Box): Seg => {
    const back = v.rank <= u.rank
    const [ui, un] = order.get(u.n.id)!, [vi] = order.get(v.n.id)!
    if (!down && !back && rowOf(u.rank) !== rowOf(v.rank) && (ui < un - 1 || vi > 0)) {
      // a box under u or over v: go around the outside of the column instead of through it
      const right = dirOf(u.rank) === 1
      const x = right ? u.x + u.w : u.x
      return { a: { x, y: u.y + u.h / 2 }, b: { x: right ? v.x + v.w : v.x, y: v.y + v.h / 2 }, vert: true, sign: 1, side: x + (right ? 30 : -30) }
    }
    if (down || rowOf(u.rank) !== rowOf(v.rank)) {
      const up = back && rowOf(v.rank) <= rowOf(u.rank)
      return { a: { x: u.x + u.w / 2, y: up ? u.y : u.y + u.h }, b: { x: v.x + v.w / 2, y: up ? v.y + v.h : v.y }, vert: true, sign: up ? -1 : 1 }
    }
    const right = v.x > u.x
    return { a: { x: right ? u.x + u.w : u.x, y: u.y + u.h / 2 }, b: { x: right ? v.x : v.x + v.w, y: v.y + v.h / 2 }, vert: false, sign: right ? 1 : -1 }
  }
  const routes: Route[] = chains.map((c, i) => {
    const bs = c.map(id => boxes.get(id)!)
    const back = bs[bs.length - 1].rank <= bs[0].rank
    const segs: Seg[] = []
    for (let k = 1; k < bs.length; k++) segs.push(segOf(bs[k - 1], bs[k]))
    return { label: edges[i].label, back, segs }
  })
  const sides = routes.flatMap(r => r.segs.map(s => s.side).filter((x): x is number => x !== undefined))
  const minX = Math.min(0, ...sides.map(x => x - 40)), maxX = Math.max(width, ...sides.map(x => x + 40))
  return { boxes: [...boxes.values()].filter(b => !b.virtual), routes, minX, width: maxX - minX, height }
}

function ctrl(s: Seg): [Pt, Pt] {
  const { a, b } = s
  const k = Math.max(24, Math.abs(s.vert ? b.y - a.y : b.x - a.x) / 2) * s.sign
  return s.vert ? [{ x: a.x, y: a.y + k }, { x: b.x, y: b.y - k }] : [{ x: a.x + k, y: a.y }, { x: b.x - k, y: b.y }]
}

/** A smooth path through the pieces; a line joins pieces that meet at a passed point. */
function pathOf(r: Route) {
  let d = ''
  r.segs.forEach((s, i) => {
    if (s.side !== undefined) {
      const x = s.side, k = Math.sign(x - s.a.x) * 12
      d += `${i === 0 ? 'M' : ' L'}${s.a.x},${s.a.y} L${x - k},${s.a.y} Q${x},${s.a.y} ${x},${s.a.y + 12} L${x},${s.b.y - 12} Q${x},${s.b.y} ${x - k},${s.b.y} L${s.b.x},${s.b.y}`
      return
    }
    const [c1, c2] = ctrl(s)
    d += `${i === 0 ? `M${s.a.x},${s.a.y}` : ` L${s.a.x},${s.a.y}`} C${c1.x},${c1.y} ${c2.x},${c2.y} ${s.b.x},${s.b.y}`
  })
  return d
}

/** Where an arrow's words go: the middle of its first piece. */
function labelAt(r: Route): Pt {
  const s = r.segs[0]
  if (s.side !== undefined) return { x: s.side, y: (s.a.y + s.b.y) / 2 }
  const [c1, c2] = ctrl(s)
  return { x: (s.a.x + 3 * c1.x + 3 * c2.x + s.b.x) / 8, y: (s.a.y + 3 * c1.y + 3 * c2.y + s.b.y) / 8 }
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
  // as many layers per row as fit; a phone, or room for only one, goes top to bottom
  const cols = phone || avail === 0 ? 0 : perRow(avail)
  const vertical = cols < 2
  const { boxes, routes, minX, width, height } = useMemo(() => layout(diagram, vertical ? 0 : cols), [diagram, vertical, cols])
  const M = 8
  const anyOld = diagram.nodes.some(n => !n.new)
  return (
    <div className="df" ref={ref}>
      <div className={`df-scroll ${vertical ? 'v' : ''}`}>
        <svg className="df-svg" viewBox={`${minX - M} ${-M} ${width + 2 * M} ${height + 2 * M}`} width={Math.min(width + 2 * M, avail || width + 2 * M)} role="img" aria-label="How data moves through this change">
          <defs>
            <marker id="df-arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse"><path d="M0,0 L10,5 L0,10 z" className="df-head" /></marker>
          </defs>
          {routes.map((r, i) => <path key={i} className={`df-edge ${r.back ? 'back' : ''}`} d={pathOf(r)} markerEnd="url(#df-arrow)" />)}
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
          {routes.map((r, i) => { const at = labelAt(r); return r.label && <text key={i} x={at.x} y={at.y} dy="0.35em" textAnchor="middle" className="df-elabel">{r.label}</text> })}
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
