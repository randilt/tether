# Tether

Local server: phone browser → WebRTC → PC. Zero phone app install.

**OBS-first.** Phones uplink over WebRTC; the PC control UI manages devices; each phone has a bare `/view` page for OBS Browser Source (or window capture). Zoom/Teams use **OBS Virtual Camera**. No NDI, no v4l2loopback, no ffmpeg media pipes.

## What this is

- Go HTTPS server (single static binary, `go:embed` for web assets)
- Phone opens a LAN HTTPS URL, grants camera/mic via `getUserMedia`, streams over WebRTC ([pion/webrtc](https://github.com/pion/webrtc))
- Signaling (SDP offer/answer) over WebSocket ([nhooyr.io/websocket](https://github.com/nhooyr/websocket))
- **Primary output:** bare localhost view pages — one per phone — for OBS to capture
- Multi-device registry; control page previews the focused phone and links to view URLs
- LAN pairing code/QR; LAN-only WebRTC — **no STUN/TURN**

## Platform decision

**Cross-platform Go server.** Do **not** invent Windows/macOS/Linux virtual camera drivers in-tree. Destination is OBS (Browser Source / Window Capture → OBS Virtual Camera). Re-adding NDI/v4l2/ffmpeg sinks only if a future prompt explicitly reopens that.

## Non-goals (unless the current prompt asks)

- Homegrown virtual camera drivers (v4l2loopback, DirectShow, CoreMediaIO)
- NDI runtime integration or shipping proprietary NDI binaries
- ffmpeg decode/encode pipelines for camera/mic sinks
- Frontend frameworks — plain HTML/CSS/vanilla JS only
- System tray apps, Electron, user accounts, cloud signaling
- Touch / motion / other input beyond camera + mic
- Shipping a headless Chromium capture appliance
- Scope creep — one prompt at a time

## Priorities

When the user sends a milestone prompt, implement **only that** prompt. Do not pre-build later items.

## Latency

Target glass-to-glass on LAN: ~30–100ms. WebRTC relay only — no re-encode on the server.

## Stack / conventions

- Language: Go; assets via `go:embed`
- UI: vanilla JS, no build step, no npm
- Prefer small, readable packages; early returns; happy path last
- Local TLS certs on disk / gitignored — no secrets in repo
- New deps only when a prompt needs them (justify in the change)
- H.264 remains the **default** phone encode (battery); VP8 is opt-in

## Agent behavior

- Follow the user’s **current** stage prompt strictly — scope creep is a bug
- Prefer editing existing files over adding new ones
- Do not add README/docs noise unless the milestone requires it
- Do not commit unless asked
- Keep diffs focused on the requested stage

## Runtime assumptions

- Phone and PC on the same LAN; phone trusts the local HTTPS cert for `getUserMedia`
- OBS (or similar) on the PC for capture + Virtual Camera when a webcam is needed in other apps
- Browser cannot stream after app switch / screen lock — Wake Lock keeps the screen on while the page is foregrounded
- Control and `/view` are localhost-only; phones use the LAN listener
