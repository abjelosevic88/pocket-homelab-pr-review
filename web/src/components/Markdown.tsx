// A comment's text as GitHub shows it: GitHub's own HTML when it sent it
// (body_html), else the Markdown rendered here — always sanitized, links
// open in a new tab, images load without a referrer.
import { useEffect, useMemo, useRef } from 'react'
import DOMPurify from 'dompurify'
import { marked } from 'marked'
import { hljs } from '../lib/highlight'

DOMPurify.addHook('afterSanitizeAttributes', node => {
  if (node.tagName === 'A' && node.getAttribute('href')) {
    node.setAttribute('target', '_blank')
    node.setAttribute('rel', 'noopener noreferrer')
  }
  // only a task list's checkbox, and only to show it
  if (node.tagName === 'INPUT') {
    if (node.getAttribute('type') !== 'checkbox') node.remove()
    else node.setAttribute('disabled', '')
  }
  if (node.tagName === 'IMG') {
    node.setAttribute('loading', 'lazy')
    node.setAttribute('referrerpolicy', 'no-referrer')
  }
})

const PURIFY = {
  ADD_TAGS: ['details', 'summary', 'picture', 'source'],
  ADD_ATTR: ['target', 'open', 'srcset', 'media', 'checked', 'disabled'],
  FORBID_TAGS: ['style', 'form', 'button', 'textarea', 'select', 'iframe', 'object', 'embed'],
  FORBID_ATTR: ['style'],
}

export function Markdown({ md, html, className = '' }: { md: string; html?: string; className?: string }) {
  const clean = useMemo(() => {
    const raw = html && html.trim() ? html : (marked.parse(md || '', { async: false, gfm: true, breaks: true }) as string)
    return DOMPurify.sanitize(raw, PURIFY) as string
  }, [md, html])
  // colour fenced code that names its language (GitHub's own HTML is coloured already)
  const ref = useRef<HTMLDivElement>(null)
  useEffect(() => {
    ref.current?.querySelectorAll<HTMLElement>('pre code[class*="language-"]').forEach(el => {
      const lang = /language-([\w-]+)/.exec(el.className)?.[1]
      if (lang && hljs.getLanguage(lang) && el.textContent && el.textContent.length < 50_000) {
        el.innerHTML = hljs.highlight(el.textContent, { language: lang, ignoreIllegals: true }).value
      }
    })
  }, [clean])
  return <div ref={ref} className={`md ${className}`} dangerouslySetInnerHTML={{ __html: clean }} />
}
