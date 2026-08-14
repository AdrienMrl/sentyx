import { useEffect, useRef, useState } from 'react'
import { api } from './api'

// In-flight OTA states in the order the updater reports them (see
// internal/updater). Anything here means "working on it"; anything else is a
// terminal outcome. waiting-safe can hold for a while by design: the updater
// refuses to install while the car is writing to the drive.
const RUNNING = ['offered', 'downloading', 'waiting-safe', 'installing', 'rebooting']

// The updater's progressPct is coarse (1 while downloading, 90 installing), so
// each state gets a floor to keep the bar honest about forward motion, and the
// real pct wins whenever it is ahead.
const FLOOR = { offered: 4, downloading: 10, 'waiting-safe': 55, installing: 90, rebooting: 96 }

const LABEL = {
  offered: 'offered — updater accepted the release',
  downloading: 'downloading — verifying size, SHA-256, signature',
  'waiting-safe': 'waiting for the car to stop writing (install never lands mid-recording)',
  installing: 'installing under /opt/teslcam/releases',
  rebooting: 'restarting the agent',
  done: 'installed',
  failed: 'failed',
  'rolled-back': 'rolled back — the previous release was restored after a failed healthcheck',
}

// Firmware card for one device: shows running vs latest, triggers a pinned
// single-device campaign, and follows the install with a progress bar. Polls
// fast (4s) only while an update is actually moving; otherwise it rides the
// parent's normal refresh.
export default function Firmware({ device }) {
  const [update, setUpdate] = useState(device.update || null)
  const [busy, setBusy] = useState(false) // between click and first poll
  const [err, setErr] = useState('')
  const timer = useRef(null)

  useEffect(() => { setUpdate(device.update || null) }, [device])

  const active = update && RUNNING.includes(update.state)

  useEffect(() => {
    if (!active && !busy) return undefined
    const poll = () => {
      api.devices()
        .then((d) => {
          const me = d.devices.find((x) => x.deviceId === device.deviceId)
          if (me) {
            setUpdate(me.update || null)
            if (me.update && me.update.state) setBusy(false)
          }
        })
        .catch(() => {})
    }
    timer.current = setInterval(poll, 4000)
    return () => clearInterval(timer.current)
  }, [active, busy, device.deviceId])

  if (!update) return null // no release ever published — nothing to offer
  const running = device.status?.agentVersion
  const { available, latestVersion, targetVersion, releaseId, state, progressPct, error } = update

  const start = () => {
    setErr('')
    setBusy(true)
    api.startUpdate(releaseId, device.deviceId)
      .catch((e) => { setErr(e.message); setBusy(false) })
  }

  const pct = active ? Math.min(99, Math.max(FLOOR[state] || 0, progressPct || 0)) : state === 'done' ? 100 : 0
  const upToDate = running && running === latestVersion && !active

  return (
    <div className="fwcard">
      <div className="fwhead">
        <h2>Firmware</h2>
        <span className="fwver">
          running <b>{running || 'unknown'}</b>
          {latestVersion && <> · latest <b>{latestVersion}</b></>}
        </span>
        {upToDate && <span className="badge good">UP TO DATE</span>}
        {available && !active && !busy && (
          <button className="fwbtn" onClick={start}>
            INSTALL {targetVersion || latestVersion}
          </button>
        )}
        {busy && !active && <span className="badge state">STARTING…</span>}
      </div>

      {(active || busy || state === 'done' || state === 'failed' || state === 'rolled-back') && (
        <div className="fwprogress">
          <div className="fwbar">
            <div
              className={`fwfill ${state === 'failed' || state === 'rolled-back' ? 'bad' : ''} ${active && (state === 'waiting-safe' || busy) ? 'pulse' : ''}`}
              style={{ width: `${state === 'failed' || state === 'rolled-back' ? 100 : pct}%` }}
            />
          </div>
          <div className="fwstate">
            <span className={`badge ${state === 'done' ? 'good' : state === 'failed' || state === 'rolled-back' ? 'alarm' : 'state'}`}>
              {(state || 'starting').toUpperCase()}{active ? ` ${pct}%` : ''}
            </span>
            <span className="note">{LABEL[state] || 'waiting for the updater’s next poll (≤15 min)'}</span>
          </div>
          {(state === 'failed' || state === 'rolled-back') && (
            <div className="fwerr">
              {error || 'no error detail reported'}
              <button className="fwbtn" onClick={start}>RETRY</button>
            </div>
          )}
        </div>
      )}

      {err && <div className="fwerr">{err}</div>}
    </div>
  )
}
