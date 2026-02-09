// sparklines.js — canvas-based sparkline dashboard for metricsd
// Renders thousands of VMs efficiently using <canvas> elements.

const SPARK_W = 120;
const SPARK_H = 28;
const DPR = window.devicePixelRatio || 1;

const COLORS = {
  primary: '#1f77b4',
  secondary: '#ff7f0e',
  tertiary: '#2ca02c',
  ref: '#999',
};

// Chart definitions: each produces one canvas per VM.
// sortKey is the field used when sorting by this column.
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
  { title: 'CPU %', sortKey: '_cpu_pct', derived: true, lines: [
    {key: '_cpu_100', color: COLORS.ref, dash: [4,3], label: 'Nom'},
    {key: '_cpu_pct', color: COLORS.primary, label: 'Used'},
  ]},
  { title: 'Network', sortKey: '_net_tx_mbps', derived: true, lines: [
    {key: '_net_tx_mbps', color: COLORS.primary, label: 'TX'},
    {key: '_net_rx_mbps', color: COLORS.secondary, label: 'RX'},
  ]},
];

// Column header definitions for the sortable table.
// 'key' is the sort field; null means sort by name.
const COLUMNS = [
  {title: 'VM', key: 'name'},
  {title: 'Host', key: 'host'},
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

function fmtVal(v, chartTitle) {
  if (chartTitle === 'CPU %') return v.toFixed(1) + '%';
  if (chartTitle === 'Network') return fmtRate(v);
  return fmtBytes(v);
}

function computeDerived(rows) {
  for (let i = 0; i < rows.length; i++) {
    rows[i]._cpu_100 = 100;
    if (i === 0) {
      rows[i]._cpu_pct = null;
      rows[i]._net_tx_mbps = null;
      rows[i]._net_rx_mbps = null;
      continue;
    }
    const prev = rows[i - 1];
    const dt = (new Date(rows[i].timestamp) - new Date(prev.timestamp)) / 1000;
    if (dt <= 0) {
      rows[i]._cpu_pct = null;
      rows[i]._net_tx_mbps = null;
      rows[i]._net_rx_mbps = null;
      continue;
    }
    const cpuD = rows[i].cpu_used_cumulative_seconds - prev.cpu_used_cumulative_seconds;
    rows[i]._cpu_pct = (cpuD >= 0 && rows[i].cpu_nominal > 0) ? (cpuD / dt / rows[i].cpu_nominal) * 100 : null;
    const txD = rows[i].network_tx_bytes - prev.network_tx_bytes;
    rows[i]._net_tx_mbps = txD >= 0 ? (txD * 8 / 1e6) / dt : null;
    const rxD = rows[i].network_rx_bytes - prev.network_rx_bytes;
    rows[i]._net_rx_mbps = rxD >= 0 ? (rxD * 8 / 1e6) / dt : null;
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

  const tMin = new Date(rows[0].timestamp).getTime();
  const tMax = new Date(rows[rows.length - 1].timestamp).getTime();
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
    for (const r of rows) {
      const v = r[line.key];
      if (v == null) { started = false; continue; }
      const x = pad + ((new Date(r.timestamp).getTime() - tMin) / tRange) * plotW;
      const y = pad + plotH - (v / yMax) * plotH;
      if (!started) { ctx.moveTo(x, y); started = true; }
      else ctx.lineTo(x, y);
    }
    ctx.stroke();
  }
  ctx.setLineDash([]);
}

function lastNonNull(rows, key) {
  for (let i = rows.length - 1; i >= 0; i--) {
    if (rows[i][key] != null) return rows[i][key];
  }
  return 0;
}

(async function() {
  const errorEl = document.getElementById('error');
  const loadingEl = document.getElementById('loading');
  try {
    const resp = await fetch('/query/sparkline?hours=24');
    if (!resp.ok) throw new Error('fetch failed: ' + resp.status);
    const data = await resp.json();
    if (!data.metrics || data.metrics.length === 0) {
      loadingEl.hidden = true;
      errorEl.textContent = 'No metrics data available.';
      return;
    }

    // Show time range
    let minT = Infinity, maxT = -Infinity;
    for (const m of data.metrics) {
      const t = new Date(m.timestamp).getTime();
      if (t < minT) minT = t;
      if (t > maxT) maxT = t;
    }
    const tfmt = d => new Date(d).toLocaleString(undefined, {month:'short',day:'numeric',hour:'2-digit',minute:'2-digit'});
    document.getElementById('range').textContent = tfmt(minT) + ' \u2013 ' + tfmt(maxT);

    // Group by vm_name
    const byVM = {};
    const vmHost = {};
    for (const m of data.metrics) {
      if (!byVM[m.vm_name]) { byVM[m.vm_name] = []; vmHost[m.vm_name] = m.host; }
      byVM[m.vm_name].push(m);
    }

    // Compute derived fields
    for (const rows of Object.values(byVM)) {
      computeDerived(rows);
    }

    // Populate host filter
    const hosts = [...new Set(Object.values(vmHost))].sort();
    const hostSel = document.getElementById('hostFilter');
    for (const h of hosts) {
      const o = document.createElement('option');
      o.value = h; o.textContent = h;
      hostSel.appendChild(o);
    }

    const statsEl = document.getElementById('stats');

    // Sort state
    let sortKey = 'name';
    let sortDesc = true;

    function render() {
      const hostF = hostSel.value;
      const nameF = document.getElementById('nameFilter').value.toLowerCase();

      let vms = Object.keys(byVM);
      if (hostF) vms = vms.filter(v => vmHost[v] === hostF);
      if (nameF) vms = vms.filter(v => v.toLowerCase().includes(nameF));

      if (sortKey === 'name') {
        vms.sort();
        if (sortDesc) vms.reverse();
      } else if (sortKey === 'host') {
        vms.sort((a, b) => {
          const cmp = vmHost[a].localeCompare(vmHost[b]);
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
        th.textContent = col.title;
        th.style.cursor = 'pointer';
        th.style.userSelect = 'none';
        if (sortKey === col.key) {
          th.textContent += sortDesc ? ' ▼' : ' ▲';
        }
        th.addEventListener('click', () => {
          if (sortKey === col.key) {
            sortDesc = !sortDesc;
          } else {
            sortKey = col.key;
            // Default descending for metrics, ascending for name/host
            sortDesc = col.key !== 'name' && col.key !== 'host';
          }
          render();
        });
        hr.appendChild(th);
      }
      thead.appendChild(hr);
      table.appendChild(thead);

      const tbody = document.createElement('tbody');
      const fragment = document.createDocumentFragment();
      const pendingDraw = [];

      for (const vm of vms) {
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
          const parts = [];
          for (const line of chartDef.lines) {
            if (line.dash) continue;
            const v = lastNonNull(rows, line.key);
            parts.push(fmtVal(v, chartDef.title));
          }
          labelDiv.innerHTML = parts.join('<br>');
          wrap.appendChild(labelDiv);

          td.appendChild(wrap);
          tr.appendChild(td);
          pendingDraw.push({canvas, rows, chartDef});
        }

        fragment.appendChild(tr);
      }

      tbody.appendChild(fragment);
      table.appendChild(tbody);
      vis.appendChild(table);

      const observer = new IntersectionObserver((entries) => {
        for (const entry of entries) {
          if (entry.isIntersecting) {
            const canvas = entry.target;
            const data = canvas._sparkData;
            if (data && !canvas._drawn) {
              drawSparkline(canvas, data.rows, data.chartDef);
              canvas._drawn = true;
            }
            observer.unobserve(canvas);
          }
        }
      }, {rootMargin: '200px'});

      for (const item of pendingDraw) {
        item.canvas._sparkData = item;
        observer.observe(item.canvas);
      }
    }

    loadingEl.hidden = true;
    hostSel.addEventListener('change', render);
    document.getElementById('nameFilter').addEventListener('input', render);
    render();
  } catch (e) {
    loadingEl.hidden = true;
    errorEl.textContent = 'Error: ' + e.message;
  }
})();
