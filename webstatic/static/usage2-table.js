// usage2-table.js — Table view of VM usage metrics with sparklines and color coding
// Usage:
//   renderUsageTable(containerEl, apiURL, vmNames, vmStatuses, {hours: 24})

(function() {
  'use strict';

  function fmtGB(v) {
    if (v == null || isNaN(v)) return '—';
    if (v >= 1000) return (v / 1000).toFixed(1) + ' TB';
    if (v >= 1) return v.toFixed(2) + ' GB';
    var mb = v * 1000;
    if (mb >= 1) return mb.toFixed(0) + ' MB';
    return (mb * 1000).toFixed(0) + ' KB';
  }

  function fmtCores(v) {
    if (v == null || isNaN(v)) return '—';
    return v.toFixed(2);
  }

  function fmtMbps(v) {
    if (v == null || isNaN(v)) return '—';
    if (v < 0.01) return '0';
    return v.toFixed(2);
  }

  function fmtPct(v) {
    if (v == null || isNaN(v)) return '—';
    return (v * 100).toFixed(1) + '%';
  }

  function fmtMBps(v) {
    if (v == null || isNaN(v)) return '—';
    if (v < 0.01) return '0';
    return v.toFixed(2);
  }

  // Color scale: ratio 0..1 -> background color
  // Blue (cool) at low, orange/red at high
  function ratioColor(ratio) {
    if (ratio <= 0 || isNaN(ratio)) return '';
    var t = Math.min(ratio, 1);
    // Light green to yellow to orange to red
    if (t < 0.5) {
      // green to yellow
      var r = Math.round(220 + t * 2 * 35);
      var g = Math.round(245 - t * 2 * 30);
      var b = Math.round(220 - t * 2 * 80);
      return 'rgb(' + r + ',' + g + ',' + b + ')';
    } else {
      // yellow to red
      var t2 = (t - 0.5) * 2;
      var r = Math.round(255);
      var g = Math.round(215 - t2 * 145);
      var b = Math.round(140 - t2 * 70);
      return 'rgb(' + r + ',' + g + ',' + b + ')';
    }
  }

  // Mini sparkline SVG (inline, ~60px wide)
  function makeSpark(values, color, scaleMax) {
    if (!values || values.length < 2) return '';
    var w = 60, h = 16;
    var max = (scaleMax != null && scaleMax > 0) ? scaleMax : Math.max.apply(null, values);
    if (max <= 0) max = 1;
    var points = values.map(function(v, i) {
      var x = (i / (values.length - 1)) * w;
      var y = h - (Math.max(v, 0) / max) * (h - 2) - 1;
      return x.toFixed(1) + ',' + y.toFixed(1);
    }).join(' ');
    return '<svg class="spark-inline" width="' + w + '" height="' + h +
           '" viewBox="0 0 ' + w + ' ' + h + '"><polyline points="' + points +
           '" style="stroke:' + color + '"/></svg>';
  }

  // Column definitions
  var columns = [
    {
      group: 'CPU',
      cols: [
        {key: 'cpu_cores', label: 'Cores', fmt: fmtCores, color: '#ff7f0e', nominal: 'cpu_nominal',
         tooltip: 'CPU cores used (from cumulative seconds)'},
        {key: 'cpu_pct', label: '% Nom', fmt: fmtPct, color: '#ff7f0e', derived: true,
         tooltip: 'CPU usage as % of nominal cores'}
      ]
    },
    {
      group: 'Memory',
      cols: [
        {key: 'memory_rss_gb', label: 'RSS', fmt: fmtGB, color: '#1f77b4', nominal: 'memory_nominal_gb',
         tooltip: 'Resident Set Size'},
        {key: 'memory_swap_gb', label: 'Swap', fmt: fmtGB, color: '#9467bd',
         tooltip: 'Swap usage'},
        {key: 'mem_pct', label: '% Nom', fmt: fmtPct, color: '#1f77b4', derived: true,
         tooltip: 'RSS as % of nominal memory'},
        {key: 'memory_nominal_gb', label: 'Nominal', fmt: fmtGB, color: '#aec7e8',
         tooltip: 'Nominal (provisioned) memory'}
      ]
    },
    {
      group: 'Network',
      cols: [
        {key: 'network_rx_mbps', label: 'RX Mbps', fmt: fmtMbps, color: '#2ca02c',
         tooltip: 'Network receive rate'},
        {key: 'network_tx_mbps', label: 'TX Mbps', fmt: fmtMbps, color: '#d62728',
         tooltip: 'Network transmit rate'}
      ]
    },
    {
      group: 'IO',
      cols: [
        {key: 'io_read_mbps', label: 'Rd MB/s', fmt: fmtMBps, color: '#17becf',
         tooltip: 'Disk read rate'},
        {key: 'io_write_mbps', label: 'Wr MB/s', fmt: fmtMBps, color: '#e377c2',
         tooltip: 'Disk write rate'}
      ]
    }
  ];

  // Process data: compute per-VM summaries + sparkline arrays
  function processData(rawData, vmNames, vmStatuses) {
    var vms = [];

    for (var n = 0; n < vmNames.length; n++) {
      var name = vmNames[n];
      var points = rawData[name] || [];
      var vm = {
        name: name,
        status: vmStatuses[name] || 'unknown',
        latest: {},
        sparklines: {},
        maxNomCPU: 0,
        maxNomMem: 0
      };

      // Build sparkline arrays and find latest
      var allKeys = ['cpu_cores', 'memory_rss_gb', 'memory_swap_gb', 'memory_nominal_gb',
                     'network_rx_mbps', 'network_tx_mbps', 'io_read_mbps', 'io_write_mbps',
                     'cpu_nominal'];
      for (var k = 0; k < allKeys.length; k++) {
        vm.sparklines[allKeys[k]] = [];
      }
      vm.sparklines['cpu_pct'] = [];
      vm.sparklines['mem_pct'] = [];

      for (var i = 0; i < points.length; i++) {
        var p = points[i];
        for (var k = 0; k < allKeys.length; k++) {
          vm.sparklines[allKeys[k]].push(p[allKeys[k]] || 0);
        }
        // Derived: CPU %
        var cpuPct = (p.cpu_nominal > 0) ? (p.cpu_cores / p.cpu_nominal) : 0;
        vm.sparklines['cpu_pct'].push(cpuPct);
        // Derived: Mem %
        var memPct = (p.memory_nominal_gb > 0) ? (p.memory_rss_gb / p.memory_nominal_gb) : 0;
        vm.sparklines['mem_pct'].push(memPct);

        if (p.cpu_nominal > vm.maxNomCPU) vm.maxNomCPU = p.cpu_nominal;
        if (p.memory_nominal_gb > vm.maxNomMem) vm.maxNomMem = p.memory_nominal_gb;
      }

      // Latest values
      if (points.length > 0) {
        var last = points[points.length - 1];
        vm.latest = {
          cpu_cores: last.cpu_cores,
          cpu_nominal: last.cpu_nominal,
          cpu_pct: last.cpu_nominal > 0 ? last.cpu_cores / last.cpu_nominal : 0,
          memory_rss_gb: last.memory_rss_gb,
          memory_swap_gb: last.memory_swap_gb,
          memory_nominal_gb: last.memory_nominal_gb,
          mem_pct: last.memory_nominal_gb > 0 ? last.memory_rss_gb / last.memory_nominal_gb : 0,
          network_rx_mbps: last.network_rx_mbps,
          network_tx_mbps: last.network_tx_mbps,
          io_read_mbps: last.io_read_mbps,
          io_write_mbps: last.io_write_mbps
        };
      }

      vms.push(vm);
    }

    return vms;
  }

  // Downsample sparkline arrays to ~30 points
  function downsample(arr, targetLen) {
    if (!arr || arr.length <= targetLen) return arr;
    var result = [];
    var step = arr.length / targetLen;
    for (var i = 0; i < targetLen; i++) {
      var idx = Math.floor(i * step);
      result.push(arr[idx]);
    }
    return result;
  }

  function esc(s) {
    var d = document.createElement('div');
    d.textContent = s;
    return d.innerHTML;
  }

  function renderTable(container, vms, opts) {
    // Compute global max for color scaling per column
    var colMaxes = {};
    var colNomMaxes = {};
    var allCols = [];
    for (var g = 0; g < columns.length; g++) {
      for (var c = 0; c < columns[g].cols.length; c++) {
        allCols.push(columns[g].cols[c]);
      }
    }
    for (var ci = 0; ci < allCols.length; ci++) {
      var col = allCols[ci];
      var max = 0;
      for (var vi = 0; vi < vms.length; vi++) {
        var val = vms[vi].latest[col.key] || 0;
        if (val > max) max = val;
        // For nominal-relative columns, track the nominal
        if (col.nominal) {
          var nom = vms[vi].latest[col.nominal] || 0;
          if (nom > (colNomMaxes[col.key] || 0)) colNomMaxes[col.key] = nom;
        }
      }
      colMaxes[col.key] = max;
    }

    // Sort VMs by total CPU usage (descending)
    vms.sort(function(a, b) {
      return (b.latest.cpu_cores || 0) - (a.latest.cpu_cores || 0);
    });

    var html = '<table class="usage2-table">';

    // Group header row
    html += '<tr><th rowspan="2" style="min-width:140px">VM</th>';
    for (var g = 0; g < columns.length; g++) {
      html += '<th colspan="' + (columns[g].cols.length * 2) + '" class="group-header">' + columns[g].group + '</th>';
    }
    html += '</tr>';

    // Column header row (each col gets 2 sub-columns: spark + value)
    html += '<tr>';
    for (var g = 0; g < columns.length; g++) {
      for (var c = 0; c < columns[g].cols.length; c++) {
        var col = columns[g].cols[c];
        var border = (c === 0 && g > 0) ? ' style="border-left:2px solid #d0d7de"' : '';
        html += '<th title="' + esc(col.tooltip || '') + '"' + border + ' colspan="2">' + col.label + '</th>';
      }
    }
    html += '</tr>';

    // Data rows
    for (var vi = 0; vi < vms.length; vi++) {
      var vm = vms[vi];
      html += '<tr>';

      // VM name cell
      var statusCls = vm.status === 'running' ? 'running' : (vm.status === 'stopped' ? 'stopped' : 'starting');
      html += '<td><div class="vm-name-cell">' +
              '<span class="vm-status-dot ' + statusCls + '"></span>' +
              esc(vm.name) + '</div></td>';

      // Metric cells
      for (var g = 0; g < columns.length; g++) {
        for (var c = 0; c < columns[g].cols.length; c++) {
          var col = columns[g].cols[c];
          var val = vm.latest[col.key];
          var formatted = col.fmt(val);

          // Color coding
          var bgColor = '';
          if (col.key === 'cpu_pct' || col.key === 'mem_pct') {
            // Percentage columns: color by absolute value
            bgColor = ratioColor(val || 0);
          } else if (col.nominal) {
            // Nominal-relative: color by ratio to nominal
            var nom = vm.latest[col.nominal] || 0;
            if (nom > 0 && val > 0) {
              bgColor = ratioColor(val / nom);
            }
          } else if (colMaxes[col.key] > 0 && val > 0) {
            // Absolute: color relative to column max
            bgColor = ratioColor(val / colMaxes[col.key]);
          }

          var style = bgColor ? ' style="background:' + bgColor + '"' : '';
          var border = (c === 0 && g > 0) ? ' style="border-left:2px solid #eaeef2' + (bgColor ? ';background:' + bgColor : '') + '"' : style;

          // Sparkline
          var sparkData = downsample(vm.sparklines[col.key], 30);
          var scaleMax = null;
          if (col.nominal) {
            scaleMax = colNomMaxes[col.key] || null;
          }
          if (col.key === 'cpu_pct' || col.key === 'mem_pct') {
            scaleMax = 1; // 100%
          }
          var spark = makeSpark(sparkData, col.color, scaleMax);
          html += '<td' + border + '>' + spark + '</td>';
          html += '<td' + style + '>' + formatted + '</td>';
        }
      }

      html += '</tr>';
    }

    html += '</table>';
    return html;
  }

  // Main entry point
  window.renderUsageTable = async function(container, apiURL, vmNames, vmStatuses, opts) {
    opts = opts || {};
    var hours = opts.hours || 24;

    container.innerHTML = '<div class="usage2-loading">Loading metrics...</div>';

    try {
      var url = apiURL + '?vm_names=' + encodeURIComponent(vmNames.join(',')) + '&hours=' + hours;
      var resp = await fetch(url);
      if (!resp.ok) {
        var errText = await resp.text();
        container.innerHTML = '<div class="usage2-error">Failed to load metrics: ' + errText + '</div>';
        return;
      }
      var data = await resp.json();

      var anyData = false;
      for (var k in data) {
        if (data[k] && data[k].length > 0) { anyData = true; break; }
      }

      if (!anyData) {
        container.innerHTML = '<div class="usage2-empty">No metrics data available for the selected period.</div>';
        return;
      }

      var vms = processData(data, vmNames, vmStatuses);

      container.innerHTML = '';

      // Range controls
      var controls = document.createElement('div');
      controls.className = 'usage2-controls';
      controls.innerHTML = '<span class="usage2-range-label">Range:</span> ';
      var ranges = [{label: '1d', hours: 24}, {label: '1w', hours: 168}, {label: '1m', hours: 744}];
      ranges.forEach(function(range) {
        var btn = document.createElement('button');
        btn.textContent = range.label;
        btn.className = 'usage2-range-btn' + (range.hours === hours ? ' active' : '');
        btn.addEventListener('click', function() {
          opts.hours = range.hours;
          window.renderUsageTable(container, apiURL, vmNames, vmStatuses, opts);
        });
        controls.appendChild(btn);
      });
      container.appendChild(controls);

      // Table
      var tableDiv = document.createElement('div');
      tableDiv.style.overflowX = 'auto';
      tableDiv.innerHTML = renderTable(container, vms, opts);
      container.appendChild(tableDiv);

    } catch (err) {
      container.innerHTML = '<div class="usage2-error">Error loading metrics: ' + err.message + '</div>';
    }
  };
})();
