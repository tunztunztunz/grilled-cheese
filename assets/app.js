"use strict";

// Question bodies, options and agent notes are HTML the agent wrote, and are
// injected as-is. Anything the user typed goes through esc().
const esc = s => String(s ?? "").replace(/[&<>"]/g, c => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[c]));
const el = h => Object.assign(document.createElement("div"), { innerHTML: h }).firstElementChild;
const post = (path, body) =>
  fetch(path, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) })
    .then(r => r.ok || r.text().then(t => Promise.reject(new Error(t))));

const dots = `<span class="dots"><i></i><i></i><i></i></span>`;

const S = { state: null, cursor: 0, drafts: {}, live: [] };

/* ------------------------------------------------------------------ poll */

// The server holds the request open until the state moves, so this loop is
// idle rather than busy between changes.
async function poll() {
  for (;;) {
    try {
      const res = await fetch(`/state?since=${S.state?.version ?? -1}`);
      S.state = await res.json();
      render();
    } catch {
      await new Promise(r => setTimeout(r, 1000));
    }
  }
}

/* ---------------------------------------------------------------- render */

const pending = q => q.entries?.length && q.entries.at(-1).from === "user";

function render() {
  // A keypress can reach moveTo() before the first poll lands.
  if (!S.state) return;
  const { purpose, rounds = [], roundComplete } = S.state;

  // Drafts and focus survive a re-render; the poll loop rebuilds the list
  // under whatever the user is in the middle of typing.
  document.querySelectorAll("textarea[data-id]").forEach(t => (S.drafts[t.dataset.id] = t.value));
  const focused = document.activeElement?.dataset?.id;
  const caret = document.activeElement?.selectionStart;

  document.getElementById("purpose").textContent = purpose || "The agent has not summarised this session yet.";

  const latest = rounds.at(-1);
  S.live = latest ? latest.questions : [];
  S.cursor = Math.min(S.cursor, Math.max(S.live.length - 1, 0));

  const settled = S.live.filter(q => q.status !== "open").length;
  document.getElementById("progress").textContent = latest ? `round ${latest.n} · ${settled}/${S.live.length}` : "";

  const main = document.getElementById("rounds");
  main.replaceChildren();

  rounds.slice(0, -1).forEach(r => {
    const d = el(`<details class="past"><summary>Round ${r.n} · ${r.questions.length} settled</summary><div></div></details>`);
    r.questions.forEach((q, i) => d.lastElementChild.append(card(q, i, false)));
    main.append(d);
  });

  if (latest) {
    main.append(el(`<p class="round-label">Round ${latest.n}</p>`));
    S.live.forEach((q, i) => main.append(card(q, i, true)));
  }
  if (!latest) main.append(el(`<div class="banner">${dots} Waiting for the first round</div>`));
  else if (roundComplete) main.append(el(`<div class="banner">${dots} Round complete — the agent is deciding what to ask next</div>`));

  renderLog(rounds);

  const t = focused && document.querySelector(`textarea[data-id="${focused}"]`);
  if (t) {
    t.focus();
    t.setSelectionRange(caret, caret);
  } else {
    // Submitting removes the focused textarea without firing blur, which would
    // otherwise strand the keyboard in insert mode.
    setMode("normal");
  }
}

function card(q, i, live) {
  const n = i + 1;
  const label = q.status === "open" ? "" : q.status;
  const node = el(`<article class="q" data-id="${q.id}" data-status="${q.status}">
    <h3><span class="n">${n}</span><span>${esc(q.title)}</span><span class="state">${label}</span></h3>
    <div class="body">${q.body || ""}</div>
  </article>`);
  if (live && i === S.cursor) node.dataset.cursor = "";

  // findLast, not find: a question replied to with --status open stays open, so
  // the user can pick again and only the latest choice is theirs.
  const chosen = q.entries?.findLast(e => e.kind === "option")?.option;
  if (q.options?.length) {
    const ul = el(`<ul class="options"></ul>`);
    q.options.forEach((opt, j) => {
      const k = j + 1;
      const li = el(`<li><button ${q.status === "open" ? "" : "disabled"} aria-pressed="${chosen === k}">
        <kbd>${k}</kbd><span>${opt}</span>${q.recommended === k ? `<span class="rec">recommended</span>` : ""}
      </button></li>`);
      li.firstElementChild.onclick = () => answer(q, { kind: "option", option: k });
      ul.append(li);
    });
    node.append(ul);
  }

  if (q.entries?.length) {
    const thread = el(`<div class="thread"></div>`);
    q.entries.forEach(e => thread.append(entry(q, e)));
    node.append(thread);
  }

  if (q.status !== "open") return node;
  if (pending(q)) {
    node.append(el(`<div class="waiting">${dots} Thinking…</div>`));
    return node;
  }

  const form = el(`<form>
    <textarea data-id="${q.id}" rows="2" placeholder="Answer, or ask a follow-up…"></textarea>
    <div class="actions">
      <button type="button" class="reject"><kbd>x</kbd> Reject</button>
      <span class="spacer"></span>
      <button type="submit" class="send">Send <kbd>ctrl ↵</kbd></button>
    </div>
  </form>`);
  const box = form.querySelector("textarea");
  box.value = S.drafts[q.id] || "";
  box.onkeydown = e => {
    if (e.key === "Escape") { box.blur(); e.preventDefault(); }
    if (e.key === "Enter" && (e.ctrlKey || e.metaKey)) { form.requestSubmit(); e.preventDefault(); }
  };
  box.onfocus = () => setMode("insert");
  box.onblur = () => setMode("normal");
  form.querySelector(".reject").onclick = () => answer(q, { kind: "reject" });
  form.onsubmit = e => {
    e.preventDefault();
    if (box.value.trim()) answer(q, { kind: "text", body: box.value.trim() });
  };
  node.append(form);
  return node;
}

function entry(q, e) {
  if (e.from === "agent") return el(`<div class="entry agent"><span class="who">agent</span>${e.body || ""}</div>`);
  const said = {
    option: () => q.options?.[e.option - 1] || `Option ${e.option}`,
    reject: () => "Rejected this proposal.",
    text: () => `<p>${esc(e.body)}</p>`,
  }[e.kind]();
  return el(`<div class="entry user ${e.kind}"><span class="who">you</span>${said}</div>`);
}

function renderLog(rounds) {
  const decided = rounds.flatMap(r => r.questions).filter(q => q.status !== "open");
  const log = document.getElementById("log");
  log.replaceChildren();
  if (!decided.length) { log.append(el(`<li class="empty">Nothing settled yet.</li>`)); return; }
  decided.forEach(q => {
    const pick = q.entries?.findLast(e => e.kind === "option");
    // Guarded as entry() guards it: the server rejects an out-of-range pick, but
    // a state.json written before it did would otherwise log "undefined".
    const outcome = q.status === "rejected" ? "rejected"
      : pick ? q.options?.[pick.option - 1] || `Option ${pick.option}`
      : q.entries?.findLast(e => e.from === "user")?.body || "settled";
    log.append(el(`<li class="${q.status}">
      <span class="tag">${q.id}</span>
      <span class="what"><strong>${esc(q.title)}</strong>${outcome}</span>
    </li>`));
  });
}

/* ------------------------------------------------------------- interaction */

function answer(q, payload) {
  if (q.status !== "open" || pending(q)) return;
  // Clearing the live box too, or the next render harvests the sent text
  // straight back into the draft.
  delete S.drafts[q.id];
  const box = document.querySelector(`textarea[data-id="${q.id}"]`);
  if (box) box.value = "";
  post("/submit", { id: q.id, ...payload }).catch(e => alert(e.message));
}

const setMode = m => (document.body.dataset.mode = document.getElementById("mode").textContent = m);

function moveTo(i) {
  S.cursor = Math.max(0, Math.min(i, S.live.length - 1));
  render();
  document.querySelector("[data-cursor]")?.scrollIntoView({ block: "center", behavior: "smooth" });
}

function togglePanel(which) {
  ["purpose", "log"].forEach(name => {
    const open = name === which && document.getElementById(`panel-${name}`).hidden;
    document.getElementById(`panel-${name}`).hidden = !open;
    document.getElementById(`btn-${name}`).setAttribute("aria-expanded", open);
  });
}
document.getElementById("btn-purpose").onclick = () => togglePanel("purpose");
document.getElementById("btn-log").onclick = () => togglePanel("log");

let gPending = false;
addEventListener("keydown", e => {
  if (document.body.dataset.mode === "insert" || e.ctrlKey || e.metaKey || e.altKey) return;

  const q = S.live[S.cursor];
  const g = gPending;
  gPending = false;

  const keys = {
    j: () => moveTo(S.cursor + 1),
    k: () => moveTo(S.cursor - 1),
    G: () => moveTo(S.live.length - 1),
    g: () => (g ? moveTo(0) : (gPending = true)),
    x: () => q && answer(q, { kind: "reject" }),
    d: () => togglePanel("log"),
    "?": () => togglePanel("purpose"),
    "/": () => {
      const next = S.live.findIndex((c, i) => i > S.cursor && c.status === "open");
      moveTo(next === -1 ? S.live.findIndex(c => c.status === "open") : next);
    },
    i: () => document.querySelector("[data-cursor] textarea")?.focus(),
    Enter: () => document.querySelector("[data-cursor] textarea")?.focus(),
    Escape: () => togglePanel(null),
  };

  if (/^[1-9]$/.test(e.key)) {
    if (q?.options?.[+e.key - 1]) answer(q, { kind: "option", option: +e.key });
  } else if (keys[e.key]) {
    keys[e.key]();
  } else {
    return;
  }
  e.preventDefault();
});

poll();
