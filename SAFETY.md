# Safety and operator responsibilities

Tether is a **local** media bridge: phones stream to a PC on your LAN. You (the operator) are responsible for how that media is used.

## Consent and safeguarding

If you record or live-stream people — especially **minors** (school events, youth sports, etc.):

- Obtain appropriate **consent** (parents/guardians, school policy, venue rules).
- Follow local **image rights**, **privacy**, and **child-protection** laws.
- Tether does not collect accounts or cloud footage, but **does not** replace your legal or safeguarding duties.

A short notice appears on the control page when you first open it. Acknowledge it to dismiss (stored in browser `localStorage` only).

## NDI on shared networks

`-ndi` publishes **unencrypted**, mDNS-discoverable video sources on the LAN. Anyone on the same Wi‑Fi who can run an NDI receiver may be able to view those feeds.

- Prefer a **dedicated / private** network for production.
- Use `-ndi-groups` so sources are not on the public default group.
- Do not enable NDI on untrusted guest networks without understanding this risk.

## Codecs

- Default phone encode is **H.264** (battery-friendly on iOS).
- **VP8** can be preferred via the control page toggle (royalty-free; may use more phone CPU/battery).
- Decoding uses **ffmpeg** installed on the PC; NDI’s wire format is SpeedHQ, not H.264.

## Trademark

“Tether” as a product name should be checked against trademarks in your jurisdiction before commercial branding. **NDI®** is a registered trademark of Vizrt Group; see [ndi.video](https://ndi.video/) for SDK attribution requirements.
