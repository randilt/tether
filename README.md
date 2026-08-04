# Tether

Phone browser → WebRTC → PC. **Zero phone app install.**

- **NDI (opt-in):** each connected phone publishes as its own NDI source — OBS/vMix switch cameras for events
- **Linux:** active phone can also feed `v4l2loopback` (Zoom/Teams-style single webcam) + Pulse/ALSA hear-back
- Pairing code/QR; multi-device registry; control UI on localhost

## Install (Linux)

From a clone:

```bash
./install.sh
```

Fresh machine (saves the script, then runs it — so prompts work):

```bash
curl -fsSL https://raw.githubusercontent.com/randilt/tether/main/install.sh -o install.sh
bash install.sh
```

The script builds the `tether` binary into `~/.local/bin`, checks for `ffmpeg` / `v4l2loopback`, prints the right package command for apt/dnf/pacman when something’s missing, and **asks before any sudo** (shows the exact command). No `.deb`/AUR yet.

Cross-platform binaries (when released):

```bash
goreleaser build --snapshot --clean   # or download a GitHub Release
```

## Windows / macOS (NDI path)

The Go server runs on Windows and macOS. There is **no** built-in virtual webcam driver on those OSes — use NDI:

1. Install [ffmpeg](https://ffmpeg.org/) on PATH.
2. Install the [NDI runtime / SDK](https://ndi.video/for-developers/ndi-sdk/) (dynamic load — not bundled).
3. Run: `tether -ndi -v4l2 "" -audio ""`
4. In **OBS**: add an NDI Source (DistroAV / obs-ndi) for each phone, or feed **OBS Virtual Camera**.
5. For Zoom/Teams on Windows/macOS: install [NDI Tools](https://ndi.video/tools/) → **NDI Virtual Input**, or use OBS Virtual Camera.

### macOS Sonoma+ caveats

NDI Virtual Input has had camera-extension / Sonoma breakage (duplicate extensions, legacy camera plugins). If Virtual Input does not appear as a webcam, prefer **OBS Virtual Camera** fed by an NDI source. See Vizrt’s NDI Tools docs for recovery/extension fixes.

## NDI (multi-phone / events)

```bash
tether -ndi -ndi-name TETHER -ndi-size 1280x720
# optional: -ndi-groups school-gym   # keep sources off the public default group
```

Each live phone becomes e.g. `TETHER (iPhone a1b2c3d4)`. OBS can show all of them and switch live.

**Security:** NDI is unencrypted and mDNS-discoverable on the LAN. Treat `-ndi` as explicit opt-in; use `-ndi-groups` on shared Wi‑Fi. Attribution: [ndi.video](https://ndi.video/).

Budget: full-bandwidth UYVY NDI is roughly 60–125 Mbps per 720p/1080p source — fine on loopback; prefer wired gigabit between PCs.

## Requirements

- Go 1.24+ (toolchain auto-downloads if needed)
- `ffmpeg` on PATH
- Phone and PC on the same Wi‑Fi/LAN
- Phone browser with `getUserMedia` + WebRTC (Safari on iOS is fine)
- Linux virtual cam: `v4l2loopback`; Linux mic hear-back: Pulse/PipeWire or ALSA
- NDI: separate NDI runtime when using `-ndi`

## One-time: v4l2loopback (Linux)

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
4. Quit Safari, open the pairing URL / scan the QR — the phone page walks through certificate trust in plain language (no README needed).

If your LAN IP changes, `rm -rf certs`, restart, reinstall the CA from the phone page.

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

- See [SAFETY.md](./SAFETY.md) for consent, NDI LAN risk, codecs, and trademark notes.
- LAN-only media: no STUN/TURN (`iceServers: []`).
- Control UI is not LAN-reachable by default (`-control 127.0.0.1:8444`).
- Active device drives v4l2 (Linux) when it has video; with `-ndi`, **every** live camera phone publishes an NDI source.
- Virtual cam path accepts H264 or VP8 (H264 default for phone battery).
- Audio path (Linux): Opus RTP → Ogg → ffmpeg → Pulse/ALSA.
- **iPhone → Linux Chrome preview:** Chrome often can’t decode H264 → black `<video>`. Firefox usually works. OBS/Zoom use v4l2 or NDI.
