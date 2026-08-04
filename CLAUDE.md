# Tether — Claude

Follow [AGENTS.md](./AGENTS.md) as the project constitution. Do not diverge from it.

## Hard constraints (summary)

- **v3 = NDI-first, cross-platform Go server.** Do not invent Win/macOS camera drivers; use NDI + NDI Tools / OBS Virtual Camera. Keep **v4l2loopback** as the Linux-only local webcam path.
- Never vendor the proprietary NDI runtime into the repo — dynamic load only.
- v1–v2 complete; **v3 milestones** in AGENTS.md — **one milestone prompt at a time**, in that order. Do not combine.
- No frontend framework — plain HTML/CSS/vanilla JS.
- LAN-only WebRTC — no STUN/TURN. Pairing token is LAN hygiene, not full auth (until a security milestone says otherwise).
- H.264 default (phone battery); VP8 opt-in. Deps stay lean; assets via `go:embed`. ffmpeg low-latency flags for media pipes.
- NDI is **opt-in** (security: unencrypted / discoverable on LAN). Attribute [ndi.video](https://ndi.video/) near NDI UI.
