export const fmtBytes = (n) => {
  if (n == null) return '—'
  if (n >= 1e9) return (n / 1e9).toFixed(1) + ' GB'
  if (n >= 1e6) return (n / 1e6).toFixed(1) + ' MB'
  return Math.round(n / 1e3) + ' kB'
}

export const fmtAgo = (ms) => {
  if (!ms) return 'never'
  const s = Math.max(0, Math.floor((Date.now() - ms) / 1000))
  if (s < 90) return `${s}s ago`
  if (s < 5400) return `${Math.round(s / 60)}m ago`
  if (s < 129600) return `${Math.round(s / 3600)}h ago`
  return `${Math.round(s / 86400)}d ago`
}

export const fmtUptime = (sec) => {
  if (sec == null) return '—'
  if (sec < 3600) return `${Math.floor(sec / 60)}m`
  if (sec < 86400) return `${Math.floor(sec / 3600)}h ${Math.floor((sec % 3600) / 60)}m`
  return `${Math.floor(sec / 86400)}d ${Math.floor((sec % 86400) / 3600)}h`
}

export const pctFree = (hb) =>
  hb?.storageFreeBytes != null && hb?.storageTotalBytes
    ? Math.round((hb.storageFreeBytes / hb.storageTotalBytes) * 100)
    : null
