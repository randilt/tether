(() => {
  const statusEl = document.getElementById("status");
  const preview = document.getElementById("preview");
  const startBtn = document.getElementById("start");
  const stopBtn = document.getElementById("stop");

  /** @type {RTCPeerConnection | null} */
  let pc = null;
  /** @type {WebSocket | null} */
  let ws = null;
  /** @type {MediaStream | null} */
  let localStream = null;
  let starting = false;

  function setStatus(text, state = "wait") {
    statusEl.textContent = text;
    statusEl.dataset.state = state;
  }

  function wsURL() {
    const proto = location.protocol === "https:" ? "wss:" : "ws:";
    return `${proto}//${location.host}/ws?role=phone`;
  }

  function send(msg) {
    if (ws && ws.readyState === WebSocket.OPEN) {
      ws.send(JSON.stringify(msg));
    }
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
    ws.addEventListener("close", () => {
      setStatus("Signaling disconnected", "error");
    });
    await new Promise((resolve, reject) => {
      const t = setTimeout(() => reject(new Error("ws timeout")), 8000);
      ws.addEventListener("open", () => { clearTimeout(t); resolve(); }, { once: true });
      ws.addEventListener("error", () => { clearTimeout(t); reject(new Error("ws error")); }, { once: true });
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

    try {
      setStatus("Requesting camera…", "wait");
      localStream = await navigator.mediaDevices.getUserMedia({
        audio: false,
        video: {
          facingMode: { ideal: "environment" },
          width: { ideal: 1280 },
          height: { ideal: 720 },
        },
      });
      preview.srcObject = localStream;

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
        if (s === "connected") setStatus("Live — sending camera", "live");
        else if (s === "failed") setStatus("WebRTC failed", "error");
        else if (s === "disconnected") setStatus("Disconnected", "error");
      };

      const offer = await pc.createOffer();
      await pc.setLocalDescription(offer);
      send({ type: "offer", sdp: offer.sdp });

      stopBtn.disabled = false;
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
    if (pc) {
      pc.close();
      pc = null;
    }
    if (ws) {
      ws.close();
      ws = null;
    }
    startBtn.disabled = false;
    setStatus("Stopped", "wait");
  }

  startBtn.addEventListener("click", start);
  stopBtn.addEventListener("click", stop);
})();
