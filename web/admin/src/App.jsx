import { useEffect, useState } from 'react'
import { HashRouter, Routes, Route, NavLink, useNavigate } from 'react-router-dom'
import { api, setAuthErrorHandler } from './api'
import Fleet from './Fleet'
import Device from './Device'
import Events from './Events'
import EventDetail from './EventDetail'

function Login({ onDone }) {
  const [token, setToken] = useState('')
  const [err, setErr] = useState('')
  const submit = async (e) => {
    e.preventDefault()
    const resp = await api.login(token.trim())
    if (resp.ok) onDone()
    else setErr(resp.status === 401 ? 'invalid operator token' : `login failed (HTTP ${resp.status})`)
  }
  return (
    <div className="login-wrap">
      <form className="login" onSubmit={submit}>
        <span className="brand">SENT<em>YX</em><small>fleet console</small></span>
        <label htmlFor="tok">operator token</label>
        <input id="tok" type="password" autoFocus value={token}
          onChange={(e) => setToken(e.target.value)} />
        <button type="submit">Authenticate</button>
        {err && <div className="err">{err}</div>}
      </form>
    </div>
  )
}

function Chrome({ children }) {
  const navigate = useNavigate()
  const logout = async () => {
    try { await api.logout() } catch { /* cookie may already be gone */ }
    window.location.reload()
  }
  return (
    <div className="shell">
      <header className="topbar">
        <a className="brand" href="#/" onClick={() => navigate('/')}>
          SENT<em>YX</em><small>fleet console</small>
        </a>
        <nav>
          <NavLink to="/" end className={({ isActive }) => (isActive ? 'active' : '')}>Fleet</NavLink>
          <NavLink to="/events" className={({ isActive }) => (isActive ? 'active' : '')}>Events</NavLink>
        </nav>
        <button className="logout" onClick={logout}>log out</button>
      </header>
      {children}
    </div>
  )
}

export default function App() {
  const [auth, setAuth] = useState('checking') // checking | out | in | unreachable
  useEffect(() => {
    setAuthErrorHandler(() => setAuth('out'))
    api.me().then(() => setAuth('in')).catch((e) => {
      // An auth failure already routed to the login screen via the handler;
      // anything else means the server itself is unreachable.
      if (e.message !== 'not authenticated') setAuth('unreachable')
    })
  }, [])

  if (auth === 'checking') return <div className="loading">CONNECTING</div>
  if (auth === 'unreachable') return (
    <div className="login-wrap">
      <div className="login">
        <span className="brand">SENT<em>YX</em><small>fleet console</small></span>
        <div className="err">server unreachable — is teslcam-server running?</div>
        <button onClick={() => window.location.reload()}>Retry</button>
      </div>
    </div>
  )
  if (auth === 'out') return <Login onDone={() => setAuth('in')} />
  return (
    <HashRouter>
      <Chrome>
        <Routes>
          <Route path="/" element={<Fleet />} />
          <Route path="/devices/:deviceId" element={<Device />} />
          <Route path="/events" element={<Events />} />
          <Route path="/events/:id" element={<EventDetail />} />
        </Routes>
      </Chrome>
    </HashRouter>
  )
}
