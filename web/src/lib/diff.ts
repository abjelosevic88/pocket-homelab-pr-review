import type { Hunk, Line } from './types'

/** Reads a unified-diff patch (GitHub's per-file patch, hunks only). */
export function parsePatch(patch: string): Hunk[] {
  const out: Hunk[] = []
  let cur: Hunk | null = null
  let o = 0
  let n = 0
  for (const raw of patch.split('\n')) {
    const m = /^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@ ?(.*)$/.exec(raw)
    if (m) {
      cur = { header: m[5].trim(), old_start: +m[1], old_lines: m[2] ? +m[2] : 1, new_start: +m[3], new_lines: m[4] ? +m[4] : 1, lines: [], hash: '' }
      out.push(cur)
      o = cur.old_start
      n = cur.new_start
      continue
    }
    if (!cur || raw === '') continue
    const c = raw[0]
    if (c === '+') cur.lines.push({ t: '+', n: n++, s: raw.slice(1) })
    else if (c === '-') cur.lines.push({ t: '-', o: o++, s: raw.slice(1) })
    else if (c === '\\') cur.lines.push({ t: '\\', s: raw.slice(1).trim() })
    else cur.lines.push({ t: ' ', o: o++, n: n++, s: c === ' ' ? raw.slice(1) : raw })
  }
  return out
}

/** The changed middle of a pair of lines (common prefix and suffix left plain). */
export function changedRange(a: string, b: string): [number, number, number, number] | null {
  if (a === b) return null
  let p = 0
  while (p < a.length && p < b.length && a[p] === b[p]) p++
  let s = 0
  while (s < a.length - p && s < b.length - p && a[a.length - 1 - s] === b[b.length - 1 - s]) s++
  // nothing in common: highlighting the whole line says nothing
  if (p + s < 3) return null
  return [p, a.length - s, p, b.length - s]
}

/** For each line index, the [from, to) of its changed part when it pairs with a line on the other side. */
export function wordMarks(lines: Line[]): Map<number, [number, number]> {
  const marks = new Map<number, [number, number]>()
  let i = 0
  while (i < lines.length) {
    if (lines[i].t !== '-') { i++; continue }
    const del: number[] = []
    while (i < lines.length && lines[i].t === '-') del.push(i++)
    const add: number[] = []
    while (i < lines.length && lines[i].t === '+') add.push(i++)
    if (del.length !== add.length || del.length > 8) continue
    for (let k = 0; k < del.length; k++) {
      const r = changedRange(lines[del[k]].s, lines[add[k]].s)
      if (r) {
        marks.set(del[k], [r[0], r[1]])
        marks.set(add[k], [r[2], r[3]])
      }
    }
  }
  return marks
}

/** A short commit id. */
export const short = (sha?: string) => (sha || '').slice(0, 7)
