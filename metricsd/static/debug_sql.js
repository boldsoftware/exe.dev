(function () {
  const $ = (id) => document.getElementById(id);
  const form = $("sqlForm");
  const qEl = $("q");
  const limitEl = $("limit");
  const timeoutEl = $("timeout");
  const statusEl = $("status");
  const errEl = $("error");
  const resultEl = $("result");
  const csvLink = $("csvLink");
  const runBtn = $("run");

  // Restore last query from localStorage.
  try {
    const last = localStorage.getItem("metricsd.debugSQL.q");
    if (last && !qEl.value) qEl.value = last;
  } catch (_) {}
  // Fall back to a useful sample.
  if (!qEl.value) {
    qEl.value = "SELECT vm_name, count(*) AS n\nFROM vm_metrics\nGROUP BY 1\nORDER BY n DESC\nLIMIT 20";
  }

  qEl.addEventListener("keydown", (e) => {
    if ((e.ctrlKey || e.metaKey) && e.key === "Enter") {
      e.preventDefault();
      form.requestSubmit();
    }
  });

  form.addEventListener("submit", async (e) => {
    e.preventDefault();
    await run();
  });

  function updateCsvLink() {
    const params = new URLSearchParams({
      q: qEl.value,
      format: "csv",
      limit: limitEl.value || "1000",
      timeout: timeoutEl.value || "30s",
    });
    csvLink.href = "/debug/sql/run?" + params.toString();
  }
  qEl.addEventListener("input", updateCsvLink);
  limitEl.addEventListener("input", updateCsvLink);
  timeoutEl.addEventListener("input", updateCsvLink);
  updateCsvLink();

  function setStatus(msg) { statusEl.textContent = msg || ""; }
  function setError(msg) { errEl.textContent = msg || ""; }

  function isNumber(v) { return typeof v === "number" || (typeof v === "string" && /^-?\d+(\.\d+)?$/.test(v)); }

  function renderTable(cols, rows, truncated) {
    while (resultEl.firstChild) resultEl.removeChild(resultEl.firstChild);
    if (!cols || !cols.length) return;
    const thead = document.createElement("thead");
    const trh = document.createElement("tr");
    cols.forEach((c) => {
      const th = document.createElement("th");
      th.textContent = c;
      trh.appendChild(th);
    });
    thead.appendChild(trh);
    resultEl.appendChild(thead);
    const tbody = document.createElement("tbody");
    rows.forEach((row) => {
      const tr = document.createElement("tr");
      row.forEach((v) => {
        const td = document.createElement("td");
        if (v === null || v === undefined) {
          td.textContent = "NULL";
          td.className = "null";
        } else if (typeof v === "object") {
          td.textContent = JSON.stringify(v);
        } else {
          td.textContent = String(v);
          if (isNumber(v)) td.className = "num";
        }
        tr.appendChild(td);
      });
      tbody.appendChild(tr);
    });
    resultEl.appendChild(tbody);
  }

  async function run() {
    setError("");
    setStatus("running…");
    runBtn.disabled = true;
    try {
      localStorage.setItem("metricsd.debugSQL.q", qEl.value);
    } catch (_) {}
    const params = new URLSearchParams({
      limit: limitEl.value || "1000",
      timeout: timeoutEl.value || "30s",
    });
    try {
      const resp = await fetch("/debug/sql/run?" + params.toString(), {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ q: qEl.value }),
      });
      // Some error paths (overload, bad form/JSON, method not
      // allowed) reply with text/plain; only application/json
      // bodies follow the structured shape.
      const ct = resp.headers.get("content-type") || "";
      if (!ct.includes("application/json")) {
        const txt = await resp.text();
        setError(`HTTP ${resp.status}: ${txt.trim()}`);
        renderTable([], [], false);
        setStatus("");
        return;
      }
      const data = await resp.json();
      if (data.error) {
        setError(data.error);
        renderTable(data.columns || [], data.rows || [], data.truncated);
        setStatus("");
        return;
      }
      renderTable(data.columns || [], data.rows || [], data.truncated);
      const trunc = data.truncated ? " (truncated)" : "";
      setStatus(`${(data.rows || []).length} rows in ${data.elapsed_ms} ms${trunc}`);
    } catch (err) {
      setError(String(err));
      setStatus("");
    } finally {
      runBtn.disabled = false;
    }
  }
})();
