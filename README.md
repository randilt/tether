# Tether

Phone browser → WebRTC → Linux PC. Milestone 1 proves the video pipe in a browser preview (no v4l2 yet).

## Requirements

- Go 1.24+ (toolchain auto-downloads if needed)
- Linux PC and phone on the same Wi‑Fi/LAN
- Phone browser that supports `getUserMedia` + WebRTC (Safari on iOS is fine)

## Run

```bash
go run .
```

Listens on `https://0.0.0.0:8443` by default (`-addr` to change).

On first start it writes a self-signed cert to `certs/` (gitignored). The process prints LAN URLs:

| Page | Path |
|------|------|
| PC control (preview) | `/control` |
| Phone camera | `/phone` |
| Cert download (iOS) | `/cert.cer` |

## Trust the cert on iPhone (once)

`getUserMedia` requires a trusted HTTPS origin. Install the generated cert:

1. On the iPhone, open Safari to `https://<pc-lan-ip>:8443/cert.cer` and allow the profile download.
2. **Settings → Profile Downloaded** (or **Settings → General → VPN & Device Management**) → install the **Tether Local** profile.
3. **Settings → General → About → Certificate Trust Settings** → enable full trust for the Tether certificate.
4. Open `https://<pc-lan-ip>:8443/phone`, tap **Start camera**, allow camera access.

If your LAN IP changes, delete `certs/` and restart so SANs are regenerated, then reinstall the cert on the phone.

## Acceptance check

1. Laptop: open `/control` — should say waiting for phone.
2. iPhone: open `/phone`, start camera, grant permission.
3. Laptop `<video>` shows the live phone feed.

## Notes

- LAN-only: no STUN/TURN (`iceServers: []`).
- Single phone + single viewer for this milestone.
- No v4l2 / ffmpeg / auth / multi-device in this pass.
