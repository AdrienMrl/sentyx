# Prompt for Claude Design

Design a mobile app concept for **Sentyx**, a hardware-and-cloud product that turns a Raspberry Pi connected to a Tesla into a real-time Sentry Mode monitor. The Pi appears to the car as USB storage, detects new Sentry events while they are being written, uploads selected clips, and sends them to Gemini for analysis. The app connects locally to the Pi over Bluetooth or Wi-Fi and remotely to the Sentyx backend. It must eventually work on iOS and Android, but this request is for an interactive React mockup only.

## What to design now

Create **three visually and structurally distinct design directions**, each showing these two linked screens:

1. **Event feed** — today's Sentry events, useful severity cues, time, location, thumbnail, short Gemini-generated description, and a compact indication that the Pi/car is online.
2. **Event detail** — video preview, Gemini description and confidence, event severity, meaningful moments/timeline, camera source, location/time, and an action to download the original full-quality clip from the car over Wi-Fi or Bluetooth.

Use the same realistic mock data in all three directions so they can be compared fairly. Make each feed item open its corresponding detail screen, and make the three directions switchable inside the prototype.

## Realistic content

Use these events:

- 2:14 PM, Downtown Garage — **Attention** — “Person lingered near driver door” — left repeater — 48 seconds — Gemini: person looked through the driver window, then walked away; no contact detected; 92% confidence.
- 11:38 AM, Whole Foods · Rampart — **Routine** — “Shopping cart passed close to vehicle” — front camera — 1:02 — no contact; 88% confidence.
- 8:05 AM, Home — **Routine** — “Pedestrian walked past vehicle” — right repeater — 36 seconds — no stop or contact; 96% confidence.

Device state: Model 3, connected over Wi-Fi, online, 73% storage free. Original download: 248 MB; Wi-Fi recommended.

For each direction, give it a name and one-sentence design thesis. Implement the three directions in a single prototype and briefly explain which one you believe has the strongest long-term product identity and why.
