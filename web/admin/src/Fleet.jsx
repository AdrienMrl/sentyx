import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { api } from './api'
import ErrBox from './ErrBox'
import { fmtAgo, fmtBytes, fmtUptime, pctFree } from './format'

// In-flight OTA states, in the order the updater reports them. Anything here
// means "working on it"; anything else is a terminal outcome.
const RUNNING = ['offered', 'downloading', 'waiting-safe', 'installing', 'rebooting']

// UpdateBadge renders one unit's firmware-update situation. Renders nothing
// when no release has ever been published — there is no "up to date" to claim
// against an empty release list.
function UpdateBadge({ update }) {
  if (!update) return null
  const { available, state, targetVersion, latestVersion, progressPct } = update
  let cls = 'ok'
  let text = `up to date · ${latestVersion}`
  if (state === 'failed' || state === 'rolled-back') {
    cls = 'alarm'
    text = `${state} · ${targetVersion || latestVersion}`
  } else if (RUNNING.includes(state)) {
    cls = 'busy'
    text = `${state}${progressPct ? ` ${progressPct}%` : ''} → ${targetVersion || latestVersion}`
  } else if (available) {
    cls = 'pending'
    text = `update available → ${targetVersion}`
  }
  return <span className={`metric update ${cls}`}><b>firmware</b>{text}</span>
}

// Fleet view: one row per registered Pi, refreshed every 15 s.
export default function Fleet() {
  const [devices, setDevices] = useState(null)
  const [err, setErr] = useState('')
  const [tick, setTick] = useState(0)
  useEffect(() => {
    let alive = true
    const load = () => api.devices()
      .then((d) => { if (alive) { setDevices(d.devices); setErr('') } })
      .catch((e) => alive && setErr(e.message))
    load()
    const t = setInterval(load, 15_000)
    return () => { alive = false; clearInterval(t) }
  }, [tick])

  if (!devices && err) return <ErrBox what="fleet" err={err} onRetry={() => setTick((t) => t + 1)} />
  if (!devices) return <div className="loading">LOADING FLEET</div>
  if (!devices.length) return <div className="empty">no devices registered yet</div>

  return (
    <>
      <h1>Fleet — {devices.filter((d) => d.online).length}/{devices.length} online</h1>
      <div className="fleet">
        {devices.map((d) => {
          const hb = d.status || {}
          const free = pctFree(hb)
          return (
            <Link key={d.deviceId} to={`/devices/${encodeURIComponent(d.deviceId)}`}
              className={`unit ${d.online ? 'up' : 'down'}`}>
              <span className={`dot ${d.online ? 'up' : 'down'}`} />
              <span className="name">
                {d.name}
                <small>{d.deviceId}{d.ownerEmail ? ` · ${d.ownerEmail}` : ''}</small>
              </span>
              <span className={`metric ${d.online ? '' : 'alarm'}`}>
                <b>status</b>{d.online ? 'online' : `offline · ${fmtAgo(d.lastSeenMs)}`}
              </span>
              <span className={`metric ${hb.cpuTempC >= 75 ? 'alarm' : ''}`}>
                <b>cpu temp</b>{hb.cpuTempC != null ? `${hb.cpuTempC.toFixed(1)}°C` : '—'}
              </span>
              <span className={`metric ${free != null && free < 10 ? 'alarm' : ''}`}>
                <b>free</b>{free != null ? `${free}% · ${fmtBytes(hb.storageFreeBytes)}` : '—'}
              </span>
              <span className="metric">
                <b>backlog / rec</b>{hb.uploadBacklog ?? '—'} · {hb.recordingNow ? 'REC' : 'idle'}
              </span>
              <span className="metric">
                <b>agent</b>{hb.agentVersion || '—'} · up {fmtUptime(hb.uptimeSec)}
              </span>
              <UpdateBadge update={d.update} />
            </Link>
          )
        })}
      </div>
    </>
  )
}
