(() => {
  const CERT_FLAG = "tether_cert_trusted_v1";

  const trustEl = document.getElementById("trust");
  const gateEl = document.getElementById("gate");
  const camEl = document.getElementById("cam");
  const connectedEl = document.getElementById("connected");
  const headerSub = document.getElementById("header-sub");
  const pairForm = document.getElementById("pair-form");
  const pairInput = document.getElementById("pair-input");
  const trustDone = document.getElementById("trust-done");
  const trustWarn = document.getElementById("trust-warn");
  const certLink = document.getElementById("cert-link");
  const statusEl = document.getElementById("status");
  const preview = document.getElementById("preview");
  const previewWrap = document.getElementById("preview-wrap");
  const startBtn = document.getElementById("start");
  const stopBtn = document.getElementById("stop");
  const disconnectBtn = document.getElementById("disconnect");
  const camActions = document.getElementById("cam-actions");
  const wakeFallback = document.getElementById("wake-fallback");
  const orientPicker = document.getElementById("orient-picker");

  /** @type {RTCPeerConnection | null} */
  let pc = null;
  /** @type {WebSocket | null} */
  let ws = null;
  /** @type {MediaStream | null} */
  let localStream = null;
  let starting = false;
  let pairToken = "";
  /** @type {string} */
  let resumeToken = "";
  /** User wants an uplink; auto-reconnect after lock/background. */
  let wantLive = false;
  /** @type {number | null} */
  let reconnectTimer = null;
  let reconnectAttempt = 0;
  let wsGen = 0;
  /** @type {WakeLockSentinel | null} */
  let wakeLock = null;
  /** @type {"h264" | "vp8"} */
  let preferCodec = "h264";

  function selectedMode() {
    const el = document.querySelector('input[name="mode"]:checked');
    return el ? el.value : "video";
  }

  /** @returns {"portrait"|"landscape"} */
  function selectedOrient() {
    const el = document.querySelector('input[name="orient"]:checked');
    return el && el.value === "landscape" ? "landscape" : "portrait";
  }

  function syncOrientPicker() {
    if (!orientPicker) return;
    orientPicker.hidden = selectedMode() === "audio";
  }

  async function lockPhoneOrient(orient) {
    try {
      const o = screen.orientation;
      if (!o || typeof o.lock !== "function") return;
      await o.lock(orient === "landscape" ? "landscape" : "portrait");
    } catch {
      /* iOS / unsigned sites often reject; physical rotate still works */
    }
  }

  function unlockPhoneOrient() {
    try {
      screen.orientation?.unlock?.();
    } catch {
      /* ignore */
    }
  }

  function setStatus(text, state = "wait") {
    statusEl.textContent = text;
    statusEl.dataset.state = state;
  }

  function hideAll() {
    trustEl.hidden = true;
    gateEl.hidden = true;
    camEl.hidden = true;
    connectedEl.hidden = true;
  }

  function needsCertSetup() {
    if (!window.isSecureContext) return true;
    try {
      if (localStorage.getItem(CERT_FLAG) === "1") return false;
    } catch {
      /* private mode */
    }
    return true;
  }

  function markCertDone() {
    try {
      localStorage.setItem(CERT_FLAG, "1");
    } catch {
      /* ignore */
    }
  }

  function showTrust(extraWarn) {
    hideAll();
    trustEl.hidden = false;
    headerSub.textContent = "First, trust this PC (one time).";
    if (extraWarn) {
      trustWarn.hidden = false;
      trustWarn.textContent = extraWarn;
    } else {
      trustWarn.hidden = true;
    }
  }

  function showGate() {
    hideAll();
    gateEl.hidden = false;
    headerSub.textContent = "Enter the code from your PC.";
  }

  function showCam(token) {
    pairToken = token.toUpperCase().trim();
    hideAll();
    camEl.hidden = false;
    connectedEl.hidden = true;
    camActions.hidden = false;
    headerSub.textContent = "Share camera or mic with your PC.";
    syncOrientPicker();
  }

  function showConnected() {
    camEl.hidden = false;
    connectedEl.hidden = false;
    camActions.hidden = true;
    headerSub.textContent = "You’re all set.";
    document.querySelectorAll('input[name="mode"]').forEach((el) => { el.disabled = true; });
    document.querySelectorAll('input[name="orient"]').forEach((el) => { el.disabled = true; });
    acquireWakeLock();
  }

  function showReconnecting() {
    camEl.hidden = false;
    connectedEl.hidden = true;
    camActions.hidden = true;
    headerSub.textContent = "Reconnecting to your PC…";
    setStatus("Phone disconnected — reconnecting…", "wait");
  }

  // Keep screen on while live. Browser still kills capture if you switch apps —
  // Wake Lock only helps while this page stays foregrounded.
  async function acquireWakeLock() {
    if (!wantLive) return;
    startWakeFallback();
    if (!("wakeLock" in navigator) || !navigator.wakeLock) return;
    try {
      wakeLock = await navigator.wakeLock.request("screen");
      wakeLock.addEventListener("release", () => {
        wakeLock = null;
      });
    } catch (err) {
      console.warn("wakeLock", err);
    }
  }

  async function releaseWakeLock() {
    stopWakeFallback();
    if (!wakeLock) return;
    try {
      await wakeLock.release();
    } catch {
      /* ignore */
    }
    wakeLock = null;
  }

  function startWakeFallback() {
    if (!wakeFallback) return;
    // Tiny silent looping video — fallback when Wake Lock silently no-ops (older iOS PWA).
    if (!wakeFallback.src) {
      // 1x1 black pixel webm is heavy; use a canvas-generated silent stream instead.
      try {
        const c = document.createElement("canvas");
        c.width = 2;
        c.height = 2;
        const ctx = c.getContext("2d");
        if (ctx) {
          ctx.fillStyle = "#000";
          ctx.fillRect(0, 0, 2, 2);
        }
        const stream = c.captureStream(1);
        wakeFallback.srcObject = stream;
      } catch {
        return;
      }
    }
    wakeFallback.hidden = true;
    wakeFallback.muted = true;
    wakeFallback.play().catch(() => {});
  }

  function stopWakeFallback() {
    if (!wakeFallback) return;
    wakeFallback.pause();
    if (wakeFallback.srcObject) {
      for (const t of wakeFallback.srcObject.getTracks()) t.stop();
      wakeFallback.srcObject = null;
    }
  }

  function wsURL(useResume) {
    const proto = location.protocol === "https:" ? "wss:" : "ws:";
    const q = new URLSearchParams({ role: "phone" });
    if (useResume && resumeToken) {
      q.set("resume", resumeToken);
    } else {
      q.set("t", pairToken);
    }
    const name = new URLSearchParams(location.search).get("name");
    if (name) q.set("name", name);
    return `${proto}//${location.host}/ws?${q}`;
  }

  function send(msg) {
    if (ws && ws.readyState === WebSocket.OPEN) {
      ws.send(JSON.stringify(msg));
    }
  }

  function mediaConstraints(mode, orient) {
    const landscape = orient === "landscape";
    const video = {
      facingMode: { ideal: "environment" },
      width: { ideal: landscape ? 1920 : 1080 },
      height: { ideal: landscape ? 1080 : 1920 },
      aspectRatio: { ideal: landscape ? 16 / 9 : 9 / 16 },
      frameRate: { ideal: 30 },
    };
    if (mode === "audio") return { audio: true, video: false };
    if (mode === "av") return { audio: true, video };
    return { audio: false, video };
  }

  /** Push the browser encoder toward a usable bitrate (default WebRTC is call-tier). */
  async function bumpVideoEncode(peer) {
    if (!peer) return;
    for (const sender of peer.getSenders()) {
      if (!sender.track || sender.track.kind !== "video") continue;
      const params = sender.getParameters();
      if (!params.encodings || params.encodings.length === 0) {
        params.encodings = [{}];
      }
      // ~4 Mbps @ 1080p30 — still LAN-friendly; Safari may clamp.
      params.encodings[0].maxBitrate = 4_000_000;
      params.encodings[0].maxFramerate = 30;
      try {
        await sender.setParameters(params);
      } catch (err) {
        console.warn("setParameters bitrate", err);
      }
    }
  }

  function looksLikeCertOrSecureIssue(err) {
    const name = err && err.name ? err.name : "";
    const msg = (err && err.message ? err.message : String(err)).toLowerCase();
    if (!window.isSecureContext) return true;
    if (name === "SecurityError" || name === "NotSupportedError") return true;
    if (msg.includes("secure") || msg.includes("ssl") || msg.includes("certificate")) return true;
    if (name === "NotAllowedError" && /ios|iphone|ipad/i.test(navigator.userAgent)) {
      try {
        return localStorage.getItem(CERT_FLAG) !== "1";
      } catch {
        return true;
      }
    }
    return false;
  }

  function clearReconnectTimer() {
    if (reconnectTimer != null) {
      clearTimeout(reconnectTimer);
      reconnectTimer = null;
    }
  }

  function scheduleReconnect(reason) {
    if (!wantLive || !resumeToken || starting) return;
    if (reconnectAttempt >= 20) {
      wantLive = false;
      clearReconnectTimer();
      setStatus("Could not resume — get a new code from the PC", "error");
      camActions.hidden = false;
      connectedEl.hidden = true;
      return;
    }
    clearReconnectTimer();
    const delay = Math.min(8000, 500 + reconnectAttempt * 700);
    reconnectAttempt += 1;
    showReconnecting();
    setStatus(`Phone disconnected — reconnecting… (${reason})`, "wait");
    reconnectTimer = setTimeout(() => {
      reconnectTimer = null;
      reconnect().catch((err) => {
        console.warn("reconnect", err);
        scheduleReconnect("retry");
      });
    }, delay);
  }

  function tearDownPeer() {
    if (pc) {
      pc.close();
      pc = null;
    }
    if (ws) {
      const old = ws;
      ws = null;
      try { old.close(); } catch { /* ignore */ }
    }
  }

  async function ensureMedia() {
    const live = localStream && localStream.getTracks().some((t) => t.readyState === "live");
    if (live) return;
    if (localStream) {
      for (const t of localStream.getTracks()) t.stop();
      localStream = null;
    }
    const mode = selectedMode();
    const orient = selectedOrient();
    try {
      localStream = await navigator.mediaDevices.getUserMedia(mediaConstraints(mode, orient));
    } catch (err) {
      // Some browsers reject aspectRatio; retry with size ideals only.
      if (mode !== "audio") {
        const loose = mediaConstraints(mode, orient);
        if (loose.video && typeof loose.video === "object") {
          delete loose.video.aspectRatio;
        }
        localStream = await navigator.mediaDevices.getUserMedia(loose);
      } else {
        throw err;
      }
    }
    markCertDone();
    const hasVideo = localStream.getVideoTracks().length > 0;
    previewWrap.hidden = !hasVideo;
    preview.srcObject = hasVideo ? localStream : null;
    if (hasVideo) await lockPhoneOrient(orient);
  }

  async function openWS(useResume) {
    const gen = ++wsGen;
    tearDownPeer();
    const sock = new WebSocket(wsURL(useResume));
    ws = sock;
    sock.addEventListener("message", onSignal);
    sock.addEventListener("close", (ev) => {
      if (gen !== wsGen) return;
      if (!wantLive) {
        setStatus("Disconnected from PC", "error");
        camActions.hidden = false;
        return;
      }
      if (ev.code === 1008) {
        wantLive = false;
        resumeToken = "";
        setStatus("Pairing/resume rejected — get a new code from the PC", "error");
        camActions.hidden = false;
        connectedEl.hidden = true;
        return;
      }
      scheduleReconnect("signaling closed");
    });
    await new Promise((resolve, reject) => {
      const t = setTimeout(() => reject(new Error("ws timeout")), 8000);
      sock.addEventListener("open", () => { clearTimeout(t); resolve(); }, { once: true });
      sock.addEventListener("error", () => {
        clearTimeout(t);
        reject(new Error(useResume
          ? "Resume failed — unlock and wait, or get a new code"
          : "Could not connect — pairing code may be wrong or expired"));
      });
    });
    if (gen !== wsGen) {
      throw new Error("ws superseded");
    }
  }

  async function onSignal(ev) {
    let msg;
    try {
      msg = JSON.parse(ev.data);
    } catch {
      return;
    }

    if (msg.type === "status" && msg.message === "ready") {
      if (msg.resume) resumeToken = msg.resume;
      if (msg.codec === "vp8" || msg.codec === "h264") preferCodec = msg.codec;
      return;
    }

    if (msg.type === "codec") {
      if (msg.codec === "vp8" || msg.codec === "h264") preferCodec = msg.codec;
      return;
    }

    if (msg.type === "answer" && pc) {
      await pc.setRemoteDescription({ type: "answer", sdp: msg.sdp });
      return;
    }

    if (msg.type === "candidate" && pc && msg.candidate) {
      try {
        await pc.addIceCandidate({
          candidate: msg.candidate,
          sdpMid: msg.sdpMid ?? undefined,
          sdpMLineIndex: msg.sdpMLineIndex ?? undefined,
        });
      } catch (err) {
        console.warn("addIceCandidate", err);
      }
      return;
    }

    if (msg.type === "error") {
      setStatus(msg.message || "Server error", "error");
    }
  }

  function preferVideoCodec(peer) {
    if (!peer || typeof RTCRtpSender === "undefined" || !RTCRtpSender.getCapabilities) return;
    const caps = RTCRtpSender.getCapabilities("video");
    if (!caps || !caps.codecs || !caps.codecs.length) return;
    const want = preferCodec === "vp8" ? "video/vp8" : "video/h264";
    const ordered = [...caps.codecs].sort((a, b) => {
      const am = (a.mimeType || "").toLowerCase() === want ? 0 : 1;
      const bm = (b.mimeType || "").toLowerCase() === want ? 0 : 1;
      return am - bm;
    });
    for (const t of peer.getTransceivers()) {
      if (t.sender && t.sender.track && t.sender.track.kind === "video") {
        try {
          t.setCodecPreferences(ordered);
        } catch (err) {
          console.warn("setCodecPreferences", err);
        }
      }
    }
  }

  async function bindPeerAndOffer() {
    pc = new RTCPeerConnection({ iceServers: [] });
    for (const track of localStream.getTracks()) {
      pc.addTrack(track, localStream);
    }
    preferVideoCodec(pc);

    pc.onicecandidate = (e) => {
      if (!e.candidate) return;
      send({
        type: "candidate",
        candidate: e.candidate.candidate,
        sdpMid: e.candidate.sdpMid,
        sdpMLineIndex: e.candidate.sdpMLineIndex,
      });
    };

    pc.onconnectionstatechange = () => {
      if (!pc) return;
      const s = pc.connectionState;
      if (s === "connected") {
        reconnectAttempt = 0;
        bumpVideoEncode(pc);
        setStatus("Live", "live");
        showConnected();
      } else if (s === "failed") {
        if (wantLive && resumeToken) {
          scheduleReconnect("webrtc failed");
        } else {
          setStatus("Connection failed — tap Start to try again", "error");
          camActions.hidden = false;
          connectedEl.hidden = true;
        }
      } else if (s === "disconnected") {
        if (wantLive) {
          showReconnecting();
        }
      }
    };

    const offer = await pc.createOffer();
    await pc.setLocalDescription(offer);
    send({ type: "offer", sdp: offer.sdp });
  }

  async function start() {
    if (starting) return;
    starting = true;
    startBtn.disabled = true;
    clearReconnectTimer();

    try {
      wantLive = true;
      setStatus("Waiting for permission… tap Allow", "wait");
      await ensureMedia();
      setStatus("Connecting to your PC…", "wait");
      await openWS(false);
      await bindPeerAndOffer();
      stopBtn.disabled = false;
      document.querySelectorAll('input[name="mode"]').forEach((el) => { el.disabled = true; });
      document.querySelectorAll('input[name="orient"]').forEach((el) => { el.disabled = true; });
      setStatus("Almost there…", "wait");
    } catch (err) {
      console.error(err);
      wantLive = false;
      if (looksLikeCertOrSecureIssue(err)) {
        try {
          localStorage.removeItem(CERT_FLAG);
        } catch {
          /* ignore */
        }
        showTrust(
          "Camera/mic was blocked. That usually means the certificate isn’t fully trusted yet. Finish the steps below, then try again.",
        );
      } else {
        setStatus(err.message || String(err), "error");
        startBtn.disabled = false;
      }
      await stop(false);
    } finally {
      starting = false;
    }
  }

  async function reconnect() {
    if (!wantLive || !resumeToken || starting) return;
    starting = true;
    try {
      showReconnecting();
      await ensureMedia();
      await openWS(true);
      await bindPeerAndOffer();
      stopBtn.disabled = false;
      setStatus("Reconnected — negotiating…", "wait");
    } finally {
      starting = false;
    }
  }

  async function stop(resetModes = true) {
    wantLive = false;
    resumeToken = "";
    reconnectAttempt = 0;
    clearReconnectTimer();
    await releaseWakeLock();
    unlockPhoneOrient();
    stopBtn.disabled = true;
    connectedEl.hidden = true;
    camActions.hidden = false;
    if (localStream) {
      for (const t of localStream.getTracks()) t.stop();
      localStream = null;
    }
    preview.srcObject = null;
    previewWrap.hidden = false;
    tearDownPeer();
    if (resetModes) {
      document.querySelectorAll('input[name="mode"]').forEach((el) => { el.disabled = false; });
      document.querySelectorAll('input[name="orient"]').forEach((el) => { el.disabled = false; });
      syncOrientPicker();
    }
    startBtn.disabled = false;
    if (!camEl.hidden) setStatus("Stopped — tap Start when ready", "wait");
  }

  function advancePastTrust() {
    markCertDone();
    const params = new URLSearchParams(location.search);
    const t = (params.get("t") || params.get("token") || "").trim();
    if (t) showCam(t);
    else showGate();
  }

  trustDone.addEventListener("click", advancePastTrust);

  certLink.addEventListener("click", () => {
    trustWarn.hidden = false;
    trustWarn.dataset.state = "wait";
    trustWarn.textContent = "Certificate downloading. Now open the Settings app and follow steps 2–6.";
  });

  pairForm.addEventListener("submit", (e) => {
    e.preventDefault();
    const code = pairInput.value.trim().toUpperCase();
    if (!code) return;
    const url = new URL(location.href);
    url.searchParams.set("t", code);
    location.href = url.toString();
  });

  document.querySelectorAll('input[name="mode"]').forEach((el) => {
    el.addEventListener("change", syncOrientPicker);
  });

  startBtn.addEventListener("click", start);
  stopBtn.addEventListener("click", () => stop(true));
  disconnectBtn.addEventListener("click", () => stop(true));

  document.addEventListener("visibilitychange", () => {
    if (document.visibilityState === "visible") {
      if (wantLive) acquireWakeLock();
      if (!wantLive || !resumeToken) return;
      const wsDead = !ws || ws.readyState !== WebSocket.OPEN;
      const pcDead = !pc || pc.connectionState === "failed" || pc.connectionState === "closed"
        || pc.connectionState === "disconnected";
      if (wsDead || pcDead) {
        scheduleReconnect("tab visible");
      }
      return;
    }
    // Hidden: OS will release wake lock; capture will stop — resume handles recovery.
  });

  window.addEventListener("pageshow", (ev) => {
    if (!ev.persisted) return;
    if (wantLive && resumeToken) scheduleReconnect("pageshow");
  });

  // Entry
  if (!window.isSecureContext) {
    showTrust("This page is not secure. Open the HTTPS link from your PC (scan the QR again).");
  } else if (needsCertSetup()) {
    showTrust();
  } else {
    const params = new URLSearchParams(location.search);
    const t = (params.get("t") || params.get("token") || "").trim();
    if (t) showCam(t);
    else showGate();
  }
})();
