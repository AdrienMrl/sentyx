import React, { useState } from 'react'
import { createRoot } from 'react-dom/client'
import './styles.css'

const events = [
  {
    id: '0427', time: '14:14', place: 'Downtown Garage', severity: 'attention',
    title: 'Person lingered near driver door', camera: 'LEFT REPEATER', duration: '00:48',
    analysis: 'A person approached the driver-side door, looked through the window for several seconds, then walked away. No contact with the vehicle was detected.',
    confidence: 92, tone: 'amber', tags: ['1 PERSON', 'NO CONTACT', '48 SEC'],
    moments: [['00:12', 'Enters frame'], ['00:21', 'Pauses at driver door'], ['00:39', 'Leaves frame']],
  },
  {
    id: '0426', time: '11:38', place: 'Whole Foods · Rampart', severity: 'routine',
    title: 'Shopping cart passed close to vehicle', camera: 'FRONT CAMERA', duration: '01:02',
    analysis: 'An unattended shopping cart rolled across the front of the vehicle. It passed within approximately two feet but did not make contact.',
    confidence: 88, tone: 'blue', tags: ['OBJECT', 'NO CONTACT', '1 MIN'],
    moments: [['00:08', 'Cart enters frame'], ['00:28', 'Closest approach'], ['00:51', 'Leaves frame']],
  },
  {
    id: '0425', time: '08:05', place: 'Home', severity: 'routine',
    title: 'Pedestrian walked past vehicle', camera: 'RIGHT REPEATER', duration: '00:36',
    analysis: 'A pedestrian walked beside the vehicle at a normal pace. They did not stop, look inside, or touch the car.',
    confidence: 96, tone: 'green', tags: ['1 PERSON', 'ROUTINE', '36 SEC'],
    moments: [['00:04', 'Enters frame'], ['00:17', 'Passes vehicle'], ['00:31', 'Leaves frame']],
  },
]

const Icon = ({ name, size = 20 }) => {
  const icons = {
    back: <><path d="m15 18-6-6 6-6"/><path d="M9 12h10"/></>,
    chevron: <path d="m9 18 6-6-6-6"/>,
    download: <><path d="M12 3v12"/><path d="m7 10 5 5 5-5"/><path d="M5 21h14"/></>,
    grid: <><rect x="3" y="3" width="7" height="7" rx="1"/><rect x="14" y="3" width="7" height="7" rx="1"/><rect x="3" y="14" width="7" height="7" rx="1"/><rect x="14" y="14" width="7" height="7" rx="1"/></>,
    play: <path d="m9 7 8 5-8 5Z" fill="currentColor" stroke="none"/>,
    share: <><circle cx="18" cy="5" r="2"/><circle cx="6" cy="12" r="2"/><circle cx="18" cy="19" r="2"/><path d="m8 11 8-5M8 13l8 5"/></>,
    spark: <path d="m12 3 1.4 4.6L18 9l-4.6 1.4L12 15l-1.4-4.6L6 9l4.6-1.4Z"/>,
    sliders: <><path d="M4 6h16M4 12h16M4 18h16"/><circle cx="9" cy="6" r="2" fill="currentColor"/><circle cx="15" cy="12" r="2" fill="currentColor"/><circle cx="7" cy="18" r="2" fill="currentColor"/></>,
  }
  return <svg viewBox="0 0 24 24" width={size} height={size} fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">{icons[name]}</svg>
}

function StatusBar() {
  return <div className="status"><b>9:41</b><div><span>●●●●</span><span>⌁</span><span className="battery">87</span></div></div>
}

function CameraStill({ event, hero = false, children }) {
  return <div className={`camera-still ${event.tone} ${hero ? 'hero' : ''}`}>
    <div className="architecture"><i/><i/><i/></div>
    <div className="mock-car"><i className="glass"/><i className="wheel w1"/><i className="wheel w2"/></div>
    <div className="mock-person"><i/><b/></div>
    <span className="cam-name">{event.camera}</span>
    <span className="cam-time">{event.duration}</span>
    {children}
  </div>
}

function VariantPicker({ variant, onChange }) {
  const variants = [['archive', '01', 'Archive'], ['console', '02', 'Console'], ['companion', '03', 'Companion']]
  return <div className="variant-picker" aria-label="Choose design variation">
    {variants.map(([id, number, name]) => <button key={id} className={variant === id ? 'active' : ''} onClick={() => onChange(id)}><span>{number}</span>{name}</button>)}
  </div>
}

function ArchiveFeed({ onOpen }) {
  return <div className="archive screen">
    <StatusBar/>
    <header className="archive-masthead"><span>SENTYX / INCIDENT ARCHIVE</span><b>CAR 01 — MODEL 3</b></header>
    <section className="archive-date">
      <div><span>TUE</span><strong>14</strong></div>
      <div><h1>July</h1><p>3 observations recorded<br/>Las Vegas, Nevada</p></div>
      <button aria-label="Filter"><Icon name="sliders"/></button>
    </section>
    <div className="archive-rule"><span>TIME</span><span>VISUAL RECORD</span><span>ASSESSMENT</span></div>
    <section className="archive-events">
      {events.map((event, index) => <button className="archive-event" onClick={() => onOpen(event)} key={event.id}>
        <div className="archive-index"><b>{event.time}</b><span>#{event.id}</span></div>
        <CameraStill event={event}/>
        <div className="archive-copy"><span className={`archive-severity ${event.severity}`}>{event.severity}</span><h2>{event.title}</h2><p>{event.place}</p><span className="read-case">READ CASE <Icon name="chevron" size={12}/></span></div>
        <span className="vertical-number">0{index + 1}</span>
      </button>)}
    </section>
    <footer className="archive-footer"><span>CONNECTED / WIFI</span><b>73% FREE</b></footer>
  </div>
}

function ArchiveDetail({ event, onBack }) {
  return <div className="archive archive-detail screen">
    <StatusBar/>
    <header className="archive-detail-nav"><button onClick={onBack}><Icon name="back"/> INDEX</button><span>CASE #{event.id}</span></header>
    <section className="case-title"><span>OBSERVATION / {event.severity}</span><h1>{event.title}</h1><div><p>JUL 14 · {event.time}</p><p>{event.place}</p></div></section>
    <CameraStill event={event} hero><button className="square-play"><Icon name="play" size={25}/></button><span className="frame-label">EVIDENCE A / ORIGINAL</span></CameraStill>
    <section className="case-analysis">
      <aside><Icon name="spark"/><span>GEMINI<br/>ANALYSIS</span><b>{event.confidence}%</b></aside>
      <div><p>{event.analysis}</p><div className="case-tags">{event.tags.map(tag => <span key={tag}>{tag}</span>)}</div></div>
    </section>
    <section className="case-moments"><header><span>EVENT SEQUENCE</span><span>UTC−07</span></header>{event.moments.map(([time, label], i) => <div key={time}><b>0{i+1}</b><span>{time}</span><p>{label}</p>{i === 1 && <i/>}</div>)}</section>
    <button className="archive-download"><span>DOWNLOAD EVIDENCE</span><small>ORIGINAL · 248 MB</small><Icon name="download"/></button>
  </div>
}

function ConsoleFeed({ onOpen }) {
  const lead = events[0]
  return <div className="console screen">
    <StatusBar/>
    <header className="console-head"><div className="console-logo"><i/><b>SX</b></div><div><span>VEHICLE LINK</span><b>MODEL 3 / ACTIVE</b></div><div className="pulse"><i/>LIVE</div></header>
    <section className="console-title"><p>SECURITY LOG</p><h1>Nothing touched<br/>your car.</h1><div className="scan-line"><span>LAST SCAN 00:08 AGO</span><i/></div></section>
    <button className="lead-event" onClick={() => onOpen(lead)}>
      <CameraStill event={lead} hero><span className="rec">● REC 14:14:27</span><span className="target t1"/><span className="target t2"/></CameraStill>
      <div className="lead-caption"><span className="signal-code">A—03</span><div><small>REVIEW SUGGESTED</small><h2>{lead.title}</h2><p>{lead.place} · {lead.duration}</p></div><Icon name="chevron"/></div>
    </button>
    <section className="console-queue"><header><span>EARLIER / 02</span><button><Icon name="grid" size={16}/> ALL FEEDS</button></header>
      {events.slice(1).map(event => <button onClick={() => onOpen(event)} key={event.id}><span className="queue-time">{event.time}</span><CameraStill event={event}/><div><small>{event.severity}</small><b>{event.title}</b><span>{event.place}</span></div><Icon name="chevron" size={15}/></button>)}
    </section>
    <nav className="console-dock"><button className="active">LOG</button><button>VEHICLE</button><span><i/></span><button>TRANSFER</button><button>CONTROL</button></nav>
  </div>
}

function ConsoleDetail({ event, onBack }) {
  return <div className="console console-detail screen">
    <StatusBar/>
    <header className="console-detail-head"><button onClick={onBack}><Icon name="back"/></button><span>INCIDENT / {event.id}</span><button><Icon name="share"/></button></header>
    <CameraStill event={event} hero><span className="rec">● CAM / {event.camera}</span><span className="reticle"><i/><i/></span><button className="round-play"><Icon name="play" size={27}/></button></CameraStill>
    <section className="threat-readout"><div><span>THREAT INDEX</span><strong>{event.severity === 'attention' ? '34' : '08'}<small>/100</small></strong></div><i/><div><span>CONTACT</span><strong>NONE</strong></div></section>
    <section className="console-summary"><header><Icon name="spark"/><span>AI FIELD REPORT</span><b>{event.confidence}% MATCH</b></header><p>{event.analysis}</p></section>
    <section className="console-timeline"><span className="axis"/>{event.moments.map(([time, label], i) => <div key={time} className={i === 1 ? 'hot' : ''}><b>{time}</b><i/><span>{label}</span></div>)}</section>
    <div className="console-actions"><button><Icon name="download"/><span>PULL ORIGINAL<small>248 MB / WIFI</small></span></button><button><Icon name="share"/></button></div>
  </div>
}

function CompanionFeed({ onOpen }) {
  return <div className="companion screen">
    <StatusBar/>
    <header className="companion-head"><div><span>GOOD AFTERNOON</span><h1>Your Model 3</h1></div><button>AM</button></header>
    <section className="car-orbit"><div className="orbit one"/><div className="orbit two"/><div className="mini-car"><i/><b/><span/></div><div className="car-status"><i/>Protected<span>Online · 73% free</span></div></section>
    <section className="companion-intro"><span>TODAY · 3 MOMENTS</span><h2>One moment may<br/>need your attention.</h2></section>
    <section className="moment-stack">
      {events.map((event, index) => <button className={`moment-card moment-${index}`} key={event.id} onClick={() => onOpen(event)}>
        <CameraStill event={event}/><div className="moment-copy"><div><span>{event.time}</span><i className={event.severity}/></div><h3>{event.title}</h3><p>{event.place}</p></div>{index === 0 && <span className="attention-sticker">TAKE A LOOK</span>}
      </button>)}
    </section>
    <nav className="companion-nav"><button className="active"><i/>Moments</button><button><i/>My car</button><button><i/>You</button></nav>
  </div>
}

function CompanionDetail({ event, onBack }) {
  return <div className="companion companion-detail screen">
    <StatusBar/>
    <header className="companion-detail-head"><button onClick={onBack}><Icon name="back"/></button><span>{event.time} today</span><button><Icon name="share"/></button></header>
    <section className="story-title"><span className={event.severity}>{event.severity === 'attention' ? 'Worth a look' : 'All clear'}</span><h1>{event.title}</h1><p>{event.place}</p></section>
    <CameraStill event={event} hero><button className="soft-play"><Icon name="play" size={24}/></button></CameraStill>
    <section className="plain-analysis"><div><Icon name="spark"/><span>Sentyx saw</span><b>{event.confidence}% sure</b></div><p>{event.analysis}</p></section>
    <section className="story-beats"><span>THE SHORT VERSION</span><div>{event.moments.map(([time, label], i) => <article key={time} className={i === 1 ? 'active' : ''}><b>{time}</b><i/><p>{label}</p></article>)}</div></section>
    <button className="keep-button"><span>Keep the original<small>Download 248 MB from your car</small></span><Icon name="download"/></button>
  </div>
}

function Phone({ variant, selected, onOpen, onBack }) {
  if (variant === 'archive') return selected ? <ArchiveDetail event={selected} onBack={onBack}/> : <ArchiveFeed onOpen={onOpen}/>
  if (variant === 'console') return selected ? <ConsoleDetail event={selected} onBack={onBack}/> : <ConsoleFeed onOpen={onOpen}/>
  return selected ? <CompanionDetail event={selected} onBack={onBack}/> : <CompanionFeed onOpen={onOpen}/>
}

function App() {
  const [variant, setVariant] = useState('archive')
  const [selected, setSelected] = useState(null)
  const names = { archive: ['Evidence Archive', 'Editorial, forensic, high-trust'], console: ['Camera Console', 'Technical, vigilant, real-time'], companion: ['Quiet Companion', 'Warm, calm, human'] }
  const changeVariant = next => { setVariant(next); setSelected(null) }
  return <main className={`design-lab lab-${variant}`}>
    <aside className="lab-panel"><span className="lab-kicker">SENTYX / DESIGN STUDY</span><h1>{names[variant][0]}</h1><p>{names[variant][1]}</p><VariantPicker variant={variant} onChange={changeVariant}/><div className="lab-hint">Open any event to compare<br/>the corresponding detail view.</div></aside>
    <div className="mobile-picker"><VariantPicker variant={variant} onChange={changeVariant}/></div>
    <div className="phone"><Phone variant={variant} selected={selected} onOpen={setSelected} onBack={() => setSelected(null)}/></div>
  </main>
}

createRoot(document.getElementById('root')).render(<App/>)
