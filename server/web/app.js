/* Quire console */
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
    { label: "Schema", sql: "SHOW TABLES;\nDESCRIBE users;\nSHOW INDEX FROM users;" },
    { label: "Create an index", sql: "CREATE INDEX ON users (name);\nSELECT id, email FROM users WHERE name = 'ada';" },
    { label: "Aggregates", sql: "SELECT COUNT(*), MIN(age), MAX(age), SUM(age) FROM users;" },
    { label: "Explain", sql: "EXPLAIN SELECT name FROM users WHERE email = 'ada@lovelace.io';" },
  ];

  const tabs = [{ name: "query 1", sql: "" }];
  let active = 0;
  let history = JSON.parse(localStorage.getItem("kvdb.history") || "[]");

  const esc = (s) => String(s).replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
  const fmtBytes = (n) => n < 1024 ? n + " B" : n < 1048576 ? (n / 1024).toFixed(1) + " KB" : (n / 1048576).toFixed(2) + " MB";
  const cellHTML = (v) => v === null || v === undefined
    ? '<td class="nil">NULL</td>'
    : `<td class="${typeof v === "number" ? "num" : ""}">${esc(v)}</td>`;

  let indexCatalog = [];
  let activeIndex = "";

  function indexKey(table, cols) { return table + "|" + cols.join(","); }

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
      b.type = "button";
      b.className = "tab" + (i === active ? " active" : "");
      b.textContent = t.name;
      b.onclick = () => { tabs[active].sql = editor.value; active = i; editor.value = t.sql; updateGutter(); renderTabs(); };
      el.appendChild(b);
    });
    const add = document.createElement("button");
    add.type = "button";
    add.className = "tab"; add.textContent = "+"; add.title = "New tab";
    add.onclick = () => { tabs[active].sql = editor.value; tabs.push({ name: "query " + (tabs.length + 1), sql: "" }); active = tabs.length - 1; editor.value = ""; updateGutter(); renderTabs(); };
    el.appendChild(add);
  }

  function renderSnippets() {
    const el = $("#snippets");
    SNIPPETS.forEach((s) => {
      const b = document.createElement("button");
      b.type = "button";
      b.className = "snippet"; b.textContent = s.label;
      b.onclick = () => { editor.value = s.sql; updateGutter(); editor.focus(); };
      el.appendChild(b);
    });
  }

  function renderHistory() {
    historyEl.innerHTML = "";
    history.slice(0, 30).forEach((h) => {
      const b = document.createElement("button");
      b.type = "button";
      b.className = "history-item"; b.textContent = h.replace(/\s+/g, " "); b.title = h;
      b.onclick = () => { editor.value = h; updateGutter(); editor.focus(); };
      historyEl.appendChild(b);
    });
  }

  function markActiveIndex() {
    document.querySelectorAll(".idx-link, #index-picker button").forEach((b) => {
      if (!b.dataset.key) return;
      b.classList.toggle("active", b.dataset.key === activeIndex);
    });
  }

  async function loadTables() {
    const el = $("#table-list");
    try {
      const tables = await (await fetch("/api/tables")).json();
      if (!tables.length) { el.innerHTML = '<p class="muted">No tables yet.</p>'; return; }
      el.innerHTML = "";
      tables.forEach((t) => {
        const d = document.createElement("div");
        d.className = "table-item";
        const idxCols = new Set(t.indexes.map((i) => i[0]));
        const pk = t.columns.slice(0, t.pkeys);
        const pkKey = indexKey(t.name, pk);
        const links = [`<button type="button" class="idx-link pk${pkKey === activeIndex ? " active" : ""}" data-key="${esc(pkKey)}" data-cols="${esc(pk.join(","))}">PRIMARY (${esc(pk.join(", "))})</button>`]
          .concat(t.indexes.map((cols) => {
            const key = indexKey(t.name, cols);
            return `<button type="button" class="idx-link${key === activeIndex ? " active" : ""}" data-key="${esc(key)}" data-cols="${esc(cols.join(","))}">INDEX (${esc(cols.join(", "))})</button>`;
          }));
        d.innerHTML = `<div class="t-name">${esc(t.name)}</div>
          <div class="t-meta">${t.columns.length} columns · ${t.indexes.length} index${t.indexes.length === 1 ? "" : "es"}</div>
          <div class="table-cols">${t.columns.map((c, i) =>
            `<span class="col-chip ${i < t.pkeys ? "pk" : idxCols.has(c) ? "idx" : ""}">${esc(c)} ${esc(t.types[i])}</span>`).join("")}</div>
          <div class="idx-links">${links.join("")}</div>`;
        d.querySelectorAll(".idx-link").forEach((b) => {
          b.onclick = (e) => {
            e.stopPropagation();
            loadIndex(t.name, b.dataset.cols.split(",").filter(Boolean));
          };
        });
        d.onclick = (e) => {
          if (e.detail === 2) { editor.value = `SELECT * FROM ${t.name} LIMIT 50;`; updateGutter(); run(); }
          else d.classList.toggle("open");
        };
        d.title = "double-click to preview rows";
        el.appendChild(d);
      });
    } catch (e) { el.innerHTML = `<p class="muted">failed to load tables</p>`; }
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
    card.className = "result-block";
    const head = document.createElement("div");
    head.className = "result-head";
    head.innerHTML = `<span class="stmt">${esc(r.statement)}</span>` +
      (r.plan ? `<span class="plan">${esc(r.plan)}</span>` : "") +
      `<span class="count">${r.affected} row${r.affected === 1 ? "" : "s"}</span>` +
      `<span class="time">${r.millis.toFixed(2)} ms</span>`;
    card.appendChild(head);
    if (r.columns) {
      const wrap = document.createElement("div");
      wrap.className = "grid-wrap";
      const rows = r.rows || [];
      wrap.innerHTML = `<table class="grid"><thead><tr>${r.columns.map((c) => `<th>${esc(c)}</th>`).join("")}</tr></thead>
        <tbody>${rows.map((row) => `<tr>${row.map(cellHTML).join("")}</tr>`).join("")}</tbody></table>`;
      if (!rows.length) {
        const m = document.createElement("p");
        m.className = "result-msg muted";
        m.textContent = "empty result set";
        wrap.appendChild(m);
      }
      card.appendChild(wrap);
    } else {
      const m = document.createElement("p");
      m.className = "result-msg"; m.textContent = r.message;
      card.appendChild(m);
    }
    return card;
  }

  function renderError(msg) {
    const card = document.createElement("div");
    card.className = "result-block bad";
    card.innerHTML = `<div class="result-head"><span class="stmt">Error</span><span class="count">rolled back</span></div>
      <p class="result-msg err">${esc(msg)}</p>`;
    return card;
  }

  async function run() {
    const sql = editor.value.trim();
    if (!sql) return;
    setMode("sql");
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
      loadTables(); loadStats(); loadPages(); loadIndexCatalog();
    } catch (e) {
      results.innerHTML = "";
      results.appendChild(renderError("request failed: " + e.message));
    } finally {
      runBtn.disabled = false;
    }
  }

  function setMode(mode) {
    const main = document.querySelector(".main");
    const same = main.classList.contains("mode-" + mode);
    main.classList.remove("mode-sql", "mode-indexes", "mode-pages");
    main.classList.add("mode-" + mode);
    document.querySelectorAll("#view-switch .tab").forEach((b) => {
      b.classList.toggle("active", b.dataset.view === mode);
    });
    if (mode === "sql") {
      $("#index-title").textContent = "Rows";
      $("#index-meta").textContent = "";
    } else if (mode === "indexes" && !activeIndex) {
      $("#index-title").textContent = "Keys";
    }
    if (!same && mode === "indexes") loadIndexCatalog();
    if (mode === "pages") {
      loadPages();
      const panel = document.getElementById("page-panel");
      if (panel) panel.scrollIntoView({ block: "nearest" });
    }
  }

  async function loadIndexCatalog() {
    const el = $("#index-picker");
    try {
      const resp = await fetch("/api/indexes");
      indexCatalog = await resp.json();
    } catch (e) {
      el.innerHTML = '<p class="muted">failed to load indexes</p>';
      return;
    }
    if (!indexCatalog.length) {
      el.innerHTML = '<p class="muted">No indexes yet.</p>';
      return;
    }
    el.innerHTML = "";
    indexCatalog.forEach((ix) => {
      const b = document.createElement("button");
      b.type = "button";
      const key = indexKey(ix.table, ix.columns);
      b.dataset.key = key;
      b.className = [ix.kind === "PRIMARY" ? "pk" : "", key === activeIndex ? "active" : ""].filter(Boolean).join(" ");
      b.textContent = ix.table + " " + ix.kind + " (" + ix.columns.join(", ") + ") prefix " + ix.prefix;
      b.onclick = () => loadIndex(ix.table, ix.columns);
      el.appendChild(b);
    });
  }

  async function loadIndex(table, cols) {
    activeIndex = indexKey(table, cols);
    setMode("indexes");
    $("#index-title").textContent = table;
    $("#index-meta").textContent = cols.join(", ");
    const body = $("#index-body");
    body.innerHTML = '<p class="muted">Loading index…</p>';
    markActiveIndex();
    const q = new URLSearchParams({ table, cols: cols.join(","), limit: "200" });
    const resp = await fetch("/api/indexes?" + q);
    if (!resp.ok) {
      body.innerHTML = `<p class="result-msg err">${esc(await resp.text())}</p>`;
      return;
    }
    const data = await resp.json();
    const rows = data.rows || [];
    const head = (data.columns || []).map((c) => `<th>${esc(c)}</th>`).join("");
    body.innerHTML = `<div class="grid-wrap"><table class="grid"><thead><tr>${head}</tr></thead><tbody>${
      rows.map((row) => `<tr>${row.map(cellHTML).join("")}</tr>`).join("")
    }</tbody></table></div><p class="muted">${rows.length} key${rows.length === 1 ? "" : "s"}</p>`;
    markActiveIndex();
  }

  function pageDetail(p) {
    const fill = p.used ? Math.round((100 * p.used) / 4096) + "%" : "—";
    const keys = p.keys ? p.keys + " keys" : "—";
    const used = p.used ? fmtBytes(p.used) : "—";
    return "page " + p.id + " · " + p.kind + " · " + keys + " · " + used + " · " + fill;
  }

  async function loadPages() {
    const summary = $("#page-summary");
    const body = $("#page-body");
    const resp = await fetch("/api/pages");
    if (!resp.ok) {
      body.innerHTML = `<p class="result-msg err">${esc(await resp.text())}</p>`;
      return;
    }
    const pm = await resp.json();
    const cards = [
      ["height", pm.height],
      ["leaf keys", pm.leaf_keys],
      ["leaves", pm.leaves],
      ["internal", pm.internal],
      ["free items", pm.free_items],
      ["unused", pm.unused],
    ];
    summary.innerHTML = cards.map(([k, v]) => `<div><dt>${esc(k)}</dt><dd>${esc(v)}</dd></div>`).join("");
    const pages = pm.pages || [];
    body.innerHTML = pages.map((p) => {
      const fill = p.used ? Math.round((100 * p.used) / 4096) + "%" : "—";
      const keys = p.keys ? p.keys + " keys" : "—";
      return `<button type="button" class="ticket ${esc(p.kind)}" data-detail="${esc(pageDetail(p))}"><span class="tid">${p.id}</span><span class="tkind">${esc(p.kind)}</span><span class="tmeta">${esc(keys)} · ${esc(fill)}</span></button>`;
    }).join("") + `<p class="strip-note">${pm.truncated ? "showing the first " + pages.length + " pages" : pages.length + " pages · " + fmtBytes((pm.used || 0) * 4096)}</p>`;
    if (pages[0]) $("#page-focus").textContent = pageDetail(pages[0]);
    body.querySelectorAll(".ticket").forEach((btn) => {
      btn.onclick = () => {
        body.querySelectorAll(".ticket.on").forEach((n) => n.classList.remove("on"));
        btn.classList.add("on");
        $("#page-focus").textContent = btn.dataset.detail;
      };
    });
  }

  document.querySelectorAll("#view-switch .tab").forEach((b) => {
    b.onclick = () => setMode(b.dataset.view);
  });
  runBtn.onclick = run;
  $("#clear-btn").onclick = () => { editor.value = ""; updateGutter(); editor.focus(); };
  $("#refresh-tables").onclick = () => { loadTables(); loadStats(); loadIndexCatalog(); loadPages(); };
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
  loadTables(); loadStats(); loadIndexCatalog(); loadPages();
  setInterval(loadStats, 5000);
})();
