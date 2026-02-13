// sparklines.js — parquet-powered sparkline dashboard using DuckDB-WASM
import * as duckdb from 'https://cdn.jsdelivr.net/npm/@duckdb/duckdb-wasm@1.29.0/+esm';

const SPARK_W = 120;
const SPARK_H = 28;
const DPR = window.devicePixelRatio || 1;

const COLORS = {
  primary: '#1f77b4',
  secondary: '#ff7f0e',
  tertiary: '#2ca02c',
  ref: '#999',
};

const CHART_DEFS = [
  { title: 'Disk', sortKey: 'disk_used_bytes', lines: [
    {key: 'disk_size_bytes', color: COLORS.ref, dash: [4,3], label: 'Prov'},
    {key: 'disk_logical_used_bytes', color: COLORS.secondary, label: 'Logical'},
    {key: 'disk_used_bytes', color: COLORS.primary, label: 'On-Disk'},
  ]},
  { title: 'Memory RSS', sortKey: 'memory_rss_bytes', lines: [
    {key: 'memory_nominal_bytes', color: COLORS.ref, dash: [4,3], label: 'Nom'},
    {key: 'memory_rss_bytes', color: COLORS.primary, label: 'RSS'},
  ]},
  { title: 'Memory Swap', sortKey: 'memory_swap_bytes', lines: [
    {key: 'memory_nominal_bytes', color: COLORS.ref, dash: [4,3], label: 'Nom'},
    {key: 'memory_swap_bytes', color: COLORS.primary, label: 'Swap'},
  ]},
  { title: 'CPU', sortKey: '_cpu_pct', derived: true, lines: [
    {key: '_cpu_100', color: COLORS.ref, dash: [4,3], label: 'Nom'},
    {key: '_cpu_pct', color: COLORS.primary, label: 'Used'},
  ]},
  { title: 'Network', sortKey: '_net_tx_mbps', derived: true, lines: [
    {key: '_net_tx_mbps', color: COLORS.primary, label: 'TX'},
    {key: '_net_rx_mbps', color: COLORS.secondary, label: 'RX'},
  ]},
  { title: 'IO', sortKey: '_io_write_bps', derived: true, lines: [
    {key: '_io_read_bps', color: COLORS.primary, label: 'Read'},
    {key: '_io_write_bps', color: COLORS.secondary, label: 'Write'},
  ]},
];

const COLUMNS = [
  {title: 'VM', key: 'name'},
  {title: 'Host', key: 'host'},
  {title: 'Group', key: 'resource_group'},
  ...CHART_DEFS.map(c => ({title: c.title, key: c.sortKey})),
];

function fmtBytes(v) {
  if (v >= 1e12) return (v/1e12).toFixed(1) + 'T';
  if (v >= 1e9) return (v/1e9).toFixed(1) + 'G';
  if (v >= 1e6) return (v/1e6).toFixed(1) + 'M';
  if (v >= 1e3) return (v/1e3).toFixed(1) + 'K';
  return v.toFixed(0);
}

function fmtRate(mbps) {
  if (mbps >= 1000) return (mbps/1000).toFixed(1) + ' Gbps';
  if (mbps >= 1) return mbps.toFixed(1) + ' Mbps';
  const kbps = mbps * 1000;
  if (kbps >= 1) return kbps.toFixed(0) + ' Kbps';
  const bps = mbps * 1e6;
  if (bps >= 1) return bps.toFixed(0) + ' bps';
  return '0';
}

function fmtCPU(cpuPct, nominalCpus) {
  const used = (cpuPct / 100) * nominalCpus;
  return used.toFixed(2) + '/' + nominalCpus;
}

function fmtBytesPerSec(bps) {
  if (bps >= 1e9) return (bps/1e9).toFixed(1) + ' GB/s';
  if (bps >= 1e6) return (bps/1e6).toFixed(1) + ' MB/s';
  if (bps >= 1e3) return (bps/1e3).toFixed(1) + ' KB/s';
  if (bps >= 1) return bps.toFixed(0) + ' B/s';
  return '0';
}

function fmtVal(v, chartTitle) {
  if (chartTitle === 'CPU') return '';
  if (chartTitle === 'Network') return fmtRate(v);
  if (chartTitle === 'IO') return fmtBytesPerSec(v);
  return fmtBytes(v);
}

function fmtFileSize(bytes) {
  if (bytes >= 1e6) return (bytes / 1e6).toFixed(1) + ' MB';
  if (bytes >= 1e3) return (bytes / 1e3).toFixed(1) + ' KB';
  return bytes + ' B';
}

function computeDerived(rows) {
  for (let i = 0; i < rows.length; i++) {
    rows[i]._cpu_100 = 100;
    if (i === 0) {
      rows[i]._cpu_pct = null;
      rows[i]._net_tx_mbps = null;
      rows[i]._net_rx_mbps = null;
      rows[i]._io_read_bps = null;
      rows[i]._io_write_bps = null;
      continue;
    }
    const prev = rows[i - 1];
    const dt = (rows[i].timestamp - prev.timestamp) / 1000;
    if (dt <= 0) {
      rows[i]._cpu_pct = null;
      rows[i]._net_tx_mbps = null;
      rows[i]._net_rx_mbps = null;
      rows[i]._io_read_bps = null;
      rows[i]._io_write_bps = null;
      continue;
    }
    const cpuD = rows[i].cpu_used_cumulative_seconds - prev.cpu_used_cumulative_seconds;
    rows[i]._cpu_pct = (cpuD >= 0 && rows[i].cpu_nominal > 0) ? (cpuD / dt / rows[i].cpu_nominal) * 100 : null;
    const txD = rows[i].network_tx_bytes - prev.network_tx_bytes;
    rows[i]._net_tx_mbps = txD >= 0 ? (txD * 8 / 1e6) / dt : null;
    const rxD = rows[i].network_rx_bytes - prev.network_rx_bytes;
    rows[i]._net_rx_mbps = rxD >= 0 ? (rxD * 8 / 1e6) / dt : null;
    const ioRD = rows[i].io_read_bytes - prev.io_read_bytes;
    rows[i]._io_read_bps = ioRD >= 0 ? ioRD / dt : null;
    const ioWD = rows[i].io_write_bytes - prev.io_write_bytes;
    rows[i]._io_write_bps = ioWD >= 0 ? ioWD / dt : null;
  }
}

function drawSparkline(canvas, rows, chartDef) {
  const w = SPARK_W, h = SPARK_H;
  canvas.width = w * DPR;
  canvas.height = h * DPR;
  canvas.style.width = w + 'px';
  canvas.style.height = h + 'px';
  const ctx = canvas.getContext('2d');
  ctx.scale(DPR, DPR);
  ctx.clearRect(0, 0, w, h);

  if (rows.length < 2) return;

  let yMax = 0;
  for (const line of chartDef.lines) {
    for (const r of rows) {
      const v = r[line.key];
      if (v != null && v > yMax) yMax = v;
    }
  }
  if (yMax === 0) yMax = 1;

  const tMin = rows[0].timestamp;
  const tMax = rows[rows.length - 1].timestamp;
  const tRange = tMax - tMin || 1;

  const pad = 2;
  const plotW = w - pad * 2;
  const plotH = h - pad * 2;

  for (const line of chartDef.lines) {
    ctx.beginPath();
    ctx.strokeStyle = line.color;
    ctx.lineWidth = line.dash ? 0.8 : 1.2;
    if (line.dash) ctx.setLineDash(line.dash.map(d => d * 0.7));
    else ctx.setLineDash([]);

    let started = false;
    let pointCount = 0;
    let lastX, lastY;
    for (const r of rows) {
      const v = r[line.key];
      if (v == null) { started = false; continue; }
      const x = pad + ((r.timestamp - tMin) / tRange) * plotW;
      const y = pad + plotH - (v / yMax) * plotH;
      if (!started) { ctx.moveTo(x, y); started = true; }
      else ctx.lineTo(x, y);
      lastX = x; lastY = y;
      pointCount++;
    }
    ctx.stroke();

    if (pointCount === 1 && !line.dash) {
      ctx.beginPath();
      ctx.fillStyle = line.color;
      ctx.arc(lastX, lastY, 1.5, 0, Math.PI * 2);
      ctx.fill();
    }
  }
  ctx.setLineDash([]);
}

function lastNonNull(rows, key) {
  for (let i = rows.length - 1; i >= 0; i--) {
    if (rows[i][key] != null) return rows[i][key];
  }
  return 0;
}

// Convert DuckDB arrow result to array of plain objects
function arrowToObjects(result) {
  const rows = [];
  for (let i = 0; i < result.numRows; i++) {
    const row = {};
    for (const field of result.schema.fields) {
      const col = result.getChild(field.name);
      let val = col.get(i);
      // Convert BigInt to Number for numeric fields
      if (typeof val === 'bigint') val = Number(val);
      // Convert Date objects to timestamps
      if (val instanceof Date) val = val.getTime();
      row[field.name] = val;
    }
    rows.push(row);
  }
  return rows;
}

async function initDuckDB() {
  const JSDELIVR_BUNDLES = duckdb.getJsDelivrBundles();
  const bundle = await duckdb.selectBundle(JSDELIVR_BUNDLES);
  const worker_url = URL.createObjectURL(
    new Blob([`importScripts("${bundle.mainWorker}");`], {type: 'text/javascript'})
  );
  const worker = new Worker(worker_url);
  const logger = new duckdb.ConsoleLogger();
  const db = new duckdb.AsyncDuckDB(logger, worker);
  await db.instantiate(bundle.mainModule, bundle.pthreadWorker);
  URL.revokeObjectURL(worker_url);
  return db;
}

let db = null;
let conn = null;

async function ensureDuckDB() {
  if (db) return;
  db = await initDuckDB();
  conn = await db.connect();
}

async function loadData(hours) {
  const errorEl = document.getElementById('error');
  const loadingEl = document.getElementById('loading');
  const vis = document.getElementById('vis');
  errorEl.textContent = '';
  loadingEl.hidden = false;
  vis.innerHTML = '';

  // Step 1: Fetch parquet file
  loadingEl.textContent = 'Downloading parquet snapshot...';
  const resp = await fetch('/query/sparkline?hours=' + hours);
  if (!resp.ok) throw new Error('fetch failed: ' + resp.status);
  const parquetBuffer = await resp.arrayBuffer();
  const parquetBytes = parquetBuffer.byteLength;

  loadingEl.textContent = `Loading DuckDB-WASM (${fmtFileSize(parquetBytes)} snapshot)...`;

  // Step 2: Initialize DuckDB-WASM (reuse across reloads)
  await ensureDuckDB();

  // Step 3: Register parquet file (drop old one first)
  try { await db.dropFile('metrics.parquet'); } catch (_) {}
  await db.registerFileBuffer('metrics.parquet', new Uint8Array(parquetBuffer));

  // Step 4: Get row count
  const countResult = await conn.query(`SELECT count(*) as cnt FROM 'metrics.parquet'`);
  const totalRows = Number(countResult.get(0).cnt);

  if (totalRows === 0) {
    const infoEl = document.getElementById('parquet-info');
    infoEl.textContent = `Parquet: ${fmtFileSize(parquetBytes)}, 0 rows`;
    loadingEl.hidden = true;
    document.getElementById('error').textContent = 'No metrics data available.';
    return { byVM: {}, vmHost: {}, vmGroup: {} };
  }

  // Step 5: Get time range
  const rangeResult = await conn.query(`SELECT min(timestamp) as min_t, max(timestamp) as max_t FROM 'metrics.parquet'`);
  const rangeRow = rangeResult.get(0);
  const minT = new Date(rangeRow.min_t);
  const maxT = new Date(rangeRow.max_t);
  const tfmt = d => d.toLocaleString(undefined, {month:'short',day:'numeric',hour:'2-digit',minute:'2-digit'});
  document.getElementById('range').textContent = tfmt(minT) + ' \u2013 ' + tfmt(maxT);

  // Step 6: Get all VM names, hosts, groups
  const vmListResult = await conn.query(`
    SELECT DISTINCT vm_name,
      first(host) as host,
      first(resource_group) as resource_group
    FROM 'metrics.parquet'
    GROUP BY vm_name
    ORDER BY vm_name
  `);
  const vmList = arrowToObjects(vmListResult);

  const vmHost = {};
  const vmGroup = {};
  for (const vm of vmList) {
    vmHost[vm.vm_name] = vm.host;
    vmGroup[vm.vm_name] = vm.resource_group || '';
  }

  // Populate host filter
  const hosts = [...new Set(Object.values(vmHost))].sort();
  const hostSel = document.getElementById('hostFilter');
  const prevHost = hostSel.value;
  hostSel.innerHTML = '<option value="">All</option>';
  for (const h of hosts) {
    const o = document.createElement('option');
    o.value = h; o.textContent = h;
    hostSel.appendChild(o);
  }
  hostSel.value = prevHost;

  // Populate group filter
  const groups = [...new Set(Object.values(vmGroup))].filter(g => g).sort();
  const groupSel = document.getElementById('groupFilter');
  const prevGroup = groupSel.value;
  groupSel.innerHTML = '<option value="">All</option>';
  for (const g of groups) {
    const o = document.createElement('option');
    o.value = g; o.textContent = g;
    groupSel.appendChild(o);
  }
  groupSel.value = prevGroup;

  // Update info bar
  const infoEl = document.getElementById('parquet-info');
  infoEl.textContent = `Parquet: ${fmtFileSize(parquetBytes)}, ${totalRows.toLocaleString()} rows, ${vmList.length} VMs`;

  // Load all VM data into memory
  loadingEl.textContent = 'Processing metrics data...';
  const allDataResult = await conn.query(`
    SELECT * FROM 'metrics.parquet' ORDER BY vm_name, timestamp ASC
  `);
  const allRows = arrowToObjects(allDataResult);

  const byVM = {};
  for (const row of allRows) {
    if (!byVM[row.vm_name]) byVM[row.vm_name] = [];
    byVM[row.vm_name].push(row);
  }

  for (const rows of Object.values(byVM)) {
    computeDerived(rows);
  }

  loadingEl.hidden = true;
  return { byVM, vmHost, vmGroup };
}

(async function() {
  const errorEl = document.getElementById('error');
  const loadingEl = document.getElementById('loading');

  try {
    let currentHours = 3;
    let { byVM, vmHost, vmGroup } = await loadData(currentHours);

    const statsEl = document.getElementById('stats');
    let sortKey = 'name';
    let sortDesc = true;

    // Batch rendering: 100 VMs at a time
    const BATCH_SIZE = 100;
    let currentRenderAbort = null;
    let currentObserver = null;

    function render() {
      // Cancel any in-progress progressive render
      if (currentRenderAbort) {
        currentRenderAbort.abort = true;
      }
      if (currentObserver) {
        currentObserver.disconnect();
        currentObserver = null;
      }
      const abortToken = { abort: false };
      currentRenderAbort = abortToken;

      const hostF = document.getElementById('hostFilter').value;
      const groupF = document.getElementById('groupFilter').value;
      const nameF = document.getElementById('nameFilter').value.toLowerCase();

      let vms = Object.keys(byVM);
      if (hostF) vms = vms.filter(v => vmHost[v] === hostF);
      if (groupF) vms = vms.filter(v => vmGroup[v] === groupF);
      if (nameF) vms = vms.filter(v => v.toLowerCase().includes(nameF));

      if (sortKey === 'name') {
        vms.sort();
        if (sortDesc) vms.reverse();
      } else if (sortKey === 'host') {
        vms.sort((a, b) => {
          const cmp = vmHost[a].localeCompare(vmHost[b]);
          return sortDesc ? -cmp : cmp;
        });
      } else if (sortKey === 'resource_group') {
        vms.sort((a, b) => {
          const cmp = vmGroup[a].localeCompare(vmGroup[b]);
          return sortDesc ? -cmp : cmp;
        });
      } else {
        vms.sort((a, b) => {
          const va = lastNonNull(byVM[a], sortKey), vb = lastNonNull(byVM[b], sortKey);
          return sortDesc ? vb - va : va - vb;
        });
      }

      statsEl.textContent = vms.length + ' VMs';

      const vis = document.getElementById('vis');
      vis.innerHTML = '';

      if (vms.length === 0) {
        vis.innerHTML = '<p style="color:#666">No matching VMs.</p>';
        return;
      }

      const table = document.createElement('table');
      table.className = 'sparklines';
      const thead = document.createElement('thead');
      const hr = document.createElement('tr');
      for (const col of COLUMNS) {
        const th = document.createElement('th');
        const titleSpan = document.createElement('span');
        titleSpan.textContent = col.title;
        th.appendChild(titleSpan);

        const arrows = document.createElement('span');
        arrows.className = 'sort-arrows';
        const upArrow = document.createElement('span');
        upArrow.className = 'arrow' + (sortKey === col.key && !sortDesc ? ' active' : '');
        upArrow.textContent = '▲';
        const downArrow = document.createElement('span');
        downArrow.className = 'arrow' + (sortKey === col.key && sortDesc ? ' active' : '');
        downArrow.textContent = '▼';
        arrows.appendChild(upArrow);
        arrows.appendChild(downArrow);
        th.appendChild(arrows);

        const chartDef = CHART_DEFS.find(c => c.sortKey === col.key);
        if (chartDef) {
          const legend = document.createElement('span');
          legend.className = 'col-sub';
          for (let i = 0; i < chartDef.lines.length; i++) {
            const line = chartDef.lines[i];
            if (i > 0) legend.appendChild(document.createTextNode(' / '));
            if (line.dash) {
              const dashEl = document.createElement('span');
              dashEl.className = 'color-line';
              dashEl.style.borderColor = line.color;
              legend.appendChild(dashEl);
            } else {
              const dot = document.createElement('span');
              dot.className = 'color-dot';
              dot.style.backgroundColor = line.color;
              legend.appendChild(dot);
            }
            legend.appendChild(document.createTextNode(line.label));
          }
          th.appendChild(legend);
        }

        th.addEventListener('click', () => {
          if (sortKey === col.key) {
            sortDesc = !sortDesc;
          } else {
            sortKey = col.key;
            sortDesc = col.key !== 'name' && col.key !== 'host' && col.key !== 'resource_group';
          }
          render();
        });
        hr.appendChild(th);
      }
      thead.appendChild(hr);
      table.appendChild(thead);

      const tbody = document.createElement('tbody');
      table.appendChild(tbody);
      vis.appendChild(table);

      // Progressive rendering
      currentObserver = new IntersectionObserver((entries) => {
        for (const entry of entries) {
          if (entry.isIntersecting) {
            const canvas = entry.target;
            const data = canvas._sparkData;
            if (data && !canvas._drawn) {
              drawSparkline(canvas, data.rows, data.chartDef);
              canvas._drawn = true;
            }
            currentObserver.unobserve(canvas);
          }
        }
      }, {rootMargin: '200px'});
      const observer = currentObserver;

      let batchIndex = 0;

      function renderBatch() {
        if (abortToken.abort) return;

        const start = batchIndex * BATCH_SIZE;
        const end = Math.min(start + BATCH_SIZE, vms.length);
        const fragment = document.createDocumentFragment();

        for (let i = start; i < end; i++) {
          const vm = vms[i];
          const rows = byVM[vm];
          const tr = document.createElement('tr');

          const tdName = document.createElement('td');
          tdName.className = 'vm-name';
          tdName.textContent = vm;
          tdName.title = vm;
          tr.appendChild(tdName);

          const tdHost = document.createElement('td');
          tdHost.className = 'vm-host';
          tdHost.textContent = vmHost[vm];
          tr.appendChild(tdHost);

          const tdGroup = document.createElement('td');
          tdGroup.className = 'vm-group';
          tdGroup.textContent = vmGroup[vm];
          tdGroup.title = vmGroup[vm];
          tr.appendChild(tdGroup);

          for (const chartDef of CHART_DEFS) {
            const td = document.createElement('td');
            const wrap = document.createElement('div');
            wrap.style.display = 'flex';
            wrap.style.alignItems = 'center';
            wrap.style.gap = '4px';

            const canvas = document.createElement('canvas');
            canvas.width = SPARK_W * DPR;
            canvas.height = SPARK_H * DPR;
            canvas.style.width = SPARK_W + 'px';
            canvas.style.height = SPARK_H + 'px';
            wrap.appendChild(canvas);

            const labelDiv = document.createElement('div');
            labelDiv.className = 'spark-label';

            if (chartDef.title === 'CPU') {
              const cpuPct = lastNonNull(rows, '_cpu_pct');
              const cpuNom = lastNonNull(rows, '_cpu_100') > 0 ? rows[rows.length - 1].cpu_nominal : 0;
              labelDiv.textContent = fmtCPU(cpuPct, cpuNom);
            } else {
              const parts = [];
              for (const line of chartDef.lines) {
                if (line.dash) continue;
                const v = lastNonNull(rows, line.key);
                parts.push(fmtVal(v, chartDef.title));
              }
              labelDiv.innerHTML = parts.join('<br>');
            }
            wrap.appendChild(labelDiv);

            td.appendChild(wrap);
            tr.appendChild(td);

            canvas._sparkData = {rows, chartDef};
            observer.observe(canvas);
          }

          fragment.appendChild(tr);
        }

        tbody.appendChild(fragment);
        batchIndex++;

        // Update progress
        const rendered = Math.min(end, vms.length);
        if (rendered < vms.length) {
          statsEl.textContent = `${rendered}/${vms.length} VMs`;
          requestAnimationFrame(renderBatch);
        } else {
          statsEl.textContent = vms.length + ' VMs';
        }
      }

      renderBatch();
    }

    // Range buttons
    const rangeButtons = document.querySelectorAll('#rangeButtons button');
    function updateActiveButton() {
      for (const btn of rangeButtons) {
        btn.classList.toggle('active', Number(btn.dataset.hours) === currentHours);
      }
    }
    updateActiveButton();
    for (const btn of rangeButtons) {
      btn.addEventListener('click', async () => {
        const h = Number(btn.dataset.hours);
        if (h === currentHours) return;
        currentHours = h;
        updateActiveButton();
        try {
          ({ byVM, vmHost, vmGroup } = await loadData(h));
          render();
        } catch (e) {
          loadingEl.hidden = true;
          errorEl.textContent = 'Error: ' + e.message;
          console.error(e);
        }
      });
    }

    const hostSel = document.getElementById('hostFilter');
    const groupSel = document.getElementById('groupFilter');
    hostSel.addEventListener('change', render);
    groupSel.addEventListener('change', render);
    document.getElementById('nameFilter').addEventListener('input', render);
    render();

  } catch (e) {
    loadingEl.hidden = true;
    errorEl.textContent = 'Error: ' + e.message;
    console.error(e);
  }
})();
