/* kvdb web console */
(() => {
  const $ = (sel) => document.querySelector(sel);
  const editor = $("#editor");
  const gutter = $("#gutter");
  const results = $("#results");
  const historyEl = $("#history");
  const runBtn = $("#run-btn");

  const SNIPPETS = [
    { label: "Create a table", sql:
`CREATE TABLE users (
  id INT64,
  name STRING,
  email STRING,
  age INT64,
  PRIMARY KEY (id),
  INDEX (email),
  INDEX (age, name)
);` },
    { label: "Insert sample rows", sql:
`INSERT INTO users VALUES
  (1, 'ada',   'ada@lovelace.io',   36),
  (2, 'linus', 'linus@kernel.org',  54),
  (3, 'grace', 'grace@navy.mil',    36),
  (4, 'ken',   'ken@bell-labs.com', 80),
  (5, 'alan',  'alan@bletchley.uk', 41);` },
    { label: "Point lookup", sql: "SELECT * FROM users WHERE id = 3;" },
    { label: "Index lookup", sql: "SELECT id, name FROM users WHERE email = 'linus@kernel.org';" },
    { label: "Range scan", sql: "SELECT name, age FROM users WHERE age BETWEEN 30 AND 60 ORDER BY age DESC;" },
    { label: "Update + delete", sql: "UPDATE users SET age = 37 WHERE id = 1;\nDELETE FROM users WHERE age > 70;\nSELECT * FROM users;" },
    { label: "Schema", sql: "SHOW TABLES;\nDESCRIBE users;" },
  ];

  const tabs = [{ name: "query 1", sql: "" }];
  let active = 0;
  let history = JSON.parse(localStorage.getItem("kvdb.history") || "[]");

  // ---------- helpers ----------
  const esc = (s) => String(s).replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
  const fmtBytes = (n) => n < 1024 ? n + " B" : n < 1048576 ? (n / 1024).toFixed(1) + " KB" : (n / 1048576).toFixed(2) + " MB";

  function updateGutter() {
    const lines = editor.value.split("\n").length;
    gutter.textContent = Array.from({ length: lines }, (_, i) => i + 1).join("\n");
    gutter.scrollTop = editor.scrollTop;
  }

  function renderTabs() {
    const el = $("#tabs");
    el.innerHTML = "";
    tabs.forEach((t, i) => {
      const b = document.createElement("button");
      b.className = "tab" + (i === active ? " active" : "");
      b.textContent = t.name;
      b.onclick = () => { tabs[active].sql = editor.value; active = i; editor.value = t.sql; updateGutter(); renderTabs(); };
      el.appendChild(b);
    });
    const add = document.createElement("button");
    add.className = "tab"; add.textContent = "+"; add.title = "New tab";
    add.onclick = () => { tabs[active].sql = editor.value; tabs.push({ name: "query " + (tabs.length + 1), sql: "" }); active = tabs.length - 1; editor.value = ""; updateGutter(); renderTabs(); };
    el.appendChild(add);
  }

  function renderSnippets() {
    const el = $("#snippets");
    SNIPPETS.forEach((s) => {
      const b = document.createElement("button");
      b.className = "snippet"; b.textContent = s.label;
      b.onclick = () => { editor.value = s.sql; updateGutter(); editor.focus(); };
      el.appendChild(b);
    });
  }

  function renderHistory() {
    historyEl.innerHTML = "";
    history.slice(0, 30).forEach((h) => {
      const b = document.createElement("div");
      b.className = "history-item"; b.textContent = h.replace(/\s+/g, " "); b.title = h;
      b.onclick = () => { editor.value = h; updateGutter(); editor.focus(); };
      historyEl.appendChild(b);
    });
  }

  async function loadTables() {
    const el = $("#table-list");
    try {
      const tables = await (await fetch("/api/tables")).json();
      if (!tables.length) { el.innerHTML = '<div class="muted">No tables yet — try the "Create a table" snippet.</div>'; return; }
      el.innerHTML = "";
      tables.forEach((t) => {
        const d = document.createElement("div");
        d.className = "table-item";
        const idxCols = new Set(t.indexes.map((i) => i[0]));
        d.innerHTML = `<div class="t-name">${esc(t.name)}</div>
          <div class="t-meta">${t.columns.length} columns · ${t.indexes.length} index${t.indexes.length === 1 ? "" : "es"}</div>
          <div class="table-cols">${t.columns.map((c, i) =>
            `<span class="col-chip ${i < t.pkeys ? "pk" : idxCols.has(c) ? "idx" : ""}" title="${esc(t.types[i])}">${esc(c)}</span>`).join("")}</div>`;
        d.onclick = (e) => {
          if (e.detail === 2) { editor.value = `SELECT * FROM ${t.name} LIMIT 50;`; updateGutter(); run(); }
          else d.classList.toggle("open");
        };
        d.title = "double-click to preview rows";
        el.appendChild(d);
      });
    } catch (e) { el.innerHTML = `<div class="muted">failed to load tables</div>`; }
  }

  async function loadStats() {
    try {
      const s = await (await fetch("/api/stats")).json();
      $("#db-path").textContent = s.path;
      $("#s-pages").textContent = s.pages;
      $("#s-free").textContent = s.free_pages;
      $("#s-version").textContent = s.version;
      $("#s-size").textContent = fmtBytes(s.size_bytes);
    } catch (e) { /* ignore */ }
  }

  function renderResult(r) {
    const card = document.createElement("div");
    card.className = "result-card";
    const head = document.createElement("div");
    head.className = "result-head";
    head.innerHTML = `<span class="badge ok">${esc(r.statement)}</span>` +
      (r.plan ? `<span class="badge plan">${esc(r.plan)}</span>` : "") +
      `<span class="muted">${r.affected} row${r.affected === 1 ? "" : "s"}</span>` +
      `<span class="spacer"></span><span class="time">${r.millis.toFixed(2)} ms</span>`;
    card.appendChild(head);
    if (r.columns) {
      const wrap = document.createElement("div");
      wrap.className = "grid-wrap";
      const rows = r.rows || [];
      wrap.innerHTML = `<table class="grid"><thead><tr>${r.columns.map((c) => `<th>${esc(c)}</th>`).join("")}</tr></thead>
        <tbody>${rows.map((row) => `<tr>${row.map((v) => `<td class="${typeof v === "number" ? "num" : ""}">${esc(v)}</td>`).join("")}</tr>`).join("")}</tbody></table>`;
      if (!rows.length) wrap.innerHTML += `<div class="result-msg muted">empty result set</div>`;
      card.appendChild(wrap);
    } else {
      const m = document.createElement("div");
      m.className = "result-msg"; m.textContent = r.message;
      card.appendChild(m);
    }
    return card;
  }

  function renderError(msg) {
    const card = document.createElement("div");
    card.className = "result-card";
    card.innerHTML = `<div class="result-head"><span class="badge err">ERROR</span><span class="muted">transaction rolled back</span></div>
      <div class="result-msg err">${esc(msg)}</div>`;
    return card;
  }

  async function run() {
    const sql = editor.value.trim();
    if (!sql) return;
    runBtn.disabled = true;
    try {
      const resp = await fetch("/api/query", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ sql }) });
      const data = await resp.json();
      results.innerHTML = "";
      (data.results || []).forEach((r) => results.appendChild(renderResult(r)));
      if (data.error) results.appendChild(renderError(data.error));
      history = [sql, ...history.filter((h) => h !== sql)].slice(0, 50);
      localStorage.setItem("kvdb.history", JSON.stringify(history));
      renderHistory();
      loadTables(); loadStats();
    } catch (e) {
      results.innerHTML = "";
      results.appendChild(renderError("request failed: " + e.message));
    } finally {
      runBtn.disabled = false;
    }
  }

  // ---------- wiring ----------
  runBtn.onclick = run;
  $("#clear-btn").onclick = () => { editor.value = ""; updateGutter(); editor.focus(); };
  $("#refresh-tables").onclick = () => { loadTables(); loadStats(); };
  editor.addEventListener("input", updateGutter);
  editor.addEventListener("scroll", () => (gutter.scrollTop = editor.scrollTop));
  editor.addEventListener("keydown", (e) => {
    if ((e.ctrlKey || e.metaKey) && e.key === "Enter") { e.preventDefault(); run(); }
    if (e.key === "Tab") {
      e.preventDefault();
      const s = editor.selectionStart, t = editor.selectionEnd;
      editor.value = editor.value.slice(0, s) + "  " + editor.value.slice(t);
      editor.selectionStart = editor.selectionEnd = s + 2;
      updateGutter();
    }
  });

  renderTabs(); renderSnippets(); renderHistory(); updateGutter();
  loadTables(); loadStats();
  setInterval(loadStats, 5000);
})();
