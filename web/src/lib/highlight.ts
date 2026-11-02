// Syntax colours for a diff. Each side of a hunk (old: context and removed
// lines; new: context and added lines) is highlighted as one piece of code,
// so comments and strings over several lines come out right, then cut back
// into lines as tokens — no HTML is put into the page.
import hljs from 'highlight.js/lib/core'
import bash from 'highlight.js/lib/languages/bash'
import c from 'highlight.js/lib/languages/c'
import cpp from 'highlight.js/lib/languages/cpp'
import csharp from 'highlight.js/lib/languages/csharp'
import css from 'highlight.js/lib/languages/css'
import dockerfile from 'highlight.js/lib/languages/dockerfile'
import go from 'highlight.js/lib/languages/go'
import graphql from 'highlight.js/lib/languages/graphql'
import ini from 'highlight.js/lib/languages/ini'
import java from 'highlight.js/lib/languages/java'
import javascript from 'highlight.js/lib/languages/javascript'
import json from 'highlight.js/lib/languages/json'
import kotlin from 'highlight.js/lib/languages/kotlin'
import makefile from 'highlight.js/lib/languages/makefile'
import markdown from 'highlight.js/lib/languages/markdown'
import php from 'highlight.js/lib/languages/php'
import protobuf from 'highlight.js/lib/languages/protobuf'
import python from 'highlight.js/lib/languages/python'
import ruby from 'highlight.js/lib/languages/ruby'
import rust from 'highlight.js/lib/languages/rust'
import scss from 'highlight.js/lib/languages/scss'
import sql from 'highlight.js/lib/languages/sql'
import swift from 'highlight.js/lib/languages/swift'
import typescript from 'highlight.js/lib/languages/typescript'
import xml from 'highlight.js/lib/languages/xml'
import yaml from 'highlight.js/lib/languages/yaml'
import type { Line } from './types'

const LANGS = { bash, c, cpp, csharp, css, dockerfile, go, graphql, ini, java, javascript, json, kotlin, makefile, markdown, php, protobuf, python, ruby, rust, scss, sql, swift, typescript, xml, yaml }
for (const [name, def] of Object.entries(LANGS)) hljs.registerLanguage(name, def)

const BY_EXT: Record<string, string> = {
  ts: 'typescript', tsx: 'typescript', mts: 'typescript', cts: 'typescript',
  js: 'javascript', jsx: 'javascript', mjs: 'javascript', cjs: 'javascript',
  go: 'go', py: 'python', rb: 'ruby', rs: 'rust', java: 'java', kt: 'kotlin', kts: 'kotlin', php: 'php', swift: 'swift', cs: 'csharp',
  c: 'c', h: 'c', cc: 'cpp', cpp: 'cpp', hpp: 'cpp', sql: 'sql', graphql: 'graphql', gql: 'graphql', proto: 'protobuf',
  json: 'json', jsonc: 'json', yaml: 'yaml', yml: 'yaml', toml: 'ini', ini: 'ini', env: 'ini',
  css: 'css', scss: 'scss', html: 'xml', htm: 'xml', xml: 'xml', svg: 'xml', vue: 'xml',
  sh: 'bash', bash: 'bash', zsh: 'bash', md: 'markdown', mdx: 'markdown', mk: 'makefile',
}
const BY_NAME: Record<string, string> = { dockerfile: 'dockerfile', makefile: 'makefile', '.env': 'ini', '.gitignore': 'bash' }

/** The language of a file by its name, or '' when there are no colours for it. */
export function languageOf(path: string): string {
  const name = (path.split('/').pop() || '').toLowerCase()
  if (BY_NAME[name]) return BY_NAME[name]
  if (name.startsWith('dockerfile')) return 'dockerfile'
  const ext = name.includes('.') ? name.split('.').pop()! : ''
  return BY_EXT[ext] || ''
}

export interface Token { s: string; c: string } // text and its classes ('' = plain)

// past this a hunk is shown plain: colouring would make the page slow
const MAX_CHARS = 150_000

const ENT: Record<string, string> = { amp: '&', lt: '<', gt: '>', quot: '"', '#x27': "'", '#39': "'" }
const unescape = (s: string) => s.replace(/&(amp|lt|gt|quot|#x27|#39);/g, (_, e) => ENT[e])

/** hljs's HTML (only <span class>, </span> and escaped text) into tokens per line. */
function toLines(html: string): Token[][] {
  const lines: Token[][] = [[]]
  const stack: string[] = []
  const re = /<span class="([^"]*)">|<\/span>|([^<]+)/g
  let m: RegExpExecArray | null
  while ((m = re.exec(html))) {
    if (m[1] !== undefined) stack.push(m[1])
    else if (m[0] === '</span>') stack.pop()
    else {
      const cls = stack.join(' ')
      unescape(m[2]).split('\n').forEach((part, i) => {
        if (i > 0) lines.push([])
        if (part) lines[lines.length - 1].push({ s: part, c: cls })
      })
    }
  }
  return lines
}

function side(lines: Line[], keep: (l: Line) => boolean, lang: string, out: Map<number, Token[]>) {
  const idx: number[] = []
  const text: string[] = []
  lines.forEach((l, i) => { if (keep(l)) { idx.push(i); text.push(l.s) } })
  if (idx.length === 0) return
  const toks = toLines(hljs.highlight(text.join('\n'), { language: lang, ignoreIllegals: true }).value)
  idx.forEach((li, k) => { if (!out.has(li)) out.set(li, toks[k] || []) })
}

/** Tokens for each line index of a hunk; empty when the file has no language or the hunk is too big. */
export function highlightHunk(lines: Line[], lang: string): Map<number, Token[]> {
  const out = new Map<number, Token[]>()
  if (!lang || lines.reduce((n, l) => n + l.s.length + 1, 0) > MAX_CHARS) return out
  try {
    side(lines, l => l.t === ' ' || l.t === '+', lang, out)
    side(lines, l => l.t === ' ' || l.t === '-', lang, out) // context is already coloured from the new side
  } catch {
    out.clear()
  }
  return out
}

/** Tokens with the [from, to) part cut out as marked (the changed words of a line). */
export function withMark(toks: Token[], mk?: [number, number]): (Token & { mark?: boolean })[] {
  if (!mk) return toks
  const out: (Token & { mark?: boolean })[] = []
  let at = 0
  for (const t of toks) {
    const end = at + t.s.length
    const cuts = [at, Math.min(Math.max(mk[0], at), end), Math.min(Math.max(mk[1], at), end), end]
    for (let k = 0; k < 3; k++) {
      if (cuts[k + 1] > cuts[k]) out.push({ s: t.s.slice(cuts[k] - at, cuts[k + 1] - at), c: t.c, mark: k === 1 })
    }
    at = end
  }
  return out
}
