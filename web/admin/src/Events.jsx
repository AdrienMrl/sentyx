import { useEffect, useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import { api } from './api'

// Events browser (BETA: operator sees every user's events for debugging).
export default function Events() {
  const [events, setEvents] = useState(null)
  const [device, setDevice] = useState('')
  const [threat, setThreat] = useState('')
  const [q, setQ] = useState('')

  useEffect(() => { api.events().then((e) => setEvents(e.reverse())).catch(() => {}) }, [])

  const devices = useMemo(
    () => [...new Set((events || []).map((e) => e.device_id).filter(Boolean))].sort(),
    [events],
  )
  const shown = useMemo(() => (events || []).filter((e) =>
    (!device || e.device_id === device) &&
    (!threat || e.threat_level === threat) &&
    (!q || `${e.id} ${e.reason} ${e.city} ${e.camera}`.toLowerCase().includes(q.toLowerCase())),
  ), [events, device, threat, q])

  if (!events) return <div className="loading">LOADING EVENTS</div>

  return (
    <>
      <h1>Sentry events — {shown.length}/{events.length}</h1>
      <div className="filterrow">
        <select value={device} onChange={(e) => setDevice(e.target.value)}>
          <option value="">all devices</option>
          {devices.map((d) => <option key={d} value={d}>{d}</option>)}
        </select>
        <select value={threat} onChange={(e) => setThreat(e.target.value)}>
          <option value="">any threat</option>
          {['none', 'low', 'medium', 'high'].map((t) => <option key={t} value={t}>{t}</option>)}
        </select>
        <input placeholder="search id / reason / city / camera…" value={q}
          onChange={(e) => setQ(e.target.value)} />
      </div>
      {!shown.length ? <div className="empty">nothing matches</div> : (
        <div className="evlist">
          {shown.map((e) => (
            <Link key={e.id} className="ev" to={`/events/${encodeURIComponent(e.id)}`}>
              <img src={`/events/${encodeURIComponent(e.id)}/thumb`} alt="" loading="lazy"
                onError={(ev) => { ev.currentTarget.style.visibility = 'hidden' }} />
              <span className="what">
                <span className="line">
                  <span className="reason">{e.reason || 'unknown trigger'}</span>
                  <span className="meta">{e.city}{e.camera ? ` · ${e.camera}` : ''}{e.device_id ? ` · ${e.device_id}` : ''}</span>
                </span>
                <span className="id">{e.id} · {e.file_count} files · {e.analysis_state}</span>
              </span>
              <span className={`badge ${e.threat_level || 'state'}`}>{e.threat_level || e.analysis_state}</span>
            </Link>
          ))}
        </div>
      )}
    </>
  )
}
