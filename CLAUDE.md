# Tether — Claude

Follow [AGENTS.md](./AGENTS.md) as the project constitution. Do not diverge from it.

## Hard constraints (summary)

- **OBS-first.** Phone → WebRTC → bare `/view` pages for OBS. Zoom via OBS Virtual Camera. Do **not** re-add NDI, v4l2loopback, or ffmpeg media sinks unless a prompt explicitly asks.
- No frontend framework — plain HTML/CSS/vanilla JS.
- LAN-only WebRTC — no STUN/TURN. Pairing token is LAN hygiene, not full auth.
- H.264 default (phone battery); VP8 opt-in. Deps stay lean; assets via `go:embed`.
- Control + view pages are localhost-only; phone uplink is on the LAN listener.
