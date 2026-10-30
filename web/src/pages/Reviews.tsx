// The reviews you sent from here, newest first.
import { Link } from 'react-router-dom'
import { api, V1 } from '../lib/api'
import type { Review } from '../lib/types'
import { fmtDate } from '../lib/format'
import { Icon } from '../components/icons'
import { Empty, Loading, useAsync } from '../components/ui'

const EVENT: Record<string, [string, string, string]> = { APPROVE: ['Approved', 'ok', 'check-circle'], REQUEST_CHANGES: ['Asked for changes', 'warn', 'alert'], COMMENT: ['Commented', '', 'message'] }

export default function Reviews() {
  const { data, loading } = useAsync(() => api.get<Review[]>(`${V1}/reviews`), [])
  if (loading && !data) return <Loading />
  const list = data || []
  let last = ''
  return (
    <div className="stack loose">
      <div className="page-head"><div className="grow"><h2 className="page-title">Reviews you sent</h2><p className="muted small">From this app, newest first.</p></div></div>
      {list.length === 0 ? <Empty icon="message" title="No reviews sent yet">Finish a review on a pull request and it shows up here.</Empty> : (
        <ul className="pr-list">
          {list.map(r => {
            const head = r.day !== last ? <li className="day-h" key={'d' + r.day}>{fmtDate(r.day)}</li> : null
            last = r.day
            const [label, cls, icon] = EVENT[r.event] || [r.event, '', 'message']
            return [head, (
              <li key={r.id} className="pr-row">
                <Link to={`/pr/${r.pull_id}`} className="pr-main">
                  <span className={`pr-state rev ${cls}`}><Icon name={icon} size={17} /></span>
                  <span className="pr-body">
                    <span className="pr-title">{r.title}</span>
                    <span className="pr-meta muted small"><span>{r.repo}#{r.number}</span><span className={`badge ${cls}`}>{label}</span>{r.comments > 0 && <span>{r.comments} comment{r.comments === 1 ? '' : 's'}</span>}</span>
                    {r.body && <span className="rev-body small">{r.body}</span>}
                  </span>
                </Link>
                {r.gh_url && <a className="btn ghost icon-only" href={r.gh_url} target="_blank" rel="noreferrer" aria-label="On GitHub" title="On GitHub"><Icon name="external" size={15} /></a>}
              </li>
            )]
          })}
        </ul>
      )}
    </div>
  )
}
