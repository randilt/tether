(() => {
  const statusEl = document.getElementById("status");
  const statsEl = document.getElementById("stats");
  const remote = document.getElementById("remote");
  const listEl = document.getElementById("devices");
  const emptyEl = document.getElementById("device-empty");
  const pairCodeEl = document.getElementById("pair-code");
  const pairURLEl = document.getElementById("pair-url");
  const pairHintEl = document.getElementById("pair-hint");
  const copyBtn = document.getElementById("copy-url");
  const v4l2Banner = document.getElementById("v4l2-banner");
  const v4l2Msg = document.getElementById("v4l2-msg");
  const v4l2Cmd = document.getElementById("v4l2-cmd");
  const v4l2Copy = document.getElementById("v4l2-copy");

  /** @type {RTCPeerConnection | null} */
  let pc = null;
  /** @type {WebSocket | null} */
  let ws = null;
  let offering = false;
  /** @type {number | null} */
  let statsTimer = null;
  /** @type {number | null} */
  let pairTimer = null;
  /** @type {Array<{id:string,name:string,capability:string,state:string,active:boolean}>} */
  let devices = [];
  let pairURL = "";
  let v4l2Command = "";
  let pairExpiresAt = 0;

  function setStatus(text, state = "wait") {
    statusEl.textContent = text;
    statusEl.dataset.state = state;
  }

  function updatePairHint() {
    if (!pairExpiresAt) {
      pairHintEl.textContent = "Code expires in 10 minutes or after the first phone connects.";
      return;
    }
    const sec = Math.max(0, Math.floor(pairExpiresAt - Date.now() / 1000));
    const m = Math.floor(sec / 60);
    const s = sec % 60;
    pairHintEl.textContent =
      `Expires in ${m}:${String(s).padStart(2, "0")} or after first phone connects. New codes appear here automatically.`;
  }

  function renderPair(msg) {
    if (msg.code) {
      pairCodeEl.textContent = msg.code;
      pairCodeEl.classList.remove("pair-flash");
      void pairCodeEl.offsetWidth;
      pairCodeEl.classList.add("pair-flash");
    }
    if (msg.url) {
      pairURL = msg.url;
      pairURLEl.textContent = msg.url;
    }
    if (msg.expiresAt) {
      pairExpiresAt = msg.expiresAt;
      updatePairHint();
      if (pairTimer != null) clearInterval(pairTimer);
      pairTimer = setInterval(updatePairHint, 1000);
    }
  }

  function renderV4L2(msg) {
    const available = msg.available !== false;
    if (available || !msg.device) {
      v4l2Banner.hidden = true;
      v4l2Command = "";
      return;
    }
    v4l2Banner.hidden = false;
    v4l2Msg.textContent = msg.message || `Virtual camera not available — ${msg.device} is missing`;
    v4l2Command = msg.command || "";
    v4l2Cmd.textContent = v4l2Command;
    v4l2Cmd.hidden = !v4l2Command;
    v4l2Copy.hidden = !v4l2Command;
  }

  function wsURL() {
    const proto = location.protocol === "https:" ? "wss:" : "ws:";
    return `${proto}//${location.host}/ws?role=control`;
  }

  function send(msg) {
    if (ws && ws.readyState === WebSocket.OPEN) {
      ws.send(JSON.stringify(msg));
    }
  }

  function renderDevices() {
    listEl.innerHTML = "";
    emptyEl.hidden = devices.length > 0;
    for (const d of devices) {
      const li = document.createElement("li");
      const btn = document.createElement("button");
      btn.type = "button";
      btn.className = "device" + (d.active ? " active" : "");
      btn.disabled = d.state !== "live";
      btn.innerHTML =
        `<span class="device-name">${escapeHtml(d.name)}</span>` +
        `<span class="device-meta">${escapeHtml(d.capability)} · ${escapeHtml(d.state)}` +
        (d.active ? " · active" : "") +
        `</span>` +
        `<span class="device-id">${escapeHtml(d.id)}</span>`;
      btn.addEventListener("click", () => {
        if (d.active) return;
        send({ type: "select", id: d.id });
        setStatus(`Switching to ${d.name}…`, "wait");
      });
      li.appendChild(btn);
      listEl.appendChild(li);
    }
  }

  function escapeHtml(s) {
    return String(s)
      .replace(/&/g, "&amp;")
      .replace(/</g, "&lt;")
      .replace(/>/g, "&gt;")
      .replace(/"/g, "&quot;");
  }

  function stopStats() {
    if (statsTimer != null) {
      clearInterval(statsTimer);
      statsTimer = null;
    }
  }

  function startStats() {
    stopStats();
    let ticks = 0;
    statsTimer = setInterval(async () => {
      if (!pc) return;
      ticks += 1;
      let inbound = null;
      const report = await pc.getStats();
      report.forEach((r) => {
        if (r.type === "inbound-rtp" && (r.kind === "video" || r.mediaType === "video")) {
          inbound = r;
        }
      });
      if (!inbound) {
        statsEl.textContent = "stats: no inbound-rtp yet";
        return;
      }
      const codec = inbound.codecId ? report.get(inbound.codecId) : null;
      const mime = codec?.mimeType || "?";
      const frames = inbound.framesDecoded ?? inbound.framesReceived ?? 0;
      const bytes = inbound.bytesReceived ?? 0;
      const w = remote.videoWidth || 0;
      const h = remote.videoHeight || 0;
      statsEl.textContent = `${mime} · ${w}x${h} · frames=${frames} · bytes=${bytes}`;
      if (ticks >= 3 && bytes > 1000 && w === 0 && h === 0 && frames === 0) {
        setStatus(
          "Receiving RTP but no picture — Linux Chrome often lacks H264. Try Firefox for /control.",
          "error",
        );
      }
    }, 1000);
  }

  function createPC() {
    stopStats();
    if (pc) {
      pc.close();
      pc = null;
    }
    remote.srcObject = null;
    statsEl.textContent = "";

    pc = new RTCPeerConnection({ iceServers: [] });
    pc.addTransceiver("video", { direction: "recvonly" });

    pc.ontrack = (ev) => {
      const stream = ev.streams[0] || new MediaStream([ev.track]);
      remote.srcObject = stream;
      remote.muted = true;
      remote.play().catch(() => {});
      const active = devices.find((d) => d.active);
      setStatus(active ? `Live — ${active.name}` : "Live — phone camera", "live");
      startStats();
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

    if (msg.type === "pair") {
      renderPair(msg);
      return;
    }

    if (msg.type === "v4l2") {
      renderV4L2(msg);
      return;
    }

    if (msg.type === "devices") {
      devices = Array.isArray(msg.devices) ? msg.devices : [];
      renderDevices();
      return;
    }

    if (msg.type === "status") {
      if (msg.message === "waiting-for-phone") {
        stopStats();
        if (pc) {
          pc.close();
          pc = null;
        }
        remote.srcObject = null;
        setStatus("Waiting for phone to start…", "wait");
        return;
      }
      if (msg.message === "audio-only") {
        stopStats();
        if (pc) {
          pc.close();
          pc = null;
        }
        remote.srcObject = null;
        statsEl.textContent = "";
        const active = devices.find((d) => d.active);
        setStatus(
          active
            ? `Active mic — ${active.name} (listen on PC speakers / audio sink)`
            : "Active mic — listen on PC speakers / audio sink",
          "live",
        );
        return;
      }
      if (msg.message === "track-ready") {
        setStatus("Active track ready — connecting…", "wait");
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
      stopStats();
      setStatus("Signaling disconnected — refreshing…", "error");
      setTimeout(() => location.reload(), 1500);
    });
    ws.addEventListener("error", () => {
      setStatus("WebSocket error", "error");
    });
  }

  copyBtn.addEventListener("click", async () => {
    if (!pairURL) return;
    try {
      await navigator.clipboard.writeText(pairURL);
      copyBtn.textContent = "Copied";
      setTimeout(() => { copyBtn.textContent = "Copy phone URL"; }, 1500);
    } catch {
      copyBtn.textContent = "Copy failed";
    }
  });

  v4l2Copy.addEventListener("click", async () => {
    if (!v4l2Command) return;
    try {
      await navigator.clipboard.writeText(v4l2Command);
      v4l2Copy.textContent = "Copied";
      setTimeout(() => { v4l2Copy.textContent = "Copy command"; }, 1500);
    } catch {
      v4l2Copy.textContent = "Copy failed";
    }
  });

  connect();
})();
