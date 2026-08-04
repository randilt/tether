# Tether — Claude

Follow [AGENTS.md](./AGENTS.md) as the project constitution. Do not diverge from it.

## Hard constraints (summary)

- **Linux-only** — no Windows/macOS virtual cam or OS-specific cam drivers. Deliberate exclusion.
- v1 complete; **v2** = distribution, security, reliability, phone first-run UX — **one milestone prompt at a time**, in that order. Do not combine.
- No frontend framework — plain HTML/CSS/vanilla JS.
- LAN-only WebRTC — no STUN/TURN. Pairing token is LAN hygiene, not full auth (until a security milestone says otherwise).
- Deps stay lean; assets via `go:embed`. ffmpeg low-latency flags for media pipes.
