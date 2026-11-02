// Which pull requests have a Pocket AI conversation working or waiting right
// now (the person's own), shared by the lists, the sidebar and a pull
// request's page. Polled while something on screen asks and the tab shows.
import { useSyncExternalStore } from 'react'
import { api, V1 } from './api'

export type AIActive = { ref: string; status: 'running' | 'waiting'; thread_id: string; url: string }

let byRef = new Map<string, AIActive>()
let asList: AIActive[] = []
let connected = true
let timer = 0
const subs = new Set<() => void>()
const norm = (ref: string) => ref.toLowerCase()

async function poll() {
  window.clearTimeout(timer)
  if (!subs.size) return
  if (document.visibilityState === 'visible') {
    try {
      const r = await api.get<{ active: AIActive[]; connected: boolean }>(`${V1}/ai/active`)
      connected = r.connected
      // a pull request with several: waiting (needs you) wins over running
      const next = new Map<string, AIActive>()
      for (const a of r.active || []) { const k = norm(a.ref); if (!next.has(k) || a.status === 'waiting') next.set(k, a) }
      const same = next.size === byRef.size && [...next].every(([k, v]) => byRef.get(k)?.status === v.status && byRef.get(k)?.thread_id === v.thread_id)
      byRef = next
      if (!same) { asList = [...next.values()]; subs.forEach(f => f()) }
    } catch { /* keep the last answer */ }
  }
  // busy: every 5 s; quiet: every 20 s; Pocket AI not connected: rarely
  timer = window.setTimeout(poll, !connected ? 120000 : byRef.size ? 5000 : 20000)
}

function subscribe(f: () => void) {
  subs.add(f)
  if (subs.size === 1) { poll(); document.addEventListener('visibilitychange', onVisible) }
  return () => {
    subs.delete(f)
    if (!subs.size) { window.clearTimeout(timer); document.removeEventListener('visibilitychange', onVisible) }
  }
}
function onVisible() { if (document.visibilityState === 'visible') poll() }

/** Ask again now (after starting or sending a conversation). */
export function refreshAIActive() { if (subs.size) poll() }

/** The live Pocket AI state of one pull request, by owner/name#12. */
export function useAIActive(ref: string): AIActive | undefined {
  return useSyncExternalStore(subscribe, () => byRef.get(norm(ref)))
}

/** Every pull request with a conversation working or waiting. */
export function useAIActiveList(): AIActive[] {
  return useSyncExternalStore(subscribe, () => asList)
}
