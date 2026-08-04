# Tether — Claude

Follow [AGENTS.md](./AGENTS.md) as the project constitution. Do not diverge from it.

## Hard constraints (summary)

- Linux-only virtual cam (v4l2loopback + ffmpeg). No Windows/macOS cam code.
- No frontend framework — plain HTML/CSS/vanilla JS.
- No installer / tray / packaging yet.
- Deps: `pion/webrtc`, `nhooyr.io/websocket`, stdlib otherwise. Assets via `go:embed`.
- LAN-only WebRTC — no STUN/TURN.
- **Staged builds only** — implement exactly what the current user prompt asks; do not pre-build later stages.
- When ffmpeg lands: low-latency flags (`-fflags nobuffer`, `-flags low_delay`, small probesize/analyzeduration). Target ~30–100ms glass-to-glass on LAN.
