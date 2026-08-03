import { useEffect, useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import { api } from './api'
import { fmtBytes } from './format'

// Clip player with a selector over every uploaded video of the event; the
// analyzer's pick (if any) is the default.
function ClipPanel({ id, ev }) {
  const videos = (ev.files || []).filter((f) => f.name.toLowerCase().endsWith('.mp4'))
  const [file, setFile] = useState(ev.analyzed_clip || videos[0]?.name || '')
  if (!file) return (
    <div className="panel">
      <h2>Clip</h2>
      <div className="empty">no video uploaded — analysis {ev.analysis_state}</div>
    </div>
  )
  return (
    <div className="panel">
      <h2>Clip — {file}{file === ev.analyzed_clip ? ' (analyzed)' : ''}</h2>
      <video key={file} controls preload="metadata"
        src={`/events/${encodeURIComponent(id)}/clip?file=${encodeURIComponent(file)}`}
        poster={`/events/${encodeURIComponent(id)}/thumb`} />
      {videos.length > 1 && (
        <div className="filterrow" style={{ marginTop: 10, marginBottom: 0 }}>
          <select value={file} onChange={(e) => setFile(e.target.value)}>
            {videos.map((v) => (
              <option key={v.name} value={v.name}>
                {v.name}{v.name === ev.analyzed_clip ? ' (analyzed)' : ''}
              </option>
            ))}
          </select>
        </div>
      )}
    </div>
  )
}

const pretty = (raw) => {
  try { return JSON.stringify(JSON.parse(raw), null, 2) } catch { return raw }
}

// Full debugging view of one event: clip playback, selection, verdict, files.
export default function EventDetail() {
  const { id } = useParams()
  const [ev, setEv] = useState(null)

  useEffect(() => { api.event(id).then(setEv).catch(() => setEv('missing')) }, [id])

  if (ev === 'missing') return <div className="empty">unknown event {id}</div>
  if (!ev) return <div className="loading">LOADING EVENT</div>

  return (
    <>
      <div className="crumbs"><Link to="/events">events</Link> / {id}</div>
      <h1>{ev.reason || 'event'} — {ev.city || 'unknown location'}</h1>
      <div className="evgrid">
        <div style={{ display: 'grid', gap: 18 }}>
          <ClipPanel id={id} ev={ev} />
          <div className="panel">
            <h2>Files received ({ev.files?.length ?? 0})</h2>
            <table className="filetable">
              <thead><tr><th>name</th><th>size</th><th>received</th></tr></thead>
              <tbody>
                {(ev.files || []).map((f) => (
                  <tr key={f.name}>
                    <td>{f.name}</td>
                    <td>{fmtBytes(f.size)}</td>
                    <td>{new Date(f.received_at).toLocaleTimeString()}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
        <div style={{ display: 'grid', gap: 18 }}>
          <div className="panel">
            <h2>Event</h2>
            <div className="kv">
              <b>threat</b><span className={`badge ${ev.threat_level || 'state'}`}>{ev.threat_level || '—'}</span>
              <b>device</b><span>{ev.device_id ? <Link to={`/devices/${encodeURIComponent(ev.device_id)}`}>{ev.device_id}</Link> : '—'}</span>
              <b>trigger camera</b><span>{ev.camera || '—'}</span>
              <b>clip picked</b><span>{ev.analyzed_clip || '—'}</span>
              <b>event time</b><span>{ev.event_ts || '—'}</span>
              <b>first seen</b><span>{new Date(ev.first_seen).toLocaleString()}</span>
              <b>completed</b><span>{ev.completed_at ? new Date(ev.completed_at).toLocaleString() : '—'}</span>
              <b>state</b><span>{ev.state} · gen {ev.generation} · analysis {ev.analysis_state}</span>
              {ev.analysis_error && <><b>analysis error</b><span>{ev.analysis_error}</span></>}
            </div>
          </div>
          {ev.usage && (
            <div className="panel">
              <h2>Analysis cost</h2>
              <div className="kv">
                <b>model</b><span>{ev.usage.model}</span>
                <b>prompt tokens</b><span>{ev.usage.prompt_tokens.toLocaleString()}</span>
                <b>output tokens</b><span>{ev.usage.output_tokens.toLocaleString()}</span>
                <b>total</b><span>{ev.usage.total_tokens.toLocaleString()}</span>
              </div>
            </div>
          )}
          {ev.analysis_json && (
            <div className="panel">
              <h2>Gemini verdict (raw)</h2>
              <pre className="json">{pretty(ev.analysis_json)}</pre>
            </div>
          )}
        </div>
      </div>
    </>
  )
}
