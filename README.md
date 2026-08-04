# Tether

Phone browser → WebRTC → Linux PC virtual webcam (`v4l2loopback`) + browser preview.

## Requirements

- Go 1.24+ (toolchain auto-downloads if needed)
- `ffmpeg` on PATH
- Linux PC and phone on the same Wi‑Fi/LAN
- Phone browser with `getUserMedia` + WebRTC (Safari on iOS is fine)

## One-time: v4l2loopback

```bash
sudo apt install v4l2loopback-dkms ffmpeg   # or your distro’s equivalents

# Create one virtual camera at /dev/video10 labeled "Tether"
sudo modprobe v4l2loopback devices=1 video_nr=10 card_label=Tether exclusive_caps=1
```

`exclusive_caps=1` matters so Chrome/Zoom/OBS treat it as a capture device.

Check:

```bash
v4l2-ctl --list-devices   # or: ls -l /dev/video10
```

Persist across reboots (example):

```bash
echo 'v4l2loopback' | sudo tee /etc/modules-load.d/v4l2loopback.conf
echo 'options v4l2loopback devices=1 video_nr=10 card_label=Tether exclusive_caps=1' \
  | sudo tee /etc/modprobe.d/v4l2loopback.conf
```

## Run

```bash
go run .                  # default virtual cam: /dev/video10
go run . -v4l2 /dev/video2
go run . -v4l2 ''         # browser preview only, no v4l2
```

Listens on `https://0.0.0.0:8443` (`-addr` to change). First start writes a self-signed CA + cert to `certs/` (gitignored).

| Page | Path |
|------|------|
| PC control (preview) | `/control` |
| Phone camera | `/phone` |
| Cert download (iOS) | `/cert.cer` |

## Trust the cert on iPhone (once)

`getUserMedia` needs a **trusted root CA**. `/cert.cer` is the CA (not the leaf). iOS only shows Full Trust toggles for root CAs.

1. iPhone Safari → `https://<pc-lan-ip>:8443/cert.cer` (accept the warning once to download).
2. **Settings → Profile Downloaded** (or **General → VPN & Device Management**) → install **Tether Local CA**.
3. **Settings → General → About → Certificate Trust Settings** → enable **Full Trust** for **Tether Local CA**.
4. Quit Safari, open `/phone`, Start camera.

If your LAN IP changes, `rm -rf certs`, restart, reinstall the CA.

## Acceptance check (milestone 2)

1. `modprobe` the loopback device (above).
2. `go run .` — confirm it prints `Virtual cam: /dev/video10` without a missing-device warning.
3. Phone: `/phone` → start camera.
4. PC: `/control` still shows the preview; logs should show `vcam: ffmpeg → /dev/video10`.
5. OBS/Zoom → select camera **Tether** (or `/dev/video10`) — live feed with hand-wave latency, not a visible ~500ms lag.

## Notes

- LAN-only: no STUN/TURN (`iceServers: []`).
- Single phone, single virtual cam — no auth, no device picker.
- Virtual cam path is H264-only (iPhone Safari). Browser preview still relays whatever codec negotiated.
- ffmpeg uses low-latency flags (`nobuffer`, `low_delay`, tiny probesize/analyzeduration).
- **iPhone → Linux Chrome preview:** Chrome often can’t decode H264 → black `<video>`. Firefox usually works. OBS/Zoom use the v4l2 path and don’t care about the browser codec.
