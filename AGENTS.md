# Tether

Local server: phone browser → WebRTC → Linux PC virtual webcam / mic. Zero phone app install.

**v1 is done** (WebRTC video + v4l2loopback, multi-device picker, pairing, mic capability).  
**v2** = make it usable by other Linux users — not a rewrite, and **not** cross-platform.

## What this is

- Go HTTPS server on the PC (single static binary, `go:embed` for web assets)
- Phone opens a LAN HTTPS URL, grants camera/mic via `getUserMedia`, streams over WebRTC ([pion/webrtc](https://github.com/pion/webrtc))
- Signaling (SDP offer/answer) over WebSocket ([nhooyr.io/websocket](https://github.com/nhooyr/websocket))
- Video → **v4l2loopback** via **ffmpeg** subprocess; audio → Pulse/ALSA (optional `snd-aloop`)
- Multi-device registry; PC control page picks the active device
- LAN pairing code/QR; LAN-only — **no STUN/TURN**

## Explicit platform decision

**Linux-only.** Do **not** add Windows or macOS virtual camera / audio loopback support. That was evaluated and deliberately excluded (drivers on those platforms are a separate, much larger undertaking).

## Non-goals (unless the current prompt asks)

- Windows / macOS virtual cam or packaging for those OSes
- Frontend frameworks — plain HTML/CSS/vanilla JS only
- System tray apps, Electron, user accounts, cloud signaling
- Touch / motion / other input capabilities beyond camera + mic
- Scope creep across v2 milestones — one prompt at a time

## v2 priorities (build in this order — do not combine)

When the user sends a milestone prompt, implement **only that** milestone:

1. **Installer / distribution polish** — other Linux users can install and run without spelunking the repo
2. **Security hardening** — beyond the lightweight pairing token
3. **Reliability** — reconnects, device loss, ffmpeg death, unclean exits
4. **UX — phone-side first-run flow** — cert trust + pairing + permissions without a README scavenger hunt

Roughly equal importance; **order is strict**. Do not pre-build later items “while you’re in there.”

## Latency

Target glass-to-glass on LAN: ~30–100ms. ffmpeg must stay low-latency (`-fflags nobuffer`, `-flags low_delay`, small probesize/analyzeduration).

## Stack / conventions

- Language: Go; assets via `go:embed`
- UI: vanilla JS, no build step, no npm
- Virtual cam: v4l2loopback + ffmpeg subprocess (not linked libs)
- Prefer small, readable packages; early returns; happy path last
- Local TLS certs on disk / gitignored — no secrets in repo
- New deps only when a milestone needs them (justify in the change)

## Agent behavior

- Follow the user’s **current** stage prompt strictly — scope creep is a bug
- Prefer editing existing files over adding new ones
- Do not add README/docs noise unless the milestone requires it
- Do not commit unless asked
- Keep diffs focused on the requested stage

## Runtime assumptions (Linux)

- `ffmpeg`; `v4l2loopback` when using virtual cam; Pulse/PipeWire or ALSA for mic hear-back
- Phone and PC on the same LAN; phone trusts the local HTTPS cert for `getUserMedia`
