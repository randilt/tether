# Tether

Local server: phone browser → WebRTC → PC. Zero phone app install.

**v1–v2 done** (WebRTC, v4l2loopback, multi-device, pairing, mic, install.sh, reliability, phone first-run).  
**v3** = NDI-first, cross-platform Go server — every paired phone can be a discoverable NDI source for OBS/vMix; v4l2loopback stays the Linux single-webcam path.

## What this is

- Go HTTPS server (single static binary, `go:embed` for web assets)
- Phone opens a LAN HTTPS URL, grants camera/mic via `getUserMedia`, streams over WebRTC ([pion/webrtc](https://github.com/pion/webrtc))
- Signaling (SDP offer/answer) over WebSocket ([nhooyr.io/websocket](https://github.com/nhooyr/websocket))
- **Primary output:** NDI (opt-in) — one NDI source per connected phone; OBS/vMix/NDI Tools consume them
- **Linux path:** active device → **v4l2loopback** via **ffmpeg** (Zoom/Teams-style single webcam)
- Audio → NDI and/or Pulse/ALSA (Linux)
- Multi-device registry; control page monitors and can pick the active v4l2 device
- LAN pairing code/QR; LAN-only WebRTC — **no STUN/TURN**

## Platform decision (v3)

**Cross-platform Go server.** Do **not** invent Windows/macOS virtual camera drivers — use NDI + official NDI Tools / OBS Virtual Camera instead. Keep `v4l2loopback` as the Linux-only local webcam path (build-tagged).

NDI runtime is proprietary and must be **dynamically loaded** (never vendored into the repo). Open-source headers + dynamic loading per NDI SDK terms.

## Non-goals (unless the current prompt asks)

- Homegrown Windows DirectShow / macOS CoreMediaIO camera drivers
- Frontend frameworks — plain HTML/CSS/vanilla JS only
- System tray apps, Electron, user accounts, cloud signaling
- Touch / motion / other input capabilities beyond camera + mic
- Shipping the NDI proprietary runtime inside this repo
- Scope creep across v3 milestones — one prompt at a time

## v3 priorities (build in this order — do not combine)

When the user sends a milestone prompt, implement **only that** milestone:

1. **Constitution** — this file / CLAUDE.md (done when present)
2. **Phone endurance** — Screen Wake Lock + honest tripod UX (browser cannot keep camera in background)
3. **Pipeline refactor** — per-device ffmpeg + `videoSink` interface; codec-agnostic (H264 default, VP8 capable)
4. **NDI multi-source** — one NDI sender per phone; opt-in flags; control UI + ndi.video attribution
5. **Cross-platform builds** — build tags, goreleaser, Win/mac docs (NDI Tools / OBS)
6. **Production hardening** — NDI groups/opt-in UX, VP8 toggle, SAFETY.md / consent notice

Roughly equal importance; **order is strict**. Do not pre-build later items “while you’re in there.”

## Latency

Target glass-to-glass on LAN: ~30–100ms. ffmpeg must stay low-latency (`-fflags nobuffer`, `-flags low_delay`, small probesize/analyzeduration).

## Stack / conventions

- Language: Go; assets via `go:embed`
- UI: vanilla JS, no build step, no npm
- Video sinks behind a small interface; ffmpeg subprocess (not linked libs)
- NDI via no-cgo dynamic loader (`purego` / Windows LazyDLL)
- Prefer small, readable packages; early returns; happy path last
- Local TLS certs on disk / gitignored — no secrets in repo
- New deps only when a milestone needs them (justify in the change)
- H.264 remains the **default** phone encode (battery); VP8 is opt-in

## Agent behavior

- Follow the user’s **current** stage prompt strictly — scope creep is a bug
- Prefer editing existing files over adding new ones
- Do not add README/docs noise unless the milestone requires it
- Do not commit unless asked
- Keep diffs focused on the requested stage

## Runtime assumptions

- `ffmpeg` on PATH
- Phone and PC on the same LAN; phone trusts the local HTTPS cert for `getUserMedia`
- Linux virtual cam: `v4l2loopback` when using `-v4l2`
- NDI: NDI runtime installed separately when `-ndi` is enabled
- Browser cannot stream after app switch / screen lock — Wake Lock keeps the screen on while the page is foregrounded
