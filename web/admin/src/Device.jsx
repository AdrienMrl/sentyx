import { useEffect, useMemo, useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import { api } from './api'
import Chart from './Chart'
import ErrBox from './ErrBox'
import Firmware from './Firmware'
import { fmtAgo, fmtBytes, fmtDur, fmtUptime, pctFree } from './format'

const RANGES = [
  { label: '6h', ms: 6 * 36e5 },
  { label: '24h', ms: 24 * 36e5 },
  { label: '7d', ms: 7 * 24 * 36e5 },
  { label: '30d', ms: 30 * 24 * 36e5 },
]

export default function Device() {
  const { deviceId } = useParams()
  const [device, setDevice] = useState(null)
  const [err, setErr] = useState('')
  const [tick, setTick] = useState(0)
  const [range, setRange] = useState(RANGES[1])
  const [window_, setWindow] = useState(null) // {sinceMs, untilMs, samples}
  const [events, setEvents] = useState([])
  const [blackbox, setBlackbox] = useState(null) // [{atMs,type,detail}] in range

  useEffect(() => {
    setErr('')
    api.devices()
      .then((d) => setDevice(d.devices.find((x) => x.deviceId === deviceId) || 'missing'))
      .catch((e) => setErr(e.message))
    api.events().then((evs) => setEvents(evs.filter((e) => e.device_id === deviceId).reverse().slice(0, 8))).catch(() => {})
  }, [deviceId, tick])

  useEffect(() => {
    let alive = true
    const load = () => {
      const untilMs = Date.now()
      const sinceMs = untilMs - range.ms
      api.heartbeats(deviceId, sinceMs)
        .then((h) => alive && setWindow({ sinceMs, untilMs, samples: h.samples || [] }))
        .catch((e) => alive && setErr(e.message))
      api.blackbox(deviceId, sinceMs)
        .then((b) => alive && setBlackbox(b.events || []))
        .catch(() => alive && setBlackbox([]))
    }
    setWindow(null)
    load()
    const t = setInterval(load, 30_000)
    return () => { alive = false; clearInterval(t) }
  }, [deviceId, range, tick])

  const series = useMemo(() => {
    if (!window_) return null
    const pick = (fn) =>
      window_.samples
        .map((s) => ({ t: s.atMs, v: fn(s.heartbeat) }))
        .filter((p) => p.v != null && Number.isFinite(p.v))
    return {
      temp: pick((h) => (h.cpuTempC != null ? +h.cpuTempC.toFixed(1) : null)),
      free: pick((h) => (h.storageFreeBytes != null ? +(h.storageFreeBytes / 1e9).toFixed(2) : null)),
      rssi: pick((h) => h.wifiRssiDbm),
      backlog: pick((h) => h.uploadBacklog),
    }
  }, [window_])

  // Blackbox rows, newest first, each classified for display. A boot whose
  // preceding recorded event is not a clean agent-stop was a power cut — on
  // this hardware the car cutting glovebox power is the expected sleep path,
  // but it is exactly the transition that can cost a Sentry recording, so it
  // is surfaced loudest.
  const bbRows = useMemo(() => {
    if (!blackbox) return null
    const asc = [...blackbox].sort((a, b) => a.atMs - b.atMs)
    return asc.map((ev, i) => {
      const prev = i > 0 ? asc[i - 1] : null
      let label = `${ev.type} ${ev.detail}`, cls = 'state', note = ''
      if (ev.type === 'udc') {
        if (ev.detail === 'configured') { label = 'MOUNTED'; cls = 'good'; note = 'car mounted the drive' }
        else if (ev.detail === 'suspended') { label = 'USB SUSPENDED'; cls = 'warn'; note = 'host put the bus to sleep' }
        else if (ev.detail === 'not attached') { label = 'DISMOUNTED'; cls = 'alarm'; note = 'car dropped the drive while the Pi stayed up' }
        else { label = `UDC ${ev.detail.toUpperCase()}`; cls = 'alarm' }
      } else if (ev.type === 'writes') {
        if (ev.detail === 'active') { label = 'WRITING'; cls = 'good'; note = 'car is writing to the drive' }
        else { label = 'WRITES STALLED'; cls = 'warn'; note = 'mounted but no writes — buffering or silent failure' }
      } else if (ev.type === 'agent-start') {
        label = 'BOOT'; cls = 'state'; note = `agent ${ev.detail} came up`
        if (prev && prev.type !== 'agent-stop') {
          label = 'POWER CUT → BOOT'; cls = 'alarm'
          note = `no clean stop before this boot; dark for ${fmtDur(ev.atMs - prev.atMs)}`
        } else if (prev) {
          note += ` after a clean stop (dark ${fmtDur(ev.atMs - prev.atMs)})`
        }
      } else if (ev.type === 'agent-stop') {
        label = 'CLEAN STOP'; cls = 'state'; note = 'agent shut down in an orderly way'
      }
      return { ...ev, label, cls, note }
    }).reverse()
  }, [blackbox])

  if (device === 'missing') return <div className="empty">unknown device {deviceId}</div>
  if (!device && err) return <ErrBox what="device" err={err} onRetry={() => setTick((t) => t + 1)} />
  if (!device) return <div className="loading">LOADING</div>
  const hb = device.status || {}
  const free = pctFree(hb)

  return (
    <>
      <div className="crumbs"><Link to="/">fleet</Link> / {deviceId}</div>
      <h1>{device.name} — {device.online ? 'online' : `offline, last seen ${fmtAgo(device.lastSeenMs)}`}</h1>

      <div className="statgrid">
        <div className={`stat ${device.online ? 'good' : 'alarm'}`}><b>status</b><span>{device.online ? 'ONLINE' : 'OFFLINE'}</span></div>
        <div className={`stat ${hb.cpuTempC >= 75 ? 'alarm' : ''}`}><b>cpu temp</b><span>{hb.cpuTempC != null ? `${hb.cpuTempC.toFixed(1)}°C` : '—'}</span></div>
        <div className={`stat ${free != null && free < 10 ? 'alarm' : ''}`}><b>storage free</b><span>{free != null ? `${free}%` : '—'}</span></div>
        <div className="stat"><b>free bytes</b><span>{fmtBytes(hb.storageFreeBytes)}</span></div>
        <div className="stat"><b>uptime</b><span>{fmtUptime(hb.uptimeSec)}</span></div>
        <div className={`stat ${hb.underVoltageNow ? 'alarm' : ''}`}><b>under-voltage</b><span>{hb.underVoltageNow ? 'NOW' : hb.underVoltageEver ? 'ever' : 'never'}</span></div>
        <div className="stat"><b>wifi</b><span>{hb.wifiSsid ? `${hb.wifiSsid} ${hb.wifiRssiDbm ?? ''}dBm` : '—'}</span></div>
        <div className="stat"><b>backlog</b><span>{hb.uploadBacklog ?? '—'}</span></div>
        <div className={`stat ${hb.recordingNow ? 'good' : ''}`}><b>recording</b><span>{hb.recordingNow ? 'REC ●' : 'idle'}</span></div>
        <div className="stat"><b>agent</b><span>{hb.agentVersion || '—'}</span></div>
      </div>

      <Firmware device={device} />

      <div className="rangebar">
        {RANGES.map((r) => (
          <button key={r.label} className={r === range ? 'on' : ''} onClick={() => setRange(r)}>
            {r.label}
          </button>
        ))}
      </div>

      {!series ? (err
        ? <ErrBox what="metrics" err={err} onRetry={() => setTick((t) => t + 1)} />
        : <div className="loading">LOADING METRICS</div>) : (
        <div className="charts">
          <div className="chartcard">
            <h2>SoC temperature <i>°C — red bands = no samples (offline)</i></h2>
            <Chart points={series.temp} unit="°C" color="var(--series-temp)"
              sinceMs={window_.sinceMs} untilMs={window_.untilMs} />
          </div>
          <div className="chartcard">
            <h2>Storage free <i>GB</i></h2>
            <Chart points={series.free} unit=" GB" color="var(--series-disk)"
              sinceMs={window_.sinceMs} untilMs={window_.untilMs} />
          </div>
          <div className="chartcard">
            <h2>Wi-Fi signal <i>dBm</i></h2>
            <Chart points={series.rssi} unit=" dBm" color="var(--series-rssi)"
              sinceMs={window_.sinceMs} untilMs={window_.untilMs} />
          </div>
          <div className="chartcard">
            <h2>Upload backlog <i>clips pending</i></h2>
            <Chart points={series.backlog} unit="" color="var(--series-temp)" domain={[0, 1]}
              sinceMs={window_.sinceMs} untilMs={window_.untilMs} />
          </div>
        </div>
      )}

      <h1 style={{ marginTop: 28 }}>Blackbox <i className="sub">recording interruptions in the selected range</i></h1>
      {!bbRows ? (
        <div className="loading">LOADING BLACKBOX</div>
      ) : bbRows.length === 0 ? (
        <div className="empty">no transitions in this range — either uninterrupted recording or a device without the blackbox agent</div>
      ) : (
        <div className="bblist">
          {bbRows.map((ev, i) => (
            <div className="bbrow" key={`${ev.atMs}-${ev.type}-${i}`}>
              <span className="when" title={new Date(ev.atMs).toISOString()}>
                {new Date(ev.atMs).toLocaleString([], { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit', second: '2-digit' })}
              </span>
              <span className={`badge ${ev.cls}`}>{ev.label}</span>
              <span className="note">{ev.note}</span>
            </div>
          ))}
        </div>
      )}

      {events.length > 0 && (
        <>
          <h1 style={{ marginTop: 28 }}>Recent events</h1>
          <div className="evlist">
            {events.map((e) => (
              <Link key={e.id} className="ev" to={`/events/${encodeURIComponent(e.id)}`}>
                <img src={`/events/${encodeURIComponent(e.id)}/thumb`} alt=""
                  onError={(ev) => { ev.currentTarget.style.visibility = 'hidden' }} />
                <span className="what">
                  <span className="line">
                    <span className="reason">{e.reason || 'unknown trigger'}</span>
                    <span className="meta">{e.city}</span>
                  </span>
                  <span className="id">{e.id}</span>
                </span>
                <span className={`badge ${e.threat_level || 'state'}`}>{e.threat_level || e.analysis_state}</span>
              </Link>
            ))}
          </div>
        </>
      )}
    </>
  )
}
