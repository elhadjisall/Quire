/* Quire console */
(() => {
  const $ = (sel) => document.querySelector(sel);
  const editor = $("#editor");
  const results = $("#results");
  const keysEl = $("#keys");
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
    { label: "Schema", sql: "SHOW TABLES;\nDESCRIBE users;\nSHOW INDEX FROM users;" },
    { label: "Create an index", sql: "CREATE INDEX ON users (name);\nSELECT id, email FROM users WHERE name = 'ada';" },
    { label: "Aggregates", sql: "SELECT COUNT(*), MIN(age), MAX(age), SUM(age) FROM users;" },
    { label: "Explain", sql: "EXPLAIN SELECT name FROM users WHERE email = 'ada@lovelace.io';" },
  ];

  let history = JSON.parse(localStorage.getItem("kvdb.history") || "[]");
  let activeTable = "";
  let activeIndex = "";

  const esc = (s) => String(s).replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
  const fmtBytes = (n) => n < 1024 ? n + " B" : n < 1048576 ? (n / 1024).toFixed(1) + " KB" : (n / 1048576).toFixed(2) + " MB";
  const cellHTML = (v) => v === null || v === undefined
    ? '<td class="nil">NULL</td>'
    : `<td class="${typeof v === "number" ? "num" : ""}">${esc(v)}</td>`;
  const indexKey = (table, cols) => table + "|" + cols.join(",");

  function renderSnippets() {
    const el = $("#snippets");
    el.innerHTML = "";
    SNIPPETS.forEach((s) => {
      const b = document.createElement("button");
      b.type = "button";
      b.className = "snippet";
      b.textContent = s.label;
      b.onclick = () => { editor.value = s.sql; editor.focus(); };
      el.appendChild(b);
    });
  }

  function renderHistory() {
    historyEl.innerHTML = "";
    history.slice(0, 8).forEach((h) => {
      const b = document.createElement("button");
      b.type = "button";
      b.className = "history-item";
      b.textContent = h.replace(/\s+/g, " ");
      b.title = h;
      b.onclick = () => { editor.value = h; editor.focus(); };
      historyEl.appendChild(b);
    });
  }

  function tableHTML(columns, rows) {
    const body = (rows || []).map((row) => `<tr>${row.map(cellHTML).join("")}</tr>`).join("");
    return `<table><thead><tr>${columns.map((c) => `<th>${esc(c)}</th>`).join("")}</tr></thead><tbody>${body}</tbody></table>`;
  }

  async function loadTables() {
    const el = $("#schema");
    try {
      const tables = await (await fetch("/api/tables")).json();
      if (!tables.length) { el.innerHTML = '<p class="label">No tables yet.</p>'; return; }
      el.innerHTML = "";
      tables.forEach((t) => {
        const d = document.createElement("div");
        d.className = "rel";
        const pk = t.columns.slice(0, t.pkeys);
        const pkKey = indexKey(t.name, pk);
        const links = [`<button type="button" class="idx" data-key="${esc(pkKey)}" data-cols="${esc(pk.join(","))}">primary ${esc(pk.join(", "))}</button>`]
          .concat(t.indexes.map((cols) => {
            const key = indexKey(t.name, cols);
            return `<button type="button" class="idx" data-key="${esc(key)}" data-cols="${esc(cols.join(","))}">index ${esc(cols.join(", "))}</button>`;
          }));
        d.innerHTML = `<button type="button" class="tname${t.name === activeTable ? " active" : ""}">${esc(t.name)}</button>
          <p class="cols">${t.columns.map((c, i) => `${esc(c)} ${esc(t.types[i])}`).join(", ")}</p>
          <p class="idxs">${links.join("")}</p>`;
        d.querySelector(".tname").onclick = (e) => {
          activeTable = t.name;
          document.querySelectorAll(".tname").forEach((n) => n.classList.toggle("active", n.textContent === t.name));
          if (e.detail === 2) {
            editor.value = `SELECT * FROM ${t.name} LIMIT 50;`;
            run();
          }
        };
        d.querySelectorAll(".idx").forEach((b) => {
          b.onclick = (e) => {
            e.stopPropagation();
            activeTable = t.name;
            document.querySelectorAll(".tname").forEach((n) => n.classList.toggle("active", n.textContent === t.name));
            loadIndex(t.name, b.dataset.cols.split(",").filter(Boolean));
          };
        });
        el.appendChild(d);
      });
    } catch (e) { el.innerHTML = '<p class="label">Failed to load tables.</p>'; }
  }

  async function loadStats() {
    try {
      const s = await (await fetch("/api/stats")).json();
      $("#db-path").textContent = s.path + " · " + s.pages + " pages · " + s.free_pages + " free · version " + s.version + " · " + fmtBytes(s.size_bytes);
    } catch (e) { /* ignore */ }
  }

  function renderResult(r) {
    const block = document.createElement("div");
    block.className = "block";
    const head = document.createElement("p");
    head.textContent = [r.statement, r.plan, r.affected + (r.affected === 1 ? " row" : " rows"), r.millis.toFixed(2) + " ms"].filter(Boolean).join(" · ");
    block.appendChild(head);
    if (r.columns) {
      const rows = r.rows || [];
      block.insertAdjacentHTML("beforeend", tableHTML(r.columns, rows));
      if (!rows.length) {
        const m = document.createElement("p");
        m.textContent = "Empty result.";
        block.appendChild(m);
      }
    } else if (r.message) {
      const m = document.createElement("p");
      m.textContent = r.message;
      block.appendChild(m);
    }
    return block;
  }

  function renderError(msg) {
    const block = document.createElement("div");
    block.className = "block";
    block.innerHTML = `<p>Error · rolled back</p><p class="err">${esc(msg)}</p>`;
    return block;
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
      loadTables();
      loadStats();
      loadPages();
    } catch (e) {
      results.innerHTML = "";
      results.appendChild(renderError("request failed: " + e.message));
    } finally {
      runBtn.disabled = false;
    }
  }

  async function loadIndex(table, cols) {
    activeIndex = indexKey(table, cols);
    keysEl.innerHTML = '<div class="block"><p>Loading index…</p></div>';
    const q = new URLSearchParams({ table, cols: cols.join(","), limit: "200" });
    const resp = await fetch("/api/indexes?" + q);
    if (!resp.ok) {
      keysEl.innerHTML = `<div class="block"><p class="err">${esc(await resp.text())}</p></div>`;
      return;
    }
    const data = await resp.json();
    const rows = data.rows || [];
    const block = document.createElement("div");
    block.className = "block";
    const head = document.createElement("p");
    head.textContent = table + " · " + cols.join(", ") + " · " + rows.length + (rows.length === 1 ? " key" : " keys");
    block.appendChild(head);
    if (data.columns) block.insertAdjacentHTML("beforeend", tableHTML(data.columns, rows));
    keysEl.innerHTML = "";
    keysEl.appendChild(block);
  }

  async function loadPages() {
    const el = $("#pages");
    const resp = await fetch("/api/pages");
    if (!resp.ok) {
      el.textContent = "pages unavailable";
      return;
    }
    const pm = await resp.json();
    const pages = pm.pages || [];
    const ids = pages.map((p) => p.id).join(" ");
    el.textContent = (pm.truncated ? "pages (first " + pages.length + ") " : "pages ") + ids;
  }

  runBtn.onclick = run;
  $("#clear-btn").onclick = () => { editor.value = ""; editor.focus(); };
  editor.addEventListener("keydown", (e) => {
    if ((e.ctrlKey || e.metaKey) && e.key === "Enter") { e.preventDefault(); run(); }
    if (e.key === "Tab") {
      e.preventDefault();
      const s = editor.selectionStart, t = editor.selectionEnd;
      editor.value = editor.value.slice(0, s) + "  " + editor.value.slice(t);
      editor.selectionStart = editor.selectionEnd = s + 2;
    }
  });

  renderSnippets();
  renderHistory();
  loadTables();
  loadStats();
  loadPages();
  setInterval(loadStats, 5000);
})();
