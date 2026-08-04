(() => {
  const remote = document.getElementById("remote");
  const statusEl = document.getElementById("status");

  const params = new URLSearchParams(location.search);
  const deviceID = (params.get("id") || "").trim();

  /** @type {RTCPeerConnection | null} */
  let pc = null;
  /** @type {WebSocket | null} */
  let ws = null;
  let offering = false;
  /** @type {WakeLockSentinel | null} */
  let wakeLock = null;

  function setStatus(text, show = true) {
    if (!show || !text) {
      statusEl.hidden = true;
      statusEl.textContent = "";
      return;
    }
    statusEl.hidden = false;
    statusEl.textContent = text;
  }

  async function requestWakeLock() {
    if (!("wakeLock" in navigator)) return;
    try {
      wakeLock = await navigator.wakeLock.request("screen");
      wakeLock.addEventListener("release", () => {
        wakeLock = null;
      });
    } catch {
      /* ignore */
    }
  }

  function wsURL() {
    const proto = location.protocol === "https:" ? "wss:" : "ws:";
    return `${proto}//${location.host}/ws?role=view&id=${encodeURIComponent(deviceID)}`;
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
    pc.addTransceiver("audio", { direction: "recvonly" });

    pc.ontrack = (ev) => {
      let stream = remote.srcObject;
      if (!(stream instanceof MediaStream)) {
        stream = new MediaStream();
        remote.srcObject = stream;
      }
      stream.addTrack(ev.track);
      remote.muted = false;
      remote.play().catch(() => {});
      setStatus("", false);
      requestWakeLock();
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
      if (pc.connectionState === "failed") {
        setStatus("WebRTC failed — refresh");
      }
    };
  }

  async function sendOffer() {
    if (offering || !pc) return;
    offering = true;
    try {
      const offer = await pc.createOffer();
      await pc.setLocalDescription(offer);
      send({ type: "offer", sdp: offer.sdp });
    } catch (err) {
      console.error(err);
      setStatus(err.message || String(err));
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
      if (msg.message === "waiting-for-phone" || msg.message === "phone-disconnected") {
        if (pc) {
          pc.close();
          pc = null;
        }
        remote.srcObject = null;
        setStatus(msg.message === "phone-disconnected" ? "Phone disconnected…" : "Waiting for phone…");
        return;
      }
      if (msg.message === "track-ready") {
        setStatus("Connecting…");
        createPC();
        await sendOffer();
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
      setStatus(msg.message || "Server error");
    }
  }

  function connect() {
    if (!deviceID) {
      setStatus("Missing ?id= device id");
      return;
    }
    document.title = `Tether — ${deviceID}`;
    setStatus("Connecting…");
    ws = new WebSocket(wsURL());
    ws.addEventListener("message", onSignal);
    ws.addEventListener("close", () => {
      setStatus("Disconnected — refreshing…");
      setTimeout(() => location.reload(), 1500);
    });
    ws.addEventListener("error", () => setStatus("WebSocket error"));
  }

  document.addEventListener("visibilitychange", () => {
    if (document.visibilityState === "visible") requestWakeLock();
  });

  connect();
})();
