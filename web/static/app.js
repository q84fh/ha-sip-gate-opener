(() => {
  "use strict";

  const btn = document.getElementById("open");
  const status = document.getElementById("status");
  const label = btn.querySelector(".label");
  const sub = btn.querySelector(".sub");
  const leafL = btn.querySelector(".leaf-l");
  const leafR = btn.querySelector(".leaf-r");
  const $ = (id) => document.getElementById(id);
  const L = JSON.parse($("texts").textContent);
  // The token is in the URL fragment, which the browser never sends to the server.
  const token = location.hash.slice(1);

  const T = { opening: 25, open: 10, closing: 25 };
  const total = () => T.opening + T.open + T.closing;

  let cycleStart = null; // performance.now() timestamp of the (virtual) cycle start
  let override = null;   // "busy" | "err" – takes precedence over the cycle display
  let lastPhase = null;
  let raf = 0;

  const vibrate = (p) => { try { navigator.vibrate && navigator.vibrate(p); } catch (_) {} };
  const secs = (tpl, s) => tpl.replace("{s}", Math.max(1, Math.ceil(s)) + " s");

  function setStatus(msg, cls) {
    status.textContent = msg || "\u00a0";
    status.className = "status " + (cls || "");
  }

  // Swing gate seen from the front: each leaf narrows towards its post as it opens.
  const POST_L = 2.5, POST_R = 21.5, LEAF_W = 9.2;
  let drawn = -1;
  function leafPath(post, dir, w) {
    const e = (post + dir * w).toFixed(2);
    return `M${post} 8H${e}V19H${post}M${post} 19L${e} 8`;
  }
  function drawGate(openness) {
    if (Math.abs(openness - drawn) < 0.002) return;
    drawn = openness;
    const w = LEAF_W * (1 - 0.85 * openness);
    leafL.setAttribute("d", leafPath(POST_L, 1, w));
    leafR.setAttribute("d", leafPath(POST_R, -1, w));
  }

  function show(state, text, subText, openness, progress) {
    btn.dataset.state = state;
    if (label.textContent !== text) label.textContent = text;
    if (sub.textContent !== (subText || "")) sub.textContent = subText || "";
    drawGate(openness);
    btn.style.setProperty("--p", progress.toFixed(3));
  }

  function phase() {
    if (cycleStart === null) return { name: "idle", openness: 0, progress: 0, left: 0 };
    const e = (performance.now() - cycleStart) / 1000;
    if (e < T.opening) return { name: "opening", openness: e / T.opening, progress: e / T.opening, left: T.opening - e };
    if (e < T.opening + T.open) {
      const x = e - T.opening;
      return { name: "open", openness: 1, progress: x / T.open, left: T.open - x };
    }
    if (e < total()) {
      const x = (e - T.opening - T.open) / T.closing;
      return { name: "closing", openness: 1 - x, progress: x, left: T.closing * (1 - x) };
    }
    cycleStart = null;
    return { name: "idle", openness: 0, progress: 0, left: 0 };
  }

  function render() {
    raf = 0;
    const ph = phase();
    if (!override) {
      switch (ph.name) {
        case "opening":
          show("opening", L.opening, secs(L.openingSub, ph.left), ph.openness, ph.progress);
          setStatus(L.openingStatus);
          break;
        case "open":
          show("open", L.isOpen, secs(L.isOpenSub, ph.left), 1, ph.progress);
          setStatus(L.isOpenStatus, "ok");
          break;
        case "closing":
          show("closing", L.closing, L.closingSub, ph.openness, ph.progress);
          setStatus(L.closingStatus, "warn");
          break;
        default:
          show("idle", L.open, "", 0, 0);
          if (lastPhase && lastPhase !== "idle") setStatus(L.closed);
      }
      if (ph.name === "open" && lastPhase === "opening") vibrate([60, 40, 160]);
      lastPhase = ph.name;
    }
    if (cycleStart !== null || override) raf = requestAnimationFrame(render);
  }

  function kick() {
    if (!raf) raf = requestAnimationFrame(render);
  }

  function setCycle(gate) {
    if (!gate) return;
    if (gate.opening) Object.assign(T, { opening: gate.opening, open: gate.open, closing: gate.closing });
    cycleStart = gate.elapsed >= 0 ? performance.now() - gate.elapsed * 1000 : null;
    kick();
  }

  function fail(msg, sticky) {
    vibrate([80, 60, 80]);
    override = "err";
    const ph = phase();
    show("err", sticky ? L.unavailable : L.retry, "", ph.openness, 0);
    setStatus(msg, "err");
    if (sticky) {
      btn.disabled = true;
      return;
    }
    setTimeout(() => {
      if (override !== "err") return;
      override = null;
      lastPhase = null;
      kick();
    }, 4000);
  }

  async function api(path) {
    const ctrl = new AbortController();
    const t = setTimeout(() => ctrl.abort(), 20000);
    try {
      const res = await fetch(path, {
        method: "POST", cache: "no-store", credentials: "omit", signal: ctrl.signal,
        headers: { "Content-Type": "application/json" }, body: JSON.stringify({ token }),
      });
      return { res, data: await res.json().catch(() => ({})) };
    } finally {
      clearTimeout(t);
    }
  }

  function showView(d) {
    const ready = d.state === "ready";
    $("card").classList.toggle("msg", !ready);
    $("ready").hidden = !ready;
    $("msg").hidden = ready;
    if (ready) {
      $("title").textContent = d.title;
      $("hint").textContent = d.text;
      $("footer").textContent = d.footer || "";
      setCycle(d.gate);
      render();
    } else {
      // SVG elements have no .hidden property, so set the attribute.
      $("icon-expired").toggleAttribute("hidden", d.state !== "expired");
      $("icon-invalid").toggleAttribute("hidden", d.state === "expired");
      $("msg-title").textContent = d.title;
      $("msg-text").textContent = d.text;
    }
  }

  btn.addEventListener("click", async () => {
    const s = btn.dataset.state;
    if (s !== "idle" && s !== "closing" && s !== "err") return;
    override = "busy";
    const ph = phase();
    show("busy", L.busy, "", ph.openness, 0);
    setStatus(L.busyStatus);
    vibrate(30);
    kick();

    let res, data;
    try {
      ({ res, data } = await api("/api/open"));
    } catch (_) {
      return fail(L.offline);
    }

    if ((res.ok && data.ok) || res.status === 429) {
      override = null;
      lastPhase = null;
      vibrate(res.ok ? [40, 30, 40] : 40);
      setCycle(data.gate);
      return;
    }
    if (res.status === 403) return fail(data.message || L.inactive, true);
    fail(data.message || L.genericFailure);
  });

  addEventListener("hashchange", () => location.reload());

  api("/api/status")
    .then(({ data }) => showView(data.state ? data : { state: "invalid", title: L.unavailable, text: L.genericFailure }))
    .catch(() => showView({ state: "invalid", title: L.unavailable, text: L.offline }));
})();
