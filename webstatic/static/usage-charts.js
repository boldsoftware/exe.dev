// usage-charts.js — Vega-Lite based VM usage charts
// Shared between debug pages and the user-facing /usage page.
//
// Usage:
//   renderUsageCharts(containerEl, apiURL, vmNames, {hours: 24, stacked: false})

(function() {
  'use strict';

  const VEGALITE_CDN = 'https://cdn.jsdelivr.net/npm/vega-lite@5';
  const VEGA_CDN = 'https://cdn.jsdelivr.net/npm/vega@5';
  const VEGAEMBED_CDN = 'https://cdn.jsdelivr.net/npm/vega-embed@6';

  let scriptsLoaded = false;
  let loadPromise = null;

  function loadScripts() {
    if (loadPromise) return loadPromise;
    loadPromise = new Promise(function(resolve, reject) {
      if (window.vegaEmbed) { scriptsLoaded = true; resolve(); return; }
      var s1 = document.createElement('script');
      s1.src = VEGA_CDN;
      s1.onload = function() {
        var s2 = document.createElement('script');
        s2.src = VEGALITE_CDN;
        s2.onload = function() {
          var s3 = document.createElement('script');
          s3.src = VEGAEMBED_CDN;
          s3.onload = function() { scriptsLoaded = true; resolve(); };
          s3.onerror = reject;
          document.head.appendChild(s3);
        };
        s2.onerror = reject;
        document.head.appendChild(s2);
      };
      s1.onerror = reject;
      document.head.appendChild(s1);
    });
    return loadPromise;
  }

  function fmtBytes(v) {
    if (v >= 1000) return (v/1000).toFixed(1) + ' TB';
    if (v >= 1) return v.toFixed(1) + ' GB';
    var mb = v * 1000;
    if (mb >= 1) return mb.toFixed(0) + ' MB';
    return (mb*1000).toFixed(0) + ' KB';
  }

  function makeChartSpec(data, opts) {
    var multiVM = opts.vmNames && opts.vmNames.length > 1;
    var width = opts.width || 700;
    var height = opts.chartHeight || 180;

    var specs = [];

    // 1. Disk chart — nominal size only
    var diskSpec = {
      title: 'Disk',
      width: width,
      height: height,
      layer: []
    };
    if (multiVM && opts.stacked) {
      diskSpec.layer.push({
        mark: {type: 'area', opacity: 0.7},
        encoding: {
          x: {field: 'timestamp', type: 'temporal', title: 'Time'},
          y: {field: 'disk_size_gb', type: 'quantitative', title: 'Disk (GB)', stack: 'zero'},
          color: {field: 'vm_name', type: 'nominal', title: 'VM'}
        }
      });
    } else {
      diskSpec.layer.push({
        mark: {type: 'line', strokeWidth: 2},
        encoding: {
          x: {field: 'timestamp', type: 'temporal', title: 'Time'},
          y: {field: 'disk_size_gb', type: 'quantitative', title: 'Disk (GB)'},
          color: multiVM ? {field: 'vm_name', type: 'nominal', title: 'VM'} : {value: '#1f77b4'}
        }
      });
    }
    specs.push(diskSpec);

    // 2. CPU chart — cores used only
    var cpuSpec = {
      title: 'CPU (cores)',
      width: width,
      height: height,
      layer: []
    };
    if (multiVM && opts.stacked) {
      cpuSpec.layer.push({
        mark: {type: 'area', opacity: 0.7},
        encoding: {
          x: {field: 'timestamp', type: 'temporal', title: 'Time'},
          y: {field: 'cpu_cores', type: 'quantitative', title: 'Cores', stack: 'zero'},
          color: {field: 'vm_name', type: 'nominal', title: 'VM'}
        }
      });
    } else {
      cpuSpec.layer.push({
        mark: {type: 'line', strokeWidth: 2},
        encoding: {
          x: {field: 'timestamp', type: 'temporal', title: 'Time'},
          y: {field: 'cpu_cores', type: 'quantitative', title: 'Cores'},
          color: multiVM ? {field: 'vm_name', type: 'nominal', title: 'VM'} : {value: '#ff7f0e'}
        }
      });
    }
    specs.push(cpuSpec);

    // 3. Network chart — TX and RX rates
    var netSpec = {
      title: 'Network',
      width: width,
      height: height,
      layer: []
    };
    if (multiVM && opts.stacked) {
      // Transform data to long format for network
      netSpec.transform = [
        {fold: ['network_tx_mbps', 'network_rx_mbps'], as: ['direction', 'mbps']}
      ];
      netSpec.layer.push({
        mark: {type: 'area', opacity: 0.6},
        encoding: {
          x: {field: 'timestamp', type: 'temporal', title: 'Time'},
          y: {field: 'mbps', type: 'quantitative', title: 'Mbps', stack: 'zero'},
          color: {field: 'vm_name', type: 'nominal', title: 'VM'},
          strokeDash: {field: 'direction', type: 'nominal'}
        }
      });
    } else {
      netSpec.layer.push({
        mark: {type: 'line', strokeWidth: 2},
        encoding: {
          x: {field: 'timestamp', type: 'temporal', title: 'Time'},
          y: {field: 'network_tx_mbps', type: 'quantitative', title: 'Mbps'},
          color: multiVM ? {field: 'vm_name', type: 'nominal', title: 'VM'} : {value: '#1f77b4'}
        }
      });
      netSpec.layer.push({
        mark: {type: 'line', strokeWidth: 2, strokeDash: [4, 2]},
        encoding: {
          x: {field: 'timestamp', type: 'temporal'},
          y: {field: 'network_rx_mbps', type: 'quantitative'},
          color: multiVM ? {field: 'vm_name', type: 'nominal'} : {value: '#ff7f0e'}
        }
      });
    }
    specs.push(netSpec);

    return specs;
  }

  // Main entry point
  window.renderUsageCharts = async function(container, apiURL, vmNames, opts) {
    opts = opts || {};
    opts.vmNames = vmNames;
    var hours = opts.hours || 24;

    container.innerHTML = '<div class="usage-loading">Loading metrics...</div>';

    try {
      // Fetch data
      var url = apiURL + '?vm_names=' + encodeURIComponent(vmNames.join(',')) + '&hours=' + hours;
      var resp = await fetch(url);
      if (!resp.ok) {
        var errText = await resp.text();
        container.innerHTML = '<div class="usage-error">Failed to load metrics: ' + errText + '</div>';
        return;
      }
      var data = await resp.json();

      // Flatten all VM data into a single array
      var allData = [];
      var anyData = false;
      for (var vmName in data) {
        if (data[vmName] && data[vmName].length > 0) {
          anyData = true;
          for (var i = 0; i < data[vmName].length; i++) {
            allData.push(data[vmName][i]);
          }
        }
      }

      if (!anyData) {
        container.innerHTML = '<div class="usage-empty">No metrics data available for the selected period.</div>';
        return;
      }

      // Load Vega-Lite
      await loadScripts();

      // Build chart specs
      var specs = makeChartSpec(allData, opts);

      // Render
      container.innerHTML = '';

      // Controls
      var controls = document.createElement('div');
      controls.className = 'usage-controls';
      controls.innerHTML = '<span class="usage-range-label">Range:</span> ';
      var ranges = [{label: '1d', hours: 24}, {label: '1w', hours: 168}, {label: '1m', hours: 744}];
      ranges.forEach(function(range) {
        var btn = document.createElement('button');
        btn.textContent = range.label;
        btn.className = 'usage-range-btn' + (range.hours === hours ? ' active' : '');
        btn.addEventListener('click', function() {
          opts.hours = range.hours;
          window.renderUsageCharts(container, apiURL, vmNames, opts);
        });
        controls.appendChild(btn);
      });

      if (vmNames.length > 1) {
        var stackBtn = document.createElement('button');
        stackBtn.textContent = opts.stacked ? 'Unstacked' : 'Stacked';
        stackBtn.className = 'usage-range-btn';
        stackBtn.style.marginLeft = '12px';
        stackBtn.addEventListener('click', function() {
          opts.stacked = !opts.stacked;
          window.renderUsageCharts(container, apiURL, vmNames, opts);
        });
        controls.appendChild(stackBtn);
      }

      container.appendChild(controls);

      // Network legend (for single VM non-stacked)
      if (vmNames.length === 1 && !opts.stacked) {
        var legend = document.createElement('div');
        legend.className = 'usage-legend';
        legend.innerHTML = '<span>Network: <span style="color:#1f77b4">— TX</span> / <span style="color:#ff7f0e">- - RX</span></span>';
        container.appendChild(legend);
      }

      // Render each chart
      for (var j = 0; j < specs.length; j++) {
        var chartDiv = document.createElement('div');
        chartDiv.className = 'usage-chart';
        container.appendChild(chartDiv);

        var vlSpec = {
          $schema: 'https://vega.github.io/schema/vega-lite/v5.json',
          data: {values: allData},
          ...specs[j],
          config: {
            view: {stroke: null},
            axis: {grid: true, gridOpacity: 0.15, labelFontSize: 10, titleFontSize: 11},
            legend: {labelFontSize: 10, titleFontSize: 11}
          }
        };

        await vegaEmbed(chartDiv, vlSpec, {actions: false, renderer: 'svg'});
      }

    } catch (err) {
      container.innerHTML = '<div class="usage-error">Error loading metrics: ' + err.message + '</div>';
    }
  };
})();
