// slick — quick-compose client logic.
(() => {
  const $ = (id) => document.getElementById(id);
  const searchField = $("searchField");
  const search = $("search");
  const results = $("results");
  const selected = $("selected");
  const chipName = $("chipName");
  const notifs = $("notifs");
  const threadbar = $("threadbar");
  const threadBack = $("threadBack");
  const thread = $("thread");
  const compose = $("compose");
  const message = $("message");
  const send = $("send");
  const toast = $("toast");

  let all = [];        // every conversation
  let shown = [];      // currently filtered
  let active = 0;      // highlighted index in `shown`
  let target = null;   // chosen conversation
  let threadTS = null; // open thread's parent ts, or null for channel view

  // --- data ---------------------------------------------------------------
  fetch("/api/conversations")
    .then((r) => (r.ok ? r.json() : Promise.reject(r)))
    .then((data) => {
      all = data || [];
      search.placeholder = `Search ${all.length} channels & people…`;
    })
    .catch(() => flash("Couldn't load conversations — is the token still valid?", true));

  search.focus();

  // --- notifications ------------------------------------------------------
  // Ambient unread/mention summary on the main page. Polled while browsing;
  // paused during compose to keep that view distraction-free.
  let notifTimer = null;

  function renderNotifs(items) {
    notifs.innerHTML = "";
    if (!items || items.length === 0) return; // empty: CSS hides the panel
    const head = document.createElement("div");
    head.className = "notifs-head";
    const mentions = items.reduce((n, it) => n + (it.mentions > 0 ? 1 : 0), 0);
    head.textContent = mentions
      ? `${mentions} mention${mentions > 1 ? "s" : ""} · ${items.length} unread`
      : `${items.length} unread`;
    notifs.appendChild(head);

    for (const it of items) {
      const row = document.createElement("button");
      row.className = "notif";
      row.type = "button";
      const label = document.createElement("span");
      label.className = "notif-name";
      label.textContent = it.name;
      const badge = document.createElement("span");
      badge.className = it.mentions > 0 ? "badge mention" : "badge";
      badge.textContent = it.mentions > 0 ? String(it.mentions) : "";
      row.append(label, badge);
      row.addEventListener("click", () => choose(it));
      notifs.appendChild(row);
    }
  }

  function loadNotifs() {
    if (target) return; // composing — leave the panel be
    fetch("/api/notifications")
      .then((r) => (r.ok ? r.json() : Promise.reject(r)))
      .then((items) => { if (!target) renderNotifs(items); })
      .catch(() => { notifs.innerHTML = ""; }); // stay quiet on failure
  }

  function startNotifPolling() {
    stopNotifPolling();
    loadNotifs();
    notifTimer = setInterval(loadNotifs, 15000);
  }
  function stopNotifPolling() {
    if (notifTimer) { clearInterval(notifTimer); notifTimer = null; }
  }
  startNotifPolling();

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

  function renderThread(msgs, inThread) {
    // Only stick to the bottom if the reader is already there — otherwise a
    // poll while they're scrolled up reading history shouldn't yank them down.
    const atBottom = thread.scrollHeight - thread.scrollTop - thread.clientHeight < 48;
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
      li.append(who, renderBody(m.segs));
      // Outside a thread, a parent with replies gets a click-to-open affordance.
      if (!inThread && m.replies > 0) {
        const rep = document.createElement("button");
        rep.className = "replies";
        rep.type = "button";
        rep.textContent = `${m.replies} repl${m.replies > 1 ? "ies" : "y"}`;
        rep.addEventListener("click", () => openThread(m.thread || m.ts));
        li.appendChild(rep);
      }
      thread.appendChild(li);
    }
    if (atBottom) thread.scrollTop = thread.scrollHeight;
  }

  // /api/file proxies authenticated Slack file URLs so the browser can load them.
  function fileProxy(u) { return "/api/file?u=" + encodeURIComponent(u); }

  // Build a message body from server segments. Content is untrusted, so every
  // piece goes in via textContent / node creation — never innerHTML.
  function renderBody(segs) {
    const body = document.createElement("div");
    body.className = "body";
    for (const seg of segs || []) {
      if (seg.type === "pre") {
        const pre = document.createElement("pre");
        pre.className = "codeblock";
        const code = document.createElement("code");
        code.textContent = seg.text;
        pre.appendChild(code);
        body.appendChild(pre);
      } else if (seg.type === "code") {
        const code = document.createElement("code");
        code.className = "code";
        code.textContent = seg.text;
        body.appendChild(code);
      } else if (seg.type === "link") {
        const a = document.createElement("a");
        a.textContent = seg.text;
        if (/^https?:\/\//i.test(seg.url || "")) {
          a.href = seg.url;
          a.target = "_blank";
          a.rel = "noopener noreferrer";
        }
        body.appendChild(a);
      } else if (seg.type === "image") {
        const a = document.createElement("a");
        a.className = "img";
        a.href = fileProxy(seg.href || seg.url);
        a.target = "_blank";
        a.rel = "noopener noreferrer";
        const img = document.createElement("img");
        img.src = fileProxy(seg.url);
        img.alt = seg.text || "";
        img.loading = "lazy";
        a.appendChild(img);
        body.appendChild(a);
      } else {
        body.appendChild(document.createTextNode(seg.text));
      }
    }
    return body;
  }

  const markedTs = {}; // channel id -> newest ts we've marked read

  function loadHistory(id) {
    fetch("/api/history?channel=" + encodeURIComponent(id))
      .then((r) => (r.ok ? r.json() : Promise.reject(r)))
      .then((msgs) => {
        if (target && target.id === id && !threadTS) {
          renderThread(msgs, false);
          markRead(id, msgs);
        }
      })
      .catch(() => {});
  }

  function loadReplies(id, ts) {
    fetch("/api/replies?channel=" + encodeURIComponent(id) + "&thread=" + encodeURIComponent(ts))
      .then((r) => (r.ok ? r.json() : Promise.reject(r)))
      .then((msgs) => {
        if (target && target.id === id && threadTS === ts) renderThread(msgs, true);
      })
      .catch(() => {});
  }

  // refresh loads whichever view is open — the thread if one is, else history.
  function refresh() {
    if (!target) return;
    if (threadTS) loadReplies(target.id, threadTS);
    else loadHistory(target.id);
  }

  function openThread(ts) {
    threadTS = ts;
    threadbar.style.display = "flex";
    thread.innerHTML = "";
    message.placeholder = "Reply in thread…";
    loadReplies(target.id, ts);
    startPolling();
    message.focus();
  }

  function closeThread() {
    threadTS = null;
    threadbar.style.display = "none";
    thread.innerHTML = "";
    message.placeholder = "Write a message…";
    loadHistory(target.id);
    startPolling();
  }

  threadBack.addEventListener("click", closeThread);

  // Viewing a conversation clears its unread state on Slack, so it drops off
  // the notifications panel. Only send when the newest message has changed.
  function markRead(id, msgs) {
    if (!msgs || !msgs.length) return;
    const ts = msgs[msgs.length - 1].ts;
    if (!ts || markedTs[id] === ts) return;
    markedTs[id] = ts;
    fetch("/api/mark", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ channel: id, ts }),
    }).catch(() => {});
  }

  function startPolling() {
    stopPolling();
    pollTimer = setInterval(refresh, 4000);
  }
  function stopPolling() {
    if (pollTimer) { clearInterval(pollTimer); pollTimer = null; }
  }

  // --- selection flow -----------------------------------------------------
  function choose(c) {
    target = c;
    threadTS = null;
    threadbar.style.display = "none";
    message.placeholder = "Write a message…";
    stopNotifPolling();
    notifs.innerHTML = "";
    chipName.textContent = c.name;
    searchField.style.display = "none";
    results.innerHTML = "";
    selected.style.display = "flex";
    compose.style.display = "block";
    thread.classList.add("show");
    thread.innerHTML = "";
    loadHistory(c.id);
    startPolling();
    message.focus();
  }

  function reset() {
    stopPolling();
    target = null;
    threadTS = null;
    threadbar.style.display = "none";
    message.placeholder = "Write a message…";
    selected.style.display = "none";
    compose.style.display = "none";
    thread.classList.remove("show");
    thread.innerHTML = "";
    searchField.style.display = "block";
    search.value = "";
    shown = [];
    renderResults();
    startNotifPolling();
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
    if (e.key === "Enter" && e.shiftKey) { e.preventDefault(); doSend(); }
  });

  // Esc steps back no matter what has focus: out of an open thread first, then
  // out to recipient selection. The search field's own Esc (clear query) only
  // matters while browsing, when no target is set.
  document.addEventListener("keydown", (e) => {
    if (e.key !== "Escape" || !target) return;
    e.preventDefault();
    if (threadTS) closeThread();
    else reset();
  });

  send.addEventListener("click", doSend);

  function doSend() {
    const text = message.value.trim();
    if (!target || !text) return;
    send.disabled = true;
    flash("Sending…");
    const inThread = threadTS;
    fetch("/api/send", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ channel: target.id, text, thread: inThread || "" }),
    })
      .then((r) => (r.ok ? r.json() : r.text().then((t) => Promise.reject(t))))
      .then(() => {
        flash(inThread ? "Replied in thread" : `Sent to ${target.name}`);
        message.value = "";
        send.disabled = true;
        message.focus();
        refresh(); // reflect the message we just sent
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
