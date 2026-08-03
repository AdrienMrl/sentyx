import { useMemo, useRef, useState } from 'react'

// Single-series time line chart, hand-rolled SVG.
// - 2px line, recessive grid, no legend (the card title names the series).
// - The path breaks where samples are missing for > 3× the median interval,
//   and those windows are shaded: an offline Pi shows as a hole, not a lie.
// - Crosshair + tooltip on hover (nearest sample).
const W = 1000
const H = 220
const PAD = { l: 44, r: 10, t: 10, b: 22 }

function niceTicks(min, max, n = 4) {
  if (min === max) { min -= 1; max += 1 }
  const span = max - min
  const step = Math.pow(10, Math.floor(Math.log10(span / n)))
  const err = span / n / step
  const mult = err >= 7.5 ? 10 : err >= 3.5 ? 5 : err >= 1.5 ? 2 : 1
  const s = mult * step
  const ticks = []
  for (let v = Math.ceil(min / s) * s; v <= max; v += s) ticks.push(v)
  return ticks
}

const fmtClock = (ms, spanMs) =>
  new Date(ms).toLocaleString([], spanMs > 36e5 * 26
    ? { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' }
    : { hour: '2-digit', minute: '2-digit' })

export default function Chart({ points, unit, color, domain, sinceMs, untilMs }) {
  const svgRef = useRef(null)
  const [hover, setHover] = useState(null)

  const model = useMemo(() => {
    if (!points.length) return null
    const t0 = sinceMs, t1 = untilMs
    const vs = points.map((p) => p.v)
    let vMin = Math.min(...vs), vMax = Math.max(...vs)
    if (domain) { vMin = Math.min(vMin, domain[0]); vMax = Math.max(vMax, domain[1]) }
    const pad = (vMax - vMin || 1) * 0.12
    vMin -= pad; vMax += pad
    const x = (t) => PAD.l + ((t - t0) / (t1 - t0)) * (W - PAD.l - PAD.r)
    const y = (v) => H - PAD.b - ((v - vMin) / (vMax - vMin)) * (H - PAD.t - PAD.b)

    // Gap threshold: 3× the median sampling interval, floored at 5 minutes.
    const dts = []
    for (let i = 1; i < points.length; i++) dts.push(points[i].t - points[i - 1].t)
    dts.sort((a, b) => a - b)
    const median = dts.length ? dts[Math.floor(dts.length / 2)] : 60_000
    const gapMs = Math.max(3 * median, 5 * 60_000)

    let d = ''
    const gaps = []
    points.forEach((p, i) => {
      const brk = i === 0 || p.t - points[i - 1].t > gapMs
      if (brk && i > 0) gaps.push([points[i - 1].t, p.t])
      d += `${brk ? 'M' : 'L'}${x(p.t).toFixed(1)},${y(p.v).toFixed(1)}`
    })
    // Leading/trailing silence also reads as offline.
    if (points[0].t - t0 > gapMs) gaps.unshift([t0, points[0].t])
    if (t1 - points[points.length - 1].t > gapMs) gaps.push([points[points.length - 1].t, t1])

    return { x, y, d, gaps, vMin, vMax, ticks: niceTicks(vMin + pad, vMax - pad), span: t1 - t0 }
  }, [points, domain, sinceMs, untilMs])

  if (!model) return <div className="empty">no samples in this window</div>

  const { x, y, d, gaps, ticks, span } = model

  const onMove = (e) => {
    const rect = svgRef.current.getBoundingClientRect()
    const t = sinceMs + ((e.clientX - rect.left) / rect.width) * (untilMs - sinceMs)
    let best = points[0]
    for (const p of points) if (Math.abs(p.t - t) < Math.abs(best.t - t)) best = p
    setHover({ p: best, cx: e.clientX, cy: e.clientY })
  }

  const timeTicks = niceTicks(sinceMs, untilMs, 5)

  return (
    <>
      <svg
        ref={svgRef}
        viewBox={`0 0 ${W} ${H}`}
        onMouseMove={onMove}
        onMouseLeave={() => setHover(null)}
      >
        {gaps.map(([a, b], i) => (
          <rect key={i} x={x(a)} y={PAD.t} width={Math.max(x(b) - x(a), 2)} height={H - PAD.t - PAD.b}
            fill="#e66767" opacity="0.09" />
        ))}
        {ticks.map((v) => (
          <g key={v}>
            <line x1={PAD.l} x2={W - PAD.r} y1={y(v)} y2={y(v)} stroke="#32322f" strokeWidth="1" />
            <text x={PAD.l - 8} y={y(v) + 4} textAnchor="end" fontSize="11"
              fill="#85847b" fontFamily="var(--mono)">{v}</text>
          </g>
        ))}
        {timeTicks.map((t) => (
          <text key={t} x={x(t)} y={H - 6} textAnchor="middle" fontSize="10.5"
            fill="#85847b" fontFamily="var(--mono)">{fmtClock(t, span)}</text>
        ))}
        <path d={d} fill="none" stroke={color} strokeWidth="2"
          strokeLinejoin="round" strokeLinecap="round" />
        {hover && (
          <g>
            <line x1={x(hover.p.t)} x2={x(hover.p.t)} y1={PAD.t} y2={H - PAD.b}
              stroke="#85847b" strokeWidth="1" strokeDasharray="3 3" />
            <circle cx={x(hover.p.t)} cy={y(hover.p.v)} r="4.5" fill={color}
              stroke="#1a1a19" strokeWidth="2" />
          </g>
        )}
      </svg>
      {hover && (
        <div className="tooltip" style={{ left: hover.cx + 14, top: hover.cy - 14 }}>
          {hover.p.v}{unit}
          <small>{new Date(hover.p.t).toLocaleString()}</small>
        </div>
      )}
    </>
  )
}
