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

// Firmware card for one device, driven by server state, not local optimism:
//   active install  -> progress bar from the updater's reported state
//   plan pending    -> QUEUED (campaign exists; unit installs on next poll)
//   behind latest   -> INSTALL button, targeting latestReleaseId — never the
//                      releaseId of the current plan/last attempt, which is
//                      how a failed bundle once got re-offered three times
//   same version    -> UP TO DATE
// Every API failure renders in the card; nothing is swallowed.
export default function Firmware({ device }) {
  const [update, setUpdate] = useState(device.update || null)
  const [busy, setBusy] = useState(false) // POST in flight
  const [justQueued, setJustQueued] = useState(false)
  const [err, setErr] = useState('')
  const timer = useRef(null)

  useEffect(() => { setUpdate(device.update || null) }, [device])

  const active = update && RUNNING.includes(update.state)
  const pending = update && !active && update.available

  // Poll fast while anything is in motion; otherwise ride the parent refresh.
  useEffect(() => {
    if (!active && !pending && !justQueued) return undefined
    const poll = () => {
      api.devices()
        .then((d) => {
          const me = d.devices.find((x) => x.deviceId === device.deviceId)
          if (me) setUpdate(me.update || null)
        })
        .catch((e) => setErr(e.message))
    }
    timer.current = setInterval(poll, 4000)
    return () => clearInterval(timer.current)
  }, [active, pending, justQueued, device.deviceId])

  if (!update) return null // no release ever published — nothing to offer
  const running = device.status?.agentVersion
  const { latestVersion, latestReleaseId, targetVersion, state, progressPct, error } = update
  const upToDate = running && running === latestVersion && !active && !pending

  const start = () => {
    if (!latestReleaseId) { setErr('server did not report a latest release id — refresh?'); return }
    setErr('')
    setBusy(true)
    api.startUpdate(latestReleaseId, device.deviceId)
      .then(() => { setJustQueued(true) })
      .catch((e) => setErr(`could not start update: ${e.message}`))
      .finally(() => setBusy(false))
  }

  const pct = active ? Math.min(99, Math.max(FLOOR[state] || 0, progressPct || 0)) : state === 'done' ? 100 : 0
  const failed = state === 'failed' || state === 'rolled-back'

  return (
    <div className="fwcard">
      <div className="fwhead">
        <h2>Firmware</h2>
        <span className="fwver">
          running <b>{running || 'unknown'}</b>
          {latestVersion && <> · latest <b>{latestVersion}</b></>}
        </span>
        {upToDate && <span className="badge good">UP TO DATE</span>}
        {pending && !justQueued && <span className="badge state">QUEUED → {targetVersion || latestVersion}</span>}
        {!active && !pending && !upToDate && !busy && (
          <button className="fwbtn" onClick={start} disabled={busy}>
            INSTALL {latestVersion}
          </button>
        )}
        {busy && <span className="badge state">STARTING…</span>}
      </div>

      {justQueued && !active && (
        <div className="fwqueued">
          campaign created for <b>{latestVersion}</b> — the unit picks it up on its next
          updater poll (≤15 min) and the bar below starts moving
        </div>
      )}

      {(active || pending || state === 'done' || failed) && (
        <div className="fwprogress">
          <div className="fwbar">
            <div
              className={`fwfill ${failed ? 'bad' : ''} ${(active && state === 'waiting-safe') || pending ? 'pulse' : ''}`}
              style={{ width: `${failed ? 100 : pending ? 2 : pct}%` }}
            />
          </div>
          <div className="fwstate">
            <span className={`badge ${state === 'done' ? 'good' : failed ? 'alarm' : 'state'}`}>
              {(active || failed || state === 'done' ? state : 'queued').toUpperCase()}{active ? ` ${pct}%` : ''}
            </span>
            <span className="note">
              {active || failed || state === 'done'
                ? LABEL[state]
                : 'waiting for the unit’s next updater poll (≤15 min)'}
            </span>
          </div>
          {failed && (
            <div className="fwerr">
              {error || 'no error detail reported'}
              <button className="fwbtn" onClick={start}>RETRY WITH {latestVersion}</button>
            </div>
          )}
        </div>
      )}

      {err && <div className="fwerr">{err}</div>}
    </div>
  )
}
