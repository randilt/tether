(() => {
  const statusEl = document.getElementById("status");
  const remote = document.getElementById("remote");

  /** @type {RTCPeerConnection | null} */
  let pc = null;
  /** @type {WebSocket | null} */
  let ws = null;
  let offering = false;

  function setStatus(text, state = "wait") {
    statusEl.textContent = text;
    statusEl.dataset.state = state;
  }

  function wsURL() {
    const proto = location.protocol === "https:" ? "wss:" : "ws:";
    return `${proto}//${location.host}/ws?role=viewer`;
  }

  function send(msg) {
    if (ws && ws.readyState === WebSocket.OPEN) {
      ws.send(JSON.stringify(msg));
    }
  }

  function createPC() {
    if (pc) {
      pc.close();
      pc = null;
    }
    remote.srcObject = null;

    pc = new RTCPeerConnection({ iceServers: [] });
    pc.addTransceiver("video", { direction: "recvonly" });

    pc.ontrack = (ev) => {
      const stream = ev.streams[0] || new MediaStream([ev.track]);
      remote.srcObject = stream;
      remote.play().catch(() => {});
      setStatus("Live — phone camera", "live");
    };

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
      if (s === "failed") setStatus("WebRTC failed — refresh", "error");
      else if (s === "disconnected") setStatus("Disconnected", "error");
    };
  }

  async function sendOffer() {
    if (offering || !pc) return;
    offering = true;
    try {
      const offer = await pc.createOffer();
      await pc.setLocalDescription(offer);
      send({ type: "offer", sdp: offer.sdp });
      setStatus("Negotiating with server…", "wait");
    } catch (err) {
      console.error(err);
      setStatus(err.message || String(err), "error");
    } finally {
      offering = false;
    }
  }

  async function onSignal(ev) {
    let msg;
    try {
      msg = JSON.parse(ev.data);
    } catch {
      return;
    }

    if (msg.type === "status") {
      if (msg.message === "waiting-for-phone") {
        setStatus("Waiting for phone to start…", "wait");
        return;
      }
      if (msg.message === "track-ready") {
        setStatus("Phone track ready — connecting…", "wait");
        createPC();
        await sendOffer();
        return;
      }
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

  function connect() {
    setStatus("Connecting signaling…", "wait");
    ws = new WebSocket(wsURL());
    ws.addEventListener("message", onSignal);
    ws.addEventListener("open", () => {
      setStatus("Waiting for phone to start…", "wait");
    });
    ws.addEventListener("close", () => {
      setStatus("Signaling disconnected — refreshing…", "error");
      setTimeout(() => location.reload(), 1500);
    });
    ws.addEventListener("error", () => {
      setStatus("WebSocket error", "error");
    });
  }

  connect();
})();
