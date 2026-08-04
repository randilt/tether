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

  /** @type {RTCPeerConnection | null} */
  let pc = null;
  /** @type {WebSocket | null} */
  let ws = null;
  /** @type {MediaStream | null} */
  let localStream = null;
  let starting = false;
  let pairToken = "";

  function selectedMode() {
    const el = document.querySelector('input[name="mode"]:checked');
    return el ? el.value : "video";
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
  }

  function showConnected() {
    camEl.hidden = false; // keep preview if video
    connectedEl.hidden = false;
    camActions.hidden = true;
    headerSub.textContent = "You’re all set.";
    document.querySelectorAll('input[name="mode"]').forEach((el) => { el.disabled = true; });
  }

  function wsURL() {
    const proto = location.protocol === "https:" ? "wss:" : "ws:";
    const q = new URLSearchParams({ role: "phone", t: pairToken });
    const name = new URLSearchParams(location.search).get("name");
    if (name) q.set("name", name);
    return `${proto}//${location.host}/ws?${q}`;
  }

  function send(msg) {
    if (ws && ws.readyState === WebSocket.OPEN) {
      ws.send(JSON.stringify(msg));
    }
  }

  function mediaConstraints(mode) {
    const video = {
      facingMode: { ideal: "environment" },
      width: { ideal: 1280 },
      height: { ideal: 720 },
    };
    if (mode === "audio") return { audio: true, video: false };
    if (mode === "av") return { audio: true, video };
    return { audio: false, video };
  }

  function looksLikeCertOrSecureIssue(err) {
    const name = err && err.name ? err.name : "";
    const msg = (err && err.message ? err.message : String(err)).toLowerCase();
    if (!window.isSecureContext) return true;
    if (name === "SecurityError" || name === "NotSupportedError") return true;
    if (msg.includes("secure") || msg.includes("ssl") || msg.includes("certificate")) return true;
    // getUserMedia often fails oddly on untrusted iOS certs
    if (name === "NotAllowedError" && /ios|iphone|ipad/i.test(navigator.userAgent)) {
      // Could be permission deny OR cert — nudge trust if not marked done
      try {
        return localStorage.getItem(CERT_FLAG) !== "1";
      } catch {
        return true;
      }
    }
    return false;
  }

  async function ensureWS() {
    if (ws && (ws.readyState === WebSocket.OPEN || ws.readyState === WebSocket.CONNECTING)) {
      if (ws.readyState === WebSocket.OPEN) return;
      await new Promise((resolve, reject) => {
        const t = setTimeout(() => reject(new Error("ws timeout")), 8000);
        ws.addEventListener("open", () => { clearTimeout(t); resolve(); }, { once: true });
        ws.addEventListener("error", () => { clearTimeout(t); reject(new Error("ws error")); }, { once: true });
      });
      return;
    }

    ws = new WebSocket(wsURL());
    ws.addEventListener("message", onSignal);
    ws.addEventListener("close", (ev) => {
      if (connectedEl.hidden === false) {
        showCam(pairToken);
      }
      if (ev.code === 1008 || ev.code === 1006) {
        setStatus("Pairing code expired or wrong — get a new code from the PC", "error");
      } else {
        setStatus("Disconnected from PC", "error");
      }
      camActions.hidden = false;
    });
    await new Promise((resolve, reject) => {
      const t = setTimeout(() => reject(new Error("ws timeout")), 8000);
      ws.addEventListener("open", () => { clearTimeout(t); resolve(); }, { once: true });
      ws.addEventListener("error", () => {
        clearTimeout(t);
        reject(new Error("Could not connect — pairing code may be wrong or expired"));
      });
    });
  }

  async function onSignal(ev) {
    let msg;
    try {
      msg = JSON.parse(ev.data);
    } catch {
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

  async function start() {
    if (starting) return;
    starting = true;
    startBtn.disabled = true;
    const mode = selectedMode();

    try {
      setStatus("Waiting for permission… tap Allow", "wait");
      localStream = await navigator.mediaDevices.getUserMedia(mediaConstraints(mode));
      markCertDone();

      const hasVideo = localStream.getVideoTracks().length > 0;
      previewWrap.hidden = !hasVideo;
      preview.srcObject = hasVideo ? localStream : null;

      setStatus("Connecting to your PC…", "wait");
      await ensureWS();

      pc = new RTCPeerConnection({ iceServers: [] });
      for (const track of localStream.getTracks()) {
        pc.addTrack(track, localStream);
      }

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
        const s = pc.connectionState;
        if (s === "connected") {
          setStatus("Live", "live");
          showConnected();
        } else if (s === "failed") {
          setStatus("Connection failed — tap Start to try again", "error");
          camActions.hidden = false;
          connectedEl.hidden = true;
        } else if (s === "disconnected") {
          setStatus("Disconnected", "error");
          camActions.hidden = false;
          connectedEl.hidden = true;
        }
      };

      const offer = await pc.createOffer();
      await pc.setLocalDescription(offer);
      send({ type: "offer", sdp: offer.sdp });

      stopBtn.disabled = false;
      document.querySelectorAll('input[name="mode"]').forEach((el) => { el.disabled = true; });
      setStatus("Almost there…", "wait");
    } catch (err) {
      console.error(err);
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

  async function stop(resetModes = true) {
    stopBtn.disabled = true;
    connectedEl.hidden = true;
    camActions.hidden = false;
    if (localStream) {
      for (const t of localStream.getTracks()) t.stop();
      localStream = null;
    }
    preview.srcObject = null;
    previewWrap.hidden = false;
    if (pc) {
      pc.close();
      pc = null;
    }
    if (ws) {
      ws.close();
      ws = null;
    }
    if (resetModes) {
      document.querySelectorAll('input[name="mode"]').forEach((el) => { el.disabled = false; });
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
    // After download, nudge them toward Settings without leaving the page copy.
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

  startBtn.addEventListener("click", start);
  stopBtn.addEventListener("click", () => stop(true));
  disconnectBtn.addEventListener("click", () => stop(true));

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
