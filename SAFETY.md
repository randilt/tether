# Safety and operator responsibilities

Tether is a **local** media bridge: phones stream to a PC on your LAN. You (the operator) are responsible for how that media is used.

## Consent and safeguarding

If you record or live-stream people — especially **minors** (school events, youth sports, etc.):

- Obtain appropriate **consent** (parents/guardians, school policy, venue rules).
- Follow local **image rights**, **privacy**, and **child-protection** laws.
- Tether does not collect accounts or cloud footage, but **does not** replace your legal or safeguarding duties.

A short notice appears on the control page when you first open it. Acknowledge it to dismiss (stored in browser `localStorage` only).

## Localhost views and OBS

Control and `/view` pages bind to **localhost** by default. Anyone with access to your PC can open those pages and see live phone feeds. Treat the PC like a camera control surface.

OBS Browser Source loads the view URL in-process — same trust boundary as a browser tab on this machine.

## Codecs

- Default phone encode is **H.264** (battery-friendly on iOS).
- **VP8** can be preferred via the control page toggle (royalty-free; may use more phone CPU/battery).

## Trademark

“Tether” as a product name should be checked against trademarks in your jurisdiction before commercial branding.
