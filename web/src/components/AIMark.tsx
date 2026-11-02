// The live mark on a pull request: a pulsing violet sparkle while a Pocket AI
// conversation about it works, amber while it waits for you.
import { useAIActive } from '../lib/ai-active'
import { Icon } from './icons'

export function AIMark({ refName, label = false }: { refName: string; label?: boolean }) {
  const a = useAIActive(refName)
  if (!a) return null
  const text = a.status === 'waiting' ? 'Pocket AI is waiting for you' : 'Pocket AI is working on it'
  return (
    <span className={`ai-mark ${a.status}`} title={text} aria-label={text} role="img">
      <Icon name="sparkles" size={12} />{label && <span>{a.status === 'waiting' ? 'Waiting for you' : 'Working…'}</span>}
    </span>
  )
}
