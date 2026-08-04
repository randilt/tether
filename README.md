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

`getUserMedia` needs a **trusted root CA**. Tether generates a local CA; `/cert.cer` is that CA (not the leaf). iOS only shows Full Trust toggles for root CAs — if Certificate Trust Settings is empty except “Trust Store Version”, you installed a leaf or skipped profile install.

1. On the PC: `rm -rf certs && go run .` (needed once if you already generated the old non-CA cert).
2. iPhone Safari → `https://<pc-lan-ip>:8443/cert.cer`  
   Accept the warning just to download (chicken-and-egg until trusted).
3. **Settings → Profile Downloaded** (or **General → VPN & Device Management**) → install **Tether Local CA**.
4. **Settings → General → About → Certificate Trust Settings** → enable **Full Trust** for **Tether Local CA**.
5. Fully quit Safari, reopen `https://<pc-lan-ip>:8443/phone`, Start camera.

If your LAN IP changes, `rm -rf certs`, restart, reinstall the CA on the phone.

## Acceptance check

1. Laptop: open `/control` — should say waiting for phone.
2. iPhone: open `/phone`, start camera, grant permission.
3. Laptop `<video>` shows the live phone feed.

## Notes

- LAN-only: no STUN/TURN (`iceServers: []`).
- Single phone + single viewer for this milestone.
- No v4l2 / ffmpeg / auth / multi-device in this pass.
- **iPhone → Linux PC:** Safari only sends **H264**. Linux **Chrome** often cannot decode it → black `<video>` with “Live” status. Use **Firefox** for `/control` (ships OpenH264), or another browser with H264.
