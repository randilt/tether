# Tether

Local server: phone browser → WebRTC → Linux PC virtual webcam. Zero phone app install.

## What this is

- Go HTTPS server on the PC (single static binary, `go:embed` for all web assets)
- Phone opens a LAN HTTPS URL, grants camera via `getUserMedia`, streams over WebRTC ([pion/webrtc](https://github.com/pion/webrtc))
- Signaling (SDP offer/answer) over WebSocket ([nhooyr.io/websocket](https://github.com/nhooyr/websocket))
- PC decodes the inbound track and pipes into a **v4l2loopback** device via an **ffmpeg** subprocess so Zoom/OBS/etc. see a normal webcam
- LAN-only by design — **no STUN/TURN**

## Non-goals (do not build)

- Windows / macOS virtual camera paths — Linux only
- Frontend frameworks — plain HTML/CSS/vanilla JS only (phone page + PC control page)
- Installer, system tray, packaging of any kind
- Extra dependencies beyond pion/webrtc, nhooyr.io/websocket, and stdlib
- Mic / other input devices until a later stage explicitly asks

## Staged builds (critical)

Build **only** what the current prompt asks for. Do not pre-build later stages “while you’re in there.”

Suggested stages (reference only — implement when asked):

1. HTTPS static server + embedded phone/PC pages (no WebRTC yet)
2. WebSocket signaling (SDP offer/answer exchange)
3. WebRTC media path (phone → PC), LAN ICE only
4. Decode inbound track → ffmpeg → v4l2loopback
5. Mic / additional devices (later)

## Latency

Target glass-to-glass on LAN: ~30–100ms. Main risk: naive ffmpeg buffering.

When implementing the ffmpeg step, use explicit low-latency flags, e.g.:

- `-fflags nobuffer`
- `-flags low_delay`
- small `-probesize` / `-analyzeduration`

## Stack / conventions

- Language: Go
- Assets: `go:embed` into the binary — no separate static file server process
- UI: vanilla JS, no build step, no npm
- Signaling: WebSocket; media: WebRTC (pion)
- Virtual cam: v4l2loopback + ffmpeg subprocess (not linked ffmpeg libs)
- Prefer small, readable packages over clever abstractions
- Early returns for errors; happy path last
- No secrets in repo; local TLS certs stay on disk / gitignored

## Agent behavior

- Follow the user’s current stage prompt strictly — scope creep is a bug
- Prefer editing existing files over adding new ones
- Do not add README/docs unless asked
- Do not commit unless asked
- Keep diffs focused on the requested stage

## Runtime assumptions (Linux)

- Host has (or will have) `ffmpeg` and a loaded `v4l2loopback` module when that stage lands
- Phone and PC on the same LAN; phone trusts or accepts the local HTTPS cert
- Camera permission is granted in the phone browser
