# Tether

Phone browser → WebRTC → Linux PC virtual webcam (`v4l2loopback`) and/or mic audio.
Multiple phones can connect; the control page picks which one is active.
Capabilities per device: `video`, `audio`, or `av`.

## Requirements

- Go 1.24+ (toolchain auto-downloads if needed)
- `ffmpeg` on PATH
- Linux PC and phone on the same Wi‑Fi/LAN
- Phone browser with `getUserMedia` + WebRTC (Safari on iOS is fine)
- For hearing mic audio: PulseAudio/PipeWire (`-audio pulse:default`, the default) or ALSA

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

## Optional: ALSA loopback (virtual mic for apps)

Default audio plays to speakers via Pulse (`pulse:default`) so you can hear the phone.
To expose the phone as a **system microphone** (Zoom/OBS/etc.):

```bash
sudo modprobe snd-aloop
# list cards: aplay -l   → look for "Loopback"
go run . -audio alsa:hw:Loopback,0,0
```

Then in your app, pick the Loopback **capture** device (often `hw:Loopback,1,0` / “Loopback” mic). Playback goes to `,0,0`; apps record from `,1,0`.

## Run

```bash
go run .                              # phone :8443 (LAN), control 127.0.0.1:8444
go run . -v4l2 /dev/video2
go run . -v4l2 ''                     # no virtual cam
go run . -control 127.0.0.1:9443      # custom control bind
go run . -audio pulse:default         # hear mic on PC (default)
go run . -audio alsa:hw:Loopback,0,0  # virtual mic via snd-aloop
go run . -audio ''                    # disable audio sink
```

**PC control is localhost-only by default** (`127.0.0.1:8444`). Phone + `/cert.cer` listen on the LAN (`:8443`). Do not open `/control` via your LAN IP — it will not be served there.

Listens on `https://0.0.0.0:8443` (`-addr` to change). First start writes a self-signed CA + cert to `certs/` (gitignored).

| Page | Where |
|------|------|
| PC control (preview + picker) | `https://127.0.0.1:8444/control` (localhost only) |
| Phone (camera / mic / both) | `https://<lan-ip>:8443/phone?t=CODE` |
| Cert download (iOS) | `https://<lan-ip>:8443/cert.cer` |

## Trust the cert on iPhone (once)

`getUserMedia` needs a **trusted root CA**. `/cert.cer` is the CA (not the leaf). iOS only shows Full Trust toggles for root CAs.

1. iPhone Safari → `https://<pc-lan-ip>:8443/cert.cer` (accept the warning once to download).
2. **Settings → Profile Downloaded** (or **General → VPN & Device Management**) → install **Tether Local CA**.
3. **Settings → General → About → Certificate Trust Settings** → enable **Full Trust** for **Tether Local CA**.
4. Quit Safari, open the **pairing URL** from the control page (includes `?t=…`).

If your LAN IP changes, `rm -rf certs`, restart, reinstall the CA.

## Pairing

On each server start a cryptographically random **8-character** code is generated (`crypto/rand`, unbiased alphabet).

- Phone must use `/phone?t=CODE` (or enter the code on `/phone`).
- WebSocket joins without a matching **current** `t` get **403**.
- Code is **single-use**: first successful phone connect rotates a new code.
- Code also **expires after 10 minutes** unused; control page updates live (countdown + new code/URL).
- Still session pairing only — no accounts or persistent auth.
- Startup prints a **terminal QR** of the current LAN phone URL (`github.com/mdp/qrterminal`).

## Acceptance check (milestone 5 — mic)

1. `go run .` (Pulse/PipeWire available).
2. Pair phone → choose **Mic** → Start → allow microphone.
3. Device list shows capability `audio`; make it active if needed.
4. Speak into the phone — hear it on the PC speakers. Logs: `asink: ffmpeg → pulse:default`.

Camera / multi-device / pairing still work as before (`video` / `av` capabilities).

## Acceptance check (v4l2 error + localhost control)

1. Ensure `/dev/video10` is **not** present (`sudo modprobe -r v4l2loopback` if loaded).
2. `go run .` → open `https://127.0.0.1:8444/control` — red banner with copyable `sudo modprobe v4l2loopback …`.
3. From another LAN device, `https://<pc-lan-ip>:8444/control` must fail to connect; phone URL on `:8443` still works.

## Notes

- LAN-only media: no STUN/TURN (`iceServers: []`).
- Control UI is not LAN-reachable by default (`-control 127.0.0.1:8444`).
- Active device drives both v4l2 (if it has video) and the audio sink (if it has audio).
- Virtual cam path is H264-only (iPhone Safari).
- Audio path: Opus RTP → Ogg → ffmpeg → Pulse/ALSA (low-latency flags).
- **iPhone → Linux Chrome preview:** Chrome often can’t decode H264 → black `<video>`. Firefox usually works. OBS/Zoom use the v4l2 path.
