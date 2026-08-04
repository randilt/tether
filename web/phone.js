(() => {
  const gateEl = document.getElementById("gate");
  const camEl = document.getElementById("cam");
  const pairForm = document.getElementById("pair-form");
  const pairInput = document.getElementById("pair-input");
  const statusEl = document.getElementById("status");
  const preview = document.getElementById("preview");
  const previewWrap = document.getElementById("preview-wrap");
  const startBtn = document.getElementById("start");
  const stopBtn = document.getElementById("stop");

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

  function showCam(token) {
    pairToken = token.toUpperCase().trim();
    gateEl.hidden = true;
    camEl.hidden = false;
  }

  function showGate() {
    gateEl.hidden = false;
    camEl.hidden = true;
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
      if (ev.code === 1008 || ev.code === 1006) {
        setStatus("Pairing rejected — check the code on the PC", "error");
      } else {
        setStatus("Signaling disconnected", "error");
      }
    });
    await new Promise((resolve, reject) => {
      const t = setTimeout(() => reject(new Error("ws timeout")), 8000);
      ws.addEventListener("open", () => { clearTimeout(t); resolve(); }, { once: true });
      ws.addEventListener("error", () => {
        clearTimeout(t);
        reject(new Error("WebSocket failed — wrong pairing code?"));
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
      setStatus(mode === "audio" ? "Requesting microphone…" : "Requesting media…", "wait");
      localStream = await navigator.mediaDevices.getUserMedia(mediaConstraints(mode));

      const hasVideo = localStream.getVideoTracks().length > 0;
      previewWrap.hidden = !hasVideo;
      preview.srcObject = hasVideo ? localStream : null;

      setStatus("Connecting…", "wait");
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
          const label = mode === "audio" ? "mic" : mode === "av" ? "camera + mic" : "camera";
          setStatus(`Live — sending ${label}`, "live");
        } else if (s === "failed") setStatus("WebRTC failed", "error");
        else if (s === "disconnected") setStatus("Disconnected", "error");
      };

      const offer = await pc.createOffer();
      await pc.setLocalDescription(offer);
      send({ type: "offer", sdp: offer.sdp });

      stopBtn.disabled = false;
      document.querySelectorAll('input[name="mode"]').forEach((el) => { el.disabled = true; });
      setStatus("Negotiating…", "wait");
    } catch (err) {
      console.error(err);
      setStatus(err.message || String(err), "error");
      await stop();
      startBtn.disabled = false;
    } finally {
      starting = false;
    }
  }

  async function stop() {
    stopBtn.disabled = true;
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
    document.querySelectorAll('input[name="mode"]').forEach((el) => { el.disabled = false; });
    startBtn.disabled = false;
    setStatus("Stopped", "wait");
  }

  pairForm.addEventListener("submit", (e) => {
    e.preventDefault();
    const code = pairInput.value.trim().toUpperCase();
    if (!code) return;
    const url = new URL(location.href);
    url.searchParams.set("t", code);
    location.href = url.toString();
  });

  startBtn.addEventListener("click", start);
  stopBtn.addEventListener("click", stop);

  const params = new URLSearchParams(location.search);
  const t = (params.get("t") || params.get("token") || "").trim();
  if (t) {
    showCam(t);
  } else {
    showGate();
  }
})();
