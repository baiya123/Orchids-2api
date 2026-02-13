(() => {
  const imagineState = {
    running: false,
    mode: "auto",
    effectiveMode: "auto",
    taskIDs: [],
    wsSockets: [],
    sseStreams: [],
    imageCount: 0,
    latencySum: 0,
    latencyCount: 0,
    fallbackTimer: null,
  };

  function handleUnauthorized(res) {
    if (res && res.status === 401) {
      window.location.href = "./login.html";
      return true;
    }
    return false;
  }

  function detectImageMime(b64) {
    const raw = String(b64 || "");
    if (raw.startsWith("iVBOR")) return "image/png";
    if (raw.startsWith("/9j/")) return "image/jpeg";
    if (raw.startsWith("R0lGOD")) return "image/gif";
    return "image/jpeg";
  }

  function formatBytes(bytes) {
    const num = Number(bytes || 0);
    if (!Number.isFinite(num) || num <= 0) return "0 B";
    const units = ["B", "KB", "MB", "GB", "TB"];
    let value = num;
    let idx = 0;
    while (value >= 1024 && idx < units.length - 1) {
      value /= 1024;
      idx++;
    }
    return `${value.toFixed(value >= 10 ? 1 : 2)} ${units[idx]}`;
  }

  function formatTimeMS(ms) {
    const num = Number(ms || 0);
    if (!Number.isFinite(num) || num <= 0) return "-";
    return `${Math.round(num)} ms`;
  }

  function formatDateTime(ms) {
    const num = Number(ms || 0);
    if (!Number.isFinite(num) || num <= 0) return "-";
    return new Date(num).toLocaleString();
  }

  function setImagineStatus(text) {
    const el = document.getElementById("imagineStatus");
    if (el) el.textContent = String(text || "");
  }

  function setImagineButtons(running) {
    const startBtn = document.getElementById("imagineStartBtn");
    const stopBtn = document.getElementById("imagineStopBtn");
    if (startBtn) startBtn.disabled = !!running;
    if (stopBtn) stopBtn.disabled = !running;
  }

  function updateImagineActiveCount() {
    const el = document.getElementById("imagineActive");
    if (!el) return;
    if (imagineState.effectiveMode === "sse") {
      const count = imagineState.sseStreams.filter((s) => s && s.readyState !== 2).length;
      el.textContent = String(count);
      return;
    }
    const count = imagineState.wsSockets.filter((w) => w && w.readyState === 1).length;
    el.textContent = String(count);
  }

  function resetImagineMetrics() {
    imagineState.imageCount = 0;
    imagineState.latencySum = 0;
    imagineState.latencyCount = 0;
    const count = document.getElementById("imagineCount");
    const latency = document.getElementById("imagineLatency");
    if (count) count.textContent = "0";
    if (latency) latency.textContent = "-";
  }

  function appendImagineImage(b64, seq, elapsedMS, fileURL) {
    const grid = document.getElementById("imagineGrid");
    const empty = document.getElementById("imagineEmpty");
    if (!grid) return;
    if (empty) empty.style.display = "none";

    const card = document.createElement("div");
    card.className = "imagine-card";

    const img = document.createElement("img");
    let src = "";
    if (fileURL) {
      src = fileURL;
    } else if (b64) {
      const mime = detectImageMime(b64);
      src = `data:${mime};base64,${b64}`;
    }
    if (!src) return;
    img.src = src;
    img.alt = `imagine-${seq || 0}`;
    img.loading = "lazy";
    const openURL = fileURL || src;
    img.addEventListener("click", () => window.open(openURL, "_blank", "noopener"));

    const meta = document.createElement("div");
    meta.className = "imagine-meta";
    const left = document.createElement("span");
    left.textContent = `#${seq || 0}`;
    const right = document.createElement("span");
    right.textContent = formatTimeMS(elapsedMS);
    meta.appendChild(left);
    meta.appendChild(right);

    card.appendChild(img);
    card.appendChild(meta);
    grid.prepend(card);
  }

  function handleImagineMessage(payload) {
    if (!payload || typeof payload !== "object") return;
    if (payload.type === "image") {
      const b64 = String(payload.b64_json || "");
      const fileURL = String(payload.file_url || payload.url || "");
      if (!b64 && !fileURL) return;
      imagineState.imageCount += 1;
      const count = document.getElementById("imagineCount");
      if (count) count.textContent = String(imagineState.imageCount);

      const elapsed = Number(payload.elapsed_ms || 0);
      if (elapsed > 0) {
        imagineState.latencySum += elapsed;
        imagineState.latencyCount += 1;
        const avg = Math.round(imagineState.latencySum / imagineState.latencyCount);
        const latency = document.getElementById("imagineLatency");
        if (latency) latency.textContent = `${avg} ms`;
      }
      appendImagineImage(b64, payload.sequence, payload.elapsed_ms, fileURL);
      return;
    }

    if (payload.type === "status") {
      if (payload.status === "running") {
        setImagineStatus(`运行中 (${imagineState.effectiveMode.toUpperCase()})`);
      } else if (payload.status === "stopped") {
        if (imagineState.running) {
          setImagineStatus("已停止");
        }
      }
      return;
    }

    if (payload.type === "error") {
      const msg = String(payload.message || "Imagine 运行出错");
      showToast(msg, "error");
      setImagineStatus("错误");
    }
  }

  function closeImagineConnections(sendStop) {
    if (imagineState.fallbackTimer) {
      clearTimeout(imagineState.fallbackTimer);
      imagineState.fallbackTimer = null;
    }

    imagineState.wsSockets.forEach((ws) => {
      if (!ws) return;
      if (sendStop && ws.readyState === 1) {
        try {
          ws.send(JSON.stringify({ type: "stop" }));
        } catch (err) {
          // ignore
        }
      }
      try {
        ws.close(1000, "stop");
      } catch (err) {
        // ignore
      }
    });
    imagineState.wsSockets = [];

    imagineState.sseStreams.forEach((es) => {
      if (!es) return;
      try {
        es.close();
      } catch (err) {
        // ignore
      }
    });
    imagineState.sseStreams = [];
    updateImagineActiveCount();
  }

  async function createImagineTask(prompt, aspectRatio) {
    const res = await fetch("/api/v1/admin/imagine/start", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        prompt,
        aspect_ratio: aspectRatio,
      }),
    });
    if (handleUnauthorized(res)) {
      throw new Error("unauthorized");
    }
    if (!res.ok) {
      throw new Error(await res.text());
    }
    const data = await res.json();
    return String((data && data.task_id) || "").trim();
  }

  async function stopImagineTasks(taskIDs) {
    if (!Array.isArray(taskIDs) || taskIDs.length === 0) return;
    const res = await fetch("/api/v1/admin/imagine/stop", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ task_ids: taskIDs }),
    });
    if (handleUnauthorized(res)) return;
  }

  function startImagineSSE(taskIDs) {
    imagineState.effectiveMode = "sse";
    setImagineStatus("连接中 (SSE)");
    closeImagineConnections(false);

    taskIDs.forEach((taskID, idx) => {
      const url = `/api/v1/admin/imagine/sse?task_id=${encodeURIComponent(taskID)}&conn=${idx}&t=${Date.now()}`;
      const es = new EventSource(url);
      es.onopen = () => {
        setImagineStatus("运行中 (SSE)");
        updateImagineActiveCount();
      };
      es.onmessage = (event) => {
        try {
          handleImagineMessage(JSON.parse(event.data));
        } catch (err) {
          // ignore bad payload
        }
      };
      es.onerror = () => {
        updateImagineActiveCount();
        const alive = imagineState.sseStreams.filter((s) => s && s.readyState !== 2).length;
        if (alive === 0 && imagineState.running) {
          setImagineStatus("连接异常");
        }
      };
      imagineState.sseStreams.push(es);
    });
  }

  function startImagineWS(taskIDs, prompt, aspectRatio, allowFallback) {
    imagineState.effectiveMode = "ws";
    setImagineStatus("连接中 (WS)");
    closeImagineConnections(false);

    let opened = 0;
    let switched = false;

    if (allowFallback) {
      imagineState.fallbackTimer = setTimeout(() => {
        if (!imagineState.running || opened > 0 || switched) return;
        switched = true;
        showToast("WS 建连失败，自动切换 SSE", "info");
        startImagineSSE(taskIDs);
      }, 1500);
    }

    taskIDs.forEach((taskID) => {
      const protocol = window.location.protocol === "https:" ? "wss" : "ws";
      const url = `${protocol}://${window.location.host}/api/v1/admin/imagine/ws?task_id=${encodeURIComponent(taskID)}`;
      const ws = new WebSocket(url);

      ws.onopen = () => {
        opened += 1;
        updateImagineActiveCount();
        setImagineStatus("运行中 (WS)");
        try {
          ws.send(JSON.stringify({
            type: "start",
            prompt,
            aspect_ratio: aspectRatio,
          }));
        } catch (err) {
          // ignore
        }
      };

      ws.onmessage = (event) => {
        try {
          handleImagineMessage(JSON.parse(event.data));
        } catch (err) {
          // ignore bad payload
        }
      };

      ws.onerror = () => {
        if (allowFallback && opened === 0 && !switched) {
          switched = true;
          startImagineSSE(taskIDs);
          return;
        }
        updateImagineActiveCount();
      };

      ws.onclose = () => {
        updateImagineActiveCount();
      };

      imagineState.wsSockets.push(ws);
    });
  }

  async function startImagine() {
    if (imagineState.running) {
      showToast("Imagine 已在运行中", "info");
      return;
    }
    const prompt = String(document.getElementById("imaginePrompt")?.value || "").trim();
    if (!prompt) {
      showToast("请输入 Prompt", "error");
      return;
    }
    const ratio = String(document.getElementById("imagineRatio")?.value || "2:3");
    const concurrent = Math.max(1, Math.min(3, Number(document.getElementById("imagineConcurrent")?.value || 1)));
    const mode = String(document.getElementById("imagineMode")?.value || "auto").toLowerCase();

    imagineState.running = true;
    imagineState.mode = mode;
    setImagineButtons(true);
    setImagineStatus("创建任务中");

    const taskIDs = [];
    try {
      for (let i = 0; i < concurrent; i++) {
        const taskID = await createImagineTask(prompt, ratio);
        if (!taskID) {
          throw new Error("创建任务失败：空 task_id");
        }
        taskIDs.push(taskID);
      }

      imagineState.taskIDs = taskIDs;
      if (mode === "sse") {
        startImagineSSE(taskIDs);
      } else if (mode === "ws") {
        startImagineWS(taskIDs, prompt, ratio, false);
      } else {
        startImagineWS(taskIDs, prompt, ratio, true);
      }
      showToast(`Imagine 已启动 (${taskIDs.length} 并发)`, "success");
    } catch (err) {
      imagineState.running = false;
      setImagineButtons(false);
      setImagineStatus("启动失败");
      await stopImagineTasks(taskIDs);
      imagineState.taskIDs = [];
      showToast(`启动失败: ${err.message || err}`, "error");
    }
  }

  async function stopImagine() {
    const taskIDs = imagineState.taskIDs.slice();
    imagineState.running = false;
    setImagineButtons(false);
    setImagineStatus("停止中");
    closeImagineConnections(true);
    imagineState.taskIDs = [];
    try {
      await stopImagineTasks(taskIDs);
    } catch (err) {
      // ignore
    }
    updateImagineActiveCount();
    setImagineStatus("已停止");
  }

  function clearImagineGrid() {
    const grid = document.getElementById("imagineGrid");
    const empty = document.getElementById("imagineEmpty");
    if (grid) grid.innerHTML = "";
    if (empty) empty.style.display = "block";
    resetImagineMetrics();
  }

  async function fetchVoiceToken() {
    const voice = String(document.getElementById("voiceName")?.value || "ara").trim() || "ara";
    const personality = String(document.getElementById("voicePersonality")?.value || "assistant").trim() || "assistant";
    const speed = Number(document.getElementById("voiceSpeed")?.value || 1);
    const url = `/api/v1/admin/voice/token?voice=${encodeURIComponent(voice)}&personality=${encodeURIComponent(personality)}&speed=${encodeURIComponent(speed > 0 ? speed : 1)}`;

    const res = await fetch(url);
    if (handleUnauthorized(res)) return;
    if (!res.ok) {
      throw new Error(await res.text());
    }
    const data = await res.json();
    const tokenOutput = document.getElementById("voiceTokenOutput");
    const urlOutput = document.getElementById("voiceUrlOutput");
    if (tokenOutput) tokenOutput.value = String(data.token || "");
    if (urlOutput) urlOutput.value = String(data.url || "");
    showToast("Voice Token 获取成功", "success");
  }

  async function loadCacheSummary() {
    const res = await fetch("/api/v1/admin/cache");
    if (handleUnauthorized(res)) return;
    if (!res.ok) {
      throw new Error(await res.text());
    }
    const data = await res.json();
    const imageText = `${data?.image?.count || 0} / ${formatBytes(data?.image?.bytes || 0)}`;
    const videoText = `${data?.video?.count || 0} / ${formatBytes(data?.video?.bytes || 0)}`;
    const totalText = `${data?.total?.count || 0} / ${formatBytes(data?.total?.bytes || 0)}`;

    const imageEl = document.getElementById("cacheImageSummary");
    const videoEl = document.getElementById("cacheVideoSummary");
    const totalEl = document.getElementById("cacheTotalSummary");
    const baseEl = document.getElementById("cacheBaseDir");
    if (imageEl) imageEl.textContent = imageText;
    if (videoEl) videoEl.textContent = videoText;
    if (totalEl) totalEl.textContent = totalText;
    if (baseEl) baseEl.textContent = String(data.base_dir || "-");
  }

  function renderCacheList(items) {
    const body = document.getElementById("cacheListBody");
    if (!body) return;
    const list = Array.isArray(items) ? items : [];
    if (list.length === 0) {
      body.innerHTML = `<tr><td colspan="5" style="text-align:center;color:var(--text-secondary);padding:24px;">暂无缓存数据</td></tr>`;
      return;
    }

    body.innerHTML = list.map((item) => {
      const mediaType = String(item.media_type || "");
      const name = String(item.name || "");
      const url = String(item.url || "");
      const size = formatBytes(item.size || 0);
      const updatedAt = formatDateTime(item.updated_at || 0);
      return `
        <tr>
          <td><span class="tag">${mediaType}</span></td>
          <td><a href="${url}" target="_blank" rel="noopener"><code>${name}</code></a></td>
          <td>${size}</td>
          <td>${updatedAt}</td>
          <td>
            <button class="btn btn-danger-outline cache-delete-btn" data-media-type="${encodeURIComponent(mediaType)}" data-name="${encodeURIComponent(name)}" style="padding:4px 8px;">删除</button>
          </td>
        </tr>
      `;
    }).join("");
  }

  async function loadCacheList() {
    const filter = String(document.getElementById("cacheTypeFilter")?.value || "").trim();
    const url = filter ? `/api/v1/admin/cache/list?media_type=${encodeURIComponent(filter)}` : "/api/v1/admin/cache/list";
    const res = await fetch(url);
    if (handleUnauthorized(res)) return;
    if (!res.ok) {
      throw new Error(await res.text());
    }
    const data = await res.json();
    renderCacheList(data.items || []);
  }

  async function refreshCacheView() {
    await loadCacheSummary();
    await loadCacheList();
  }

  async function clearCache() {
    const filter = String(document.getElementById("cacheTypeFilter")?.value || "").trim();
    const confirmText = filter ? `确认清空 ${filter} 缓存？` : "确认清空全部缓存？";
    if (!window.confirm(confirmText)) return;

    const body = filter ? { media_type: filter } : {};
    const res = await fetch("/api/v1/admin/cache/clear", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    });
    if (handleUnauthorized(res)) return;
    if (!res.ok) {
      throw new Error(await res.text());
    }
    showToast("缓存已清空", "success");
    await refreshCacheView();
  }

  async function deleteCacheItem(mediaType, name) {
    const res = await fetch("/api/v1/admin/cache/item/delete", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        media_type: mediaType,
        name,
      }),
    });
    if (handleUnauthorized(res)) return;
    if (!res.ok) {
      throw new Error(await res.text());
    }
    await refreshCacheView();
  }

  function switchGrokToolTab(tab) {
    const sections = {
      imagine: document.getElementById("grokImagineSection"),
      voice: document.getElementById("grokVoiceSection"),
      cache: document.getElementById("grokCacheSection"),
    };
    Object.keys(sections).forEach((key) => {
      const section = sections[key];
      if (!section) return;
      section.style.display = key === tab ? "grid" : "none";
    });

    const tabs = document.querySelectorAll("#grokToolsTabs .tab-item");
    tabs.forEach((btn) => {
      const active = String(btn.dataset.tab || "").toLowerCase() === String(tab || "").toLowerCase();
      btn.classList.toggle("active", active);
    });
  }

  window.switchGrokToolTab = switchGrokToolTab;

  function bindEvents() {
    const startBtn = document.getElementById("imagineStartBtn");
    const stopBtn = document.getElementById("imagineStopBtn");
    const clearBtn = document.getElementById("imagineClearBtn");
    if (startBtn) startBtn.addEventListener("click", () => startImagine());
    if (stopBtn) stopBtn.addEventListener("click", () => stopImagine());
    if (clearBtn) clearBtn.addEventListener("click", () => clearImagineGrid());

    const voiceFetchBtn = document.getElementById("voiceFetchBtn");
    if (voiceFetchBtn) {
      voiceFetchBtn.addEventListener("click", async () => {
        try {
          await fetchVoiceToken();
        } catch (err) {
          showToast(`获取失败: ${err.message || err}`, "error");
        }
      });
    }
    const voiceCopyBtn = document.getElementById("voiceCopyBtn");
    if (voiceCopyBtn) {
      voiceCopyBtn.addEventListener("click", () => {
        const token = String(document.getElementById("voiceTokenOutput")?.value || "");
        if (!token) {
          showToast("暂无可复制 Token", "info");
          return;
        }
        copyToClipboard(token);
      });
    }

    const cacheRefreshBtn = document.getElementById("cacheRefreshBtn");
    if (cacheRefreshBtn) {
      cacheRefreshBtn.addEventListener("click", async () => {
        try {
          await refreshCacheView();
          showToast("缓存已刷新", "success");
        } catch (err) {
          showToast(`刷新失败: ${err.message || err}`, "error");
        }
      });
    }
    const cacheClearBtn = document.getElementById("cacheClearBtn");
    if (cacheClearBtn) {
      cacheClearBtn.addEventListener("click", async () => {
        try {
          await clearCache();
        } catch (err) {
          showToast(`清空失败: ${err.message || err}`, "error");
        }
      });
    }
    const cacheFilter = document.getElementById("cacheTypeFilter");
    if (cacheFilter) {
      cacheFilter.addEventListener("change", async () => {
        try {
          await loadCacheList();
        } catch (err) {
          showToast(`加载失败: ${err.message || err}`, "error");
        }
      });
    }

    const cacheListBody = document.getElementById("cacheListBody");
    if (cacheListBody) {
      cacheListBody.addEventListener("click", async (event) => {
        const btn = event.target.closest(".cache-delete-btn");
        if (!btn || !cacheListBody.contains(btn)) return;
        const mediaType = decodeURIComponent(btn.dataset.mediaType || "");
        const name = decodeURIComponent(btn.dataset.name || "");
        if (!mediaType || !name) return;
        if (!window.confirm(`确认删除 ${mediaType}/${name} ?`)) return;
        try {
          await deleteCacheItem(mediaType, name);
          showToast("删除成功", "success");
        } catch (err) {
          showToast(`删除失败: ${err.message || err}`, "error");
        }
      });
    }
  }

  async function init() {
    bindEvents();
    window.addEventListener("beforeunload", () => {
      if (!Array.isArray(imagineState.taskIDs) || imagineState.taskIDs.length === 0) return;
      closeImagineConnections(true);
      try {
        const payload = JSON.stringify({ task_ids: imagineState.taskIDs });
        navigator.sendBeacon(
          "/api/v1/admin/imagine/stop",
          new Blob([payload], { type: "application/json" }),
        );
      } catch (err) {
        // ignore unload errors
      }
    });
    switchGrokToolTab("imagine");
    resetImagineMetrics();
    updateImagineActiveCount();
    try {
      await refreshCacheView();
    } catch (err) {
      showToast(`缓存加载失败: ${err.message || err}`, "error");
    }
  }

  document.addEventListener("DOMContentLoaded", init);
})();
