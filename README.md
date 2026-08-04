# Tether

Phone browser → WebRTC → PC. **Zero phone app install.**

OBS-first: each phone gets a bare `/view` page for **OBS Browser Source** (or Window Capture). Use **OBS Virtual Camera** when Zoom/Teams need a webcam.

## Install

From a clone:

```bash
./install.sh
```

Or:

```bash
go build -o tether .
```

Cross-platform binaries (when released):

```bash
goreleaser build --snapshot --clean
```

## Requirements

- Go 1.24+ (to build)
- Phone and PC on the same Wi‑Fi/LAN
- Phone browser with `getUserMedia` + WebRTC (Safari on iOS is fine)
- [OBS Studio](https://obsproject.com/) (or similar) on the PC for capture + Virtual Camera

No ffmpeg, v4l2loopback, or NDI runtime required.

## Run

```bash
go run .                         # phone :8443 (LAN), control 127.0.0.1:8444
go run . -control 127.0.0.1:9443 # custom control bind
```

**PC control + view pages are localhost-only by default** (`127.0.0.1:8444`). Phone + `/cert.cer` listen on the LAN (`:8443`).

| Page | Where |
|------|------|
| PC control | `https://127.0.0.1:8444/control` |
| Bare view (OBS) | `https://127.0.0.1:8444/view?id=<deviceId>` |
| Phone | `https://<lan-ip>:8443/phone?t=CODE` |
| Cert download (iOS) | `https://<lan-ip>:8443/cert.cer` |

## Quick path (OBS → Zoom)

1. Start Tether; open `https://127.0.0.1:8444/control`.
2. Pair a phone (QR / pairing URL); start camera.
3. On the device row: **Copy OBS URL** (or **Open view**).
4. OBS → Sources → **Browser Source** → paste the view URL (width/height e.g. 1280×720). Enable **Control audio via OBS** if you want mic.
5. **Start Virtual Camera** in OBS → pick **OBS Virtual Camera** in Zoom/Teams.

Same view URL works as a normal browser tab for **Window Capture**.

## Trust the cert on iPhone (once)

`getUserMedia` needs a **trusted root CA**. `/cert.cer` is the CA (not the leaf).

1. iPhone Safari → `https://<pc-lan-ip>:8443/cert.cer` (accept the warning once to download).
2. **Settings → Profile Downloaded** → install **Tether Local CA**.
3. **Settings → General → About → Certificate Trust Settings** → enable **Full Trust** for **Tether Local CA**.
4. Quit Safari, open the pairing URL / scan the QR.

If your LAN IP changes, `rm -rf certs`, restart, reinstall the CA.

## Pairing

On each server start a cryptographically random **8-character** code is generated.

- Phone must use `/phone?t=CODE` (or enter the code on `/phone`).
- Code is **single-use** and also **expires after 10 minutes** unused.
- Still session pairing only — no accounts.
- Startup prints a **terminal QR** of the current LAN phone URL.

## Notes

- See [SAFETY.md](./SAFETY.md) for consent and operator duties.
- LAN-only media: no STUN/TURN (`iceServers: []`).
- Control UI is not LAN-reachable by default.
- H.264 default (phone battery); VP8 opt-in from control.
- **iPhone → Linux Chrome preview:** Chrome often can’t decode H264 → black `<video>`. Firefox usually works. OBS Browser Source uses Chromium and is typically fine.
