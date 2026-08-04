# Tether

Phone browser → WebRTC → Linux PC virtual webcam (`v4l2loopback`) + browser preview.
Multiple phones can connect; the control page picks which one is active.

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
4. Quit Safari, open the **pairing URL** from the control page (includes `?t=…`), Start camera.

If your LAN IP changes, `rm -rf certs`, restart, reinstall the CA.

## Pairing (milestone 4)

On each server start a random 6-character code is generated (printed in the terminal and shown on `/control`).

- Phone must use `/phone?t=CODE` (or enter the code on `/phone`).
- WebSocket joins without a matching `t` get **403**.
- Code lasts for the process lifetime — restart → new code.
- This is LAN hygiene, not real auth.

## Acceptance check

1. Load v4l2loopback, start `go run .`, note the pairing code.
2. Open `/control` — code + phone URL visible; copy URL to phone.
3. Phone with URL connects as before (multi-device picker still works).
4. Another device opening `/phone` without the code cannot start a session (WS rejected).
5. Optional: two phones with `?t=CODE&name=Kitchen` etc., switch active on control.

## Notes

- LAN-only: no STUN/TURN (`iceServers: []`).
- No accounts/passwords — just a process-lifetime pairing token.
- Virtual cam path is H264-only (iPhone Safari). Browser preview relays the active device’s codec.
- ffmpeg uses low-latency flags (`nobuffer`, `low_delay`, tiny probesize/analyzeduration).
- **iPhone → Linux Chrome preview:** Chrome often can’t decode H264 → black `<video>`. Firefox usually works. OBS/Zoom use the v4l2 path.
