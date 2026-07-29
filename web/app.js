// slick — quick-compose client logic.
(() => {
  const $ = (id) => document.getElementById(id);
  const searchField = $("searchField");
  const search = $("search");
  const results = $("results");
  const selected = $("selected");
  const chipName = $("chipName");
  const thread = $("thread");
  const compose = $("compose");
  const message = $("message");
  const send = $("send");
  const toast = $("toast");

  let all = [];        // every conversation
  let shown = [];      // currently filtered
  let active = 0;      // highlighted index in `shown`
  let target = null;   // chosen conversation

  // --- data ---------------------------------------------------------------
  fetch("/api/conversations")
    .then((r) => (r.ok ? r.json() : Promise.reject(r)))
    .then((data) => {
      all = data || [];
      search.placeholder = `Search ${all.length} channels & people…`;
    })
    .catch(() => flash("Couldn't load conversations — is the token still valid?", true));

  search.focus();

  // --- fuzzy matching -----------------------------------------------------
  function score(name, q) {
    const n = name.toLowerCase();
    const bare = n.replace(/^[#@]/, "");
    if (bare.startsWith(q)) return 1000 - bare.length;
    const idx = n.indexOf(q);
    if (idx >= 0) return 500 - idx;
    // subsequence
    let qi = 0;
    for (let i = 0; i < n.length && qi < q.length; i++) if (n[i] === q[qi]) qi++;
    return qi === q.length ? 100 - (n.length - q.length) : -1;
  }

  function filter(q) {
    q = q.trim().toLowerCase();
    if (!q) return [];
    return all
      .map((c) => ({ c, s: score(c.name, q) }))
      .filter((x) => x.s >= 0)
      .sort((a, b) => b.s - a.s)
      .slice(0, 8)
      .map((x) => x.c);
  }

  function renderResults() {
    results.innerHTML = "";
    shown.forEach((c, i) => {
      const li = document.createElement("li");
      li.setAttribute("role", "option");
      li.setAttribute("aria-selected", i === active ? "true" : "false");
      const label = document.createElement("span");
      label.textContent = c.name;
      const kind = document.createElement("span");
      kind.className = "kind";
      kind.textContent = c.kind === "channel" || c.kind === "private" ? "" : c.kind;
      li.append(label, kind);
      li.addEventListener("mousemove", () => { active = i; paintActive(); });
      li.addEventListener("click", () => choose(c));
      results.appendChild(li);
    });
  }

  function paintActive() {
    [...results.children].forEach((li, i) =>
      li.setAttribute("aria-selected", i === active ? "true" : "false"));
    results.children[active]?.scrollIntoView({ block: "nearest" });
  }

  // --- thread (recent history) -------------------------------------------
  let pollTimer = null;

  function fmtTime(ts) {
    const d = new Date(parseFloat(ts) * 1000);
    if (isNaN(d)) return "";
    return d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
  }

  function renderThread(msgs) {
    thread.innerHTML = "";
    if (!msgs || msgs.length === 0) {
      const li = document.createElement("li");
      li.className = "empty";
      li.textContent = "No recent messages.";
      thread.appendChild(li);
      return;
    }
    for (const m of msgs) {
      const li = document.createElement("li");
      if (m.mine) li.className = "mine";
      const who = document.createElement("div");
      who.className = "who";
      const name = document.createElement("span");
      name.textContent = m.user || "unknown";
      const at = document.createElement("span");
      at.className = "at";
      at.textContent = fmtTime(m.ts);
      who.append(name, at);
      const body = document.createElement("div");
      body.className = "body";
      body.textContent = m.text;
      li.append(who, body);
      thread.appendChild(li);
    }
    thread.scrollTop = thread.scrollHeight;
  }

  function loadHistory(id) {
    fetch("/api/history?channel=" + encodeURIComponent(id))
      .then((r) => (r.ok ? r.json() : Promise.reject(r)))
      .then((msgs) => { if (target && target.id === id) renderThread(msgs); })
      .catch(() => {});
  }

  function startPolling(id) {
    stopPolling();
    pollTimer = setInterval(() => loadHistory(id), 4000);
  }
  function stopPolling() {
    if (pollTimer) { clearInterval(pollTimer); pollTimer = null; }
  }

  // --- selection flow -----------------------------------------------------
  function choose(c) {
    target = c;
    chipName.textContent = c.name;
    searchField.style.display = "none";
    results.innerHTML = "";
    selected.style.display = "flex";
    compose.style.display = "block";
    thread.classList.add("show");
    thread.innerHTML = "";
    loadHistory(c.id);
    startPolling(c.id);
    message.focus();
  }

  function reset() {
    stopPolling();
    target = null;
    selected.style.display = "none";
    compose.style.display = "none";
    thread.classList.remove("show");
    thread.innerHTML = "";
    searchField.style.display = "block";
    search.value = "";
    shown = [];
    renderResults();
    search.focus();
  }

  $("clear").addEventListener("click", reset);

  // --- search interactions ------------------------------------------------
  search.addEventListener("input", () => {
    shown = filter(search.value);
    active = 0;
    renderResults();
  });

  search.addEventListener("keydown", (e) => {
    if (e.key === "ArrowDown") { e.preventDefault(); active = Math.min(active + 1, shown.length - 1); paintActive(); }
    else if (e.key === "ArrowUp") { e.preventDefault(); active = Math.max(active - 1, 0); paintActive(); }
    else if (e.key === "Enter") { e.preventDefault(); if (shown[active]) choose(shown[active]); }
    else if (e.key === "Escape") { search.value = ""; shown = []; renderResults(); }
  });

  // --- compose interactions ----------------------------------------------
  message.addEventListener("input", () => { send.disabled = message.value.trim() === ""; });

  message.addEventListener("keydown", (e) => {
    if ((e.metaKey || e.ctrlKey) && e.key === "Enter") { e.preventDefault(); doSend(); }
    else if (e.key === "Escape") { e.preventDefault(); reset(); }
  });

  send.addEventListener("click", doSend);

  function doSend() {
    const text = message.value.trim();
    if (!target || !text) return;
    send.disabled = true;
    flash("Sending…");
    fetch("/api/send", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ channel: target.id, text }),
    })
      .then((r) => (r.ok ? r.json() : r.text().then((t) => Promise.reject(t))))
      .then(() => {
        flash(`Sent to ${target.name}`);
        const id = target.id;
        message.value = "";
        send.disabled = true;
        message.focus();
        loadHistory(id); // reflect the message we just sent
      })
      .catch((err) => { flash(String(err).trim() || "Send failed", true); send.disabled = false; });
  }

  let toastTimer;
  function flash(msg, isErr) {
    toast.textContent = msg;
    toast.classList.toggle("err", !!isErr);
    clearTimeout(toastTimer);
    if (!isErr) toastTimer = setTimeout(() => { toast.textContent = ""; }, 2600);
  }
})();
