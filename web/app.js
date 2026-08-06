// slick — quick-compose client logic.
(() => {
  const $ = (id) => document.getElementById(id);
  const searchField = $("searchField");
  const search = $("search");
  const results = $("results");
  const msgResults = $("msgResults");
  const selected = $("selected");
  const chipName = $("chipName");
  const notifs = $("notifs");
  const favicon = $("favicon");
  const threadbar = $("threadbar");
  const threadBack = $("threadBack");
  const thread = $("thread");
  const compose = $("compose");
  const message = $("message");
  const send = $("send");
  const toast = $("toast");
  const lightbox = $("lightbox");
  const lightboxImg = lightbox.querySelector("img");

  let all = [];        // every conversation
  let shown = [];      // currently filtered
  let active = 0;      // highlighted index in `shown`
  let target = null;   // chosen conversation
  let threadTS = null; // open thread's parent ts, or null for channel view
  let anchorTS = null; // when set, history is pinned around this message (from search)

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
  let notifSig = ""; // last rendered contents, so we only animate on change

  // Swap the tab favicon to a badged version while anything is unread, so a
  // background tab still signals new activity.
  function setFaviconBadge(on) {
    const href = on ? "/static/favicon-badge.svg" : "/static/favicon.svg";
    if (favicon.getAttribute("href") !== href) favicon.setAttribute("href", href);
  }

  function renderNotifs(items) {
    setFaviconBadge(!!(items && items.length));
    const sig = (items || []).map((i) => i.id + ":" + i.mentions).join(",");
    const changed = sig !== notifSig;
    notifSig = sig;
    notifs.innerHTML = "";
    if (!items || items.length === 0) return; // empty: CSS hides the panel
    notifs.classList.toggle("intro", changed);
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
      .catch(() => { notifs.innerHTML = ""; setFaviconBadge(false); }); // stay quiet on failure
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

  // --- message search -----------------------------------------------------
  // The conversation picker matches names locally; this searches message *text*
  // via Slack, debounced, and shows hits below the name matches.
  let searchTimer = null;
  let searchSeq = 0; // guards against out-of-order responses

  function scheduleMsgSearch(q) {
    clearTimeout(searchTimer);
    q = q.trim();
    searchSeq++; // invalidate any in-flight response
    if (q.length < 2) { renderMsgResults(null); return; }
    searchTimer = setTimeout(() => runMsgSearch(q), 300);
  }

  function runMsgSearch(q) {
    const seq = ++searchSeq;
    fetch("/api/search?q=" + encodeURIComponent(q))
      .then((r) => (r.ok ? r.json() : Promise.reject(r)))
      .then((items) => { if (seq === searchSeq && !target) renderMsgResults(items); })
      .catch(() => { if (seq === searchSeq) renderMsgResults(null); });
  }

  function fmtDate(ts) {
    const d = new Date(parseFloat(ts) * 1000);
    if (isNaN(d)) return "";
    return d.toLocaleDateString([], { month: "short", day: "numeric" });
  }

  function renderMsgResults(items) {
    msgResults.innerHTML = "";
    if (!items || !items.length) return;
    const head = document.createElement("div");
    head.className = "msg-results-head";
    head.textContent = "Messages";
    msgResults.appendChild(head);
    for (const it of items) {
      const row = document.createElement("button");
      row.className = "msg-result";
      row.type = "button";
      const meta = document.createElement("div");
      meta.className = "msg-meta";
      const ch = document.createElement("span");
      ch.className = "msg-ch";
      ch.textContent = it.channelName || "";
      const who = document.createElement("span");
      who.className = "msg-who";
      who.textContent = it.user || "";
      const at = document.createElement("span");
      at.className = "msg-at";
      at.textContent = fmtDate(it.ts);
      meta.append(ch, who, at);
      const snippet = renderBody(it.segs); // reuse rich rendering (mentions, code, …)
      snippet.classList.add("msg-snippet");
      row.append(meta, snippet);
      row.addEventListener("click", () => openResult(it));
      msgResults.appendChild(row);
    }
  }

  // Open a search hit: jump into its conversation, anchoring history at that
  // message (or opening its thread when the hit is a reply).
  function openResult(it) {
    const conv = { id: it.channel, name: it.channelName, kind: it.kind };
    if (it.thread) {
      choose(conv);
      openThread(it.thread);
    } else {
      choose(conv, it.ts);
    }
  }

  // --- thread (recent history) -------------------------------------------
  let pollTimer = null;

  function fmtTime(ts) {
    const d = new Date(parseFloat(ts) * 1000);
    if (isNaN(d)) return "";
    return d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
  }

  let threadSig = null; // last rendered contents, to skip no-op re-renders

  function renderThread(msgs, inThread, animate) {
    // Skip identical re-renders from polling: rebuilding the DOM every tick
    // re-creates the <img> nodes, which makes images flash as they re-fade in.
    // A view change (animate) always renders fresh.
    const sig = JSON.stringify(msgs || []);
    if (!animate && sig === threadSig) return;
    threadSig = sig;
    // Only stick to the bottom if the reader is already there — otherwise a
    // poll while they're scrolled up reading history shouldn't yank them down.
    const atBottom = thread.scrollHeight - thread.scrollTop - thread.clientHeight < 48;
    thread.classList.toggle("intro", !!animate); // animate only on view change
    thread.innerHTML = "";
    if (!msgs || msgs.length === 0) {
      const li = document.createElement("li");
      li.className = "empty";
      li.textContent = "No recent messages.";
      thread.appendChild(li);
      return;
    }
    let hitEl = null; // the searched message, when anchored, to scroll into view
    for (const m of msgs) {
      const li = document.createElement("li");
      if (m.mine) li.className = "mine";
      if (anchorTS && m.ts === anchorTS) { li.classList.add("hit"); hitEl = li; }
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
    if (anchorTS) {
      // Center the searched message on the initial (animated) render; leave the
      // scroll alone on polls so the reader isn't yanked around.
      if (animate && hitEl) {
        const cr = thread.getBoundingClientRect();
        const hr = hitEl.getBoundingClientRect();
        thread.scrollTop += (hr.top - cr.top) - (thread.clientHeight - hitEl.clientHeight) / 2;
      }
    } else if (atBottom) {
      thread.scrollTop = thread.scrollHeight;
    }
  }

  // /api/file proxies authenticated Slack file URLs so the browser can load them.
  function fileProxy(u) { return "/api/file?u=" + encodeURIComponent(u); }

  // --- image lightbox -----------------------------------------------------
  // Click a thumbnail to open the full image in a fading, scaling overlay
  // instead of navigating away. Esc or a click anywhere closes it.
  let lightboxCloseTimer = null;
  function openLightbox(src) {
    clearTimeout(lightboxCloseTimer);
    lightboxImg.src = src;
    lightbox.classList.add("show");
    lightbox.setAttribute("aria-hidden", "false");
    requestAnimationFrame(() => lightbox.classList.add("open")); // next frame → transition runs
  }
  function closeLightbox() {
    if (!lightbox.classList.contains("show")) return;
    lightbox.classList.remove("open");
    lightbox.setAttribute("aria-hidden", "true");
    lightboxCloseTimer = setTimeout(() => {
      lightbox.classList.remove("show");
      lightboxImg.removeAttribute("src");
    }, 260); // matches the CSS transition
  }
  lightbox.addEventListener("click", closeLightbox);

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
      } else if (seg.type === "mention") {
        const span = document.createElement("span");
        span.className = seg.self ? "mention me" : "mention";
        span.textContent = seg.text;
        body.appendChild(span);
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
        const full = fileProxy(seg.href || seg.url);
        const a = document.createElement("a");
        a.className = "img";
        a.href = full; // real href: middle-click / ⌘-click still opens the file
        a.rel = "noopener noreferrer";
        a.addEventListener("click", (e) => {
          if (e.metaKey || e.ctrlKey || e.shiftKey || e.button !== 0) return;
          e.preventDefault();
          openLightbox(full);
        });
        const img = document.createElement("img");
        img.alt = seg.text || "";
        img.loading = "lazy";
        img.addEventListener("load", () => img.classList.add("loaded"));
        img.src = fileProxy(seg.url);
        a.appendChild(img);
        body.appendChild(a);
      } else if (seg.style === "b") {
        const el = document.createElement("strong");
        el.textContent = seg.text;
        body.appendChild(el);
      } else if (seg.style === "i") {
        const el = document.createElement("em");
        el.textContent = seg.text;
        body.appendChild(el);
      } else if (seg.style === "s") {
        const el = document.createElement("s");
        el.textContent = seg.text;
        body.appendChild(el);
      } else {
        body.appendChild(document.createTextNode(seg.text));
      }
    }
    return body;
  }

  const markedTs = {}; // channel id -> newest ts we've marked read

  function loadHistory(id, animate) {
    let url = "/api/history?channel=" + encodeURIComponent(id);
    if (anchorTS) url += "&latest=" + encodeURIComponent(anchorTS);
    fetch(url)
      .then((r) => (r.ok ? r.json() : Promise.reject(r)))
      .then((msgs) => {
        if (target && target.id === id && !threadTS) {
          renderThread(msgs, false, animate);
          // Don't mark read when anchored on an old search hit — that would move
          // the read cursor backward and re-flag newer messages as unread.
          if (!anchorTS) markRead(id, msgs);
        }
      })
      .catch(() => {});
  }

  function loadReplies(id, ts, animate) {
    fetch("/api/replies?channel=" + encodeURIComponent(id) + "&thread=" + encodeURIComponent(ts))
      .then((r) => (r.ok ? r.json() : Promise.reject(r)))
      .then((msgs) => {
        if (target && target.id === id && threadTS === ts) renderThread(msgs, true, animate);
      })
      .catch(() => {});
  }

  // refresh loads whichever view is open — the thread if one is, else history.
  // Poll-driven, so it never animates (that would flicker the list every tick).
  function refresh() {
    if (!target) return;
    if (threadTS) loadReplies(target.id, threadTS, false);
    else loadHistory(target.id, false);
  }

  function openThread(ts) {
    threadTS = ts;
    threadbar.style.display = "flex";
    thread.innerHTML = "";
    message.placeholder = "Reply in thread…";
    loadReplies(target.id, ts, true);
    startPolling();
    message.focus();
  }

  function closeThread() {
    threadTS = null;
    threadbar.style.display = "none";
    thread.innerHTML = "";
    message.placeholder = "Write a message…";
    loadHistory(target.id, true);
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
  function choose(c, anchor) {
    target = c;
    threadTS = null;
    anchorTS = anchor || null; // set when opening a search hit in context
    threadbar.style.display = "none";
    message.placeholder = "Write a message…";
    stopNotifPolling();
    notifs.innerHTML = "";
    notifSig = ""; // re-animate when the panel returns on reset
    clearTimeout(searchTimer);
    searchSeq++;
    msgResults.innerHTML = "";
    chipName.textContent = c.name;
    searchField.style.display = "none";
    results.innerHTML = "";
    selected.style.display = "flex";
    compose.style.display = "block";
    thread.classList.add("show");
    thread.innerHTML = "";
    loadHistory(c.id, true);
    startPolling();
    message.focus();
  }

  function reset() {
    stopPolling();
    target = null;
    threadTS = null;
    anchorTS = null;
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
    msgResults.innerHTML = "";
    startNotifPolling();
    search.focus();
  }

  $("clear").addEventListener("click", reset);

  // --- search interactions ------------------------------------------------
  search.addEventListener("input", () => {
    shown = filter(search.value);
    active = 0;
    renderResults();
    scheduleMsgSearch(search.value);
  });

  search.addEventListener("keydown", (e) => {
    if (e.key === "ArrowDown") { e.preventDefault(); active = Math.min(active + 1, shown.length - 1); paintActive(); }
    else if (e.key === "ArrowUp") { e.preventDefault(); active = Math.max(active - 1, 0); paintActive(); }
    else if (e.key === "Enter") { e.preventDefault(); if (shown[active]) choose(shown[active]); }
    else if (e.key === "Escape") { search.value = ""; shown = []; renderResults(); scheduleMsgSearch(""); }
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
    if (e.key !== "Escape") return;
    if (lightbox.classList.contains("show")) { e.preventDefault(); closeLightbox(); return; }
    if (!target) return;
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
    toast.classList.remove("pop");
    void toast.offsetWidth; // reflow so the animation restarts on repeat toasts
    if (msg) toast.classList.add("pop");
    clearTimeout(toastTimer);
    if (!isErr) toastTimer = setTimeout(() => {
      toast.textContent = "";
      toast.classList.remove("pop");
    }, 2600);
  }
})();
