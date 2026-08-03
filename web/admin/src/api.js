// Thin fetch wrapper over the operator API. Auth is the HttpOnly session
// cookie set by /admin/api/login; a 401/403 anywhere sends the UI back to the
// login screen via the onAuthError hook installed by App.

let onAuthError = () => {}
export function setAuthErrorHandler(fn) { onAuthError = fn }

async function request(path, opts = {}) {
  const resp = await fetch(path, { credentials: 'same-origin', ...opts })
  if (resp.status === 401 || resp.status === 403) {
    onAuthError()
    throw new Error('not authenticated')
  }
  if (!resp.ok) throw new Error(`${path}: HTTP ${resp.status}`)
  if (resp.status === 204) return null
  return resp.json()
}

export const api = {
  login: (token) =>
    fetch('/admin/api/login', {
      method: 'POST',
      credentials: 'same-origin',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ token }),
    }),
  logout: () => request('/admin/api/logout', { method: 'POST' }),
  me: () => request('/admin/api/me'),
  devices: () => request('/admin/api/devices'),
  heartbeats: (deviceId, sinceMs) =>
    request(`/admin/api/devices/${encodeURIComponent(deviceId)}/heartbeats?sinceMs=${sinceMs}`),
  blackbox: (deviceId, sinceMs) =>
    request(`/admin/api/devices/${encodeURIComponent(deviceId)}/blackbox?sinceMs=${sinceMs}`),
  events: () => request('/events'),
  event: (id) => request(`/events/${encodeURIComponent(id)}`),
}
