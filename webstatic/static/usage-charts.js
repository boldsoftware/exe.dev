// usage-charts.js — Vega-Lite based VM usage charts
// Shared between debug pages and the user-facing /usage page.
//
// Usage:
//   renderUsageCharts(containerEl, apiURL, vmNames, {hours: 24, debug: false})

(function() {
  'use strict';

  const VEGALITE_CDN = 'https://cdn.jsdelivr.net/npm/vega-lite@5';
  const VEGA_CDN = 'https://cdn.jsdelivr.net/npm/vega@5';
  const VEGAEMBED_CDN = 'https://cdn.jsdelivr.net/npm/vega-embed@6';

  let loadPromise = null;

  function loadScripts() {
    if (loadPromise) return loadPromise;
    loadPromise = new Promise(function(resolve, reject) {
      if (window.vegaEmbed) { resolve(); return; }
      var s1 = document.createElement('script');
      s1.src = VEGA_CDN;
      s1.onload = function() {
        var s2 = document.createElement('script');
        s2.src = VEGALITE_CDN;
        s2.onload = function() {
          var s3 = document.createElement('script');
          s3.src = VEGAEMBED_CDN;
          s3.onload = function() { resolve(); };
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

  // Compute the max nominal value from the data for a given field
  function maxNominal(allData, field) {
    var max = 0;
    for (var i = 0; i < allData.length; i++) {
      if (allData[i][field] > max) max = allData[i][field];
    }
    return max;
  }

  // Sort VM names by total metric value (descending) for legend ordering
  function sortedVMNames(allData, metricField) {
    var totals = {};
    for (var i = 0; i < allData.length; i++) {
      var d = allData[i];
      var name = d.vm_name;
      if (!totals[name]) totals[name] = 0;
      totals[name] += (d[metricField] || 0);
    }
    var names = Object.keys(totals);
    names.sort(function(a, b) { return totals[b] - totals[a]; });
    return names;
  }

  // Shared config for all charts
  var chartConfig = {
    view: {stroke: null},
    axis: {grid: true, gridOpacity: 0.15, labelFontSize: 10, titleFontSize: 11},
    legend: {labelFontSize: 10, titleFontSize: 11, symbolStrokeWidth: 2}
  };

  // Chart counter for unique param names
  var chartCounter = 0;

  function nextParamName() {
    chartCounter++;
    return 'ls_' + chartCounter;
  }

  // Build a single-layer line chart spec (with optional reference rule)
  // params go on the first layer to avoid duplicate signal issues in multi-layer specs
  function lineChart(title, width, height, multiVM, field, yTitle, sortOrder, tooltipExtra, yScale, refLine) {
    var paramName = nextParamName();
    var mainLayer = {
      mark: {type: 'line', strokeWidth: 2, clip: true},
      encoding: {
        x: {field: 'timestamp', type: 'temporal', title: 'Time'},
        y: {field: field, type: 'quantitative', title: yTitle, scale: yScale || {}},
        color: multiVM
          ? {field: 'vm_name', type: 'nominal', title: 'VM', sort: sortOrder, legend: {orient: 'right'}}
          : {value: '#1f77b4'},
        tooltip: [
          {field: 'vm_name', title: 'VM'},
          {field: 'timestamp', title: 'Time', type: 'temporal', format: '%Y-%m-%d %H:%M'}
        ].concat(tooltipExtra)
      }
    };
    if (multiVM) {
      mainLayer.params = [{
        name: paramName,
        select: {type: 'point', fields: ['vm_name']},
        bind: 'legend'
      }];
      mainLayer.encoding.opacity = {
        condition: {param: paramName, value: 1},
        value: 0.1
      };
    }

    var layers = [mainLayer];
    if (refLine != null) {
      layers.push({
        mark: {type: 'rule', strokeDash: [4, 4], strokeWidth: 1, color: '#999'},
        encoding: {y: {datum: refLine}}
      });
    }

    return {title: title, width: width, height: height, layer: layers};
  }

  function makeChartSpecs(allData, opts) {
    var multiVM = opts.vmNames && opts.vmNames.length > 1;
    var width = opts.width || 700;
    var height = opts.chartHeight || 180;
    var debug = opts.debug || false;

    var specs = [];

    // Get max nominal values for scaling
    var maxCPUNom = maxNominal(allData, 'cpu_nominal');
    var maxMemNom = maxNominal(allData, 'memory_nominal_gb');

    // Legend sort orders
    var cpuSort = sortedVMNames(allData, 'cpu_cores');
    var memRSSSort = sortedVMNames(allData, 'memory_rss_gb');
    var memSwapSort = sortedVMNames(allData, 'memory_swap_gb');
    var netSort = sortedVMNames(allData, 'network_rx_mbps');
    var diskSort = sortedVMNames(allData, 'disk_size_gb');

    // === CPU Chart ===
    specs.push(lineChart(
      'CPU (cores)', width, height, multiVM,
      'cpu_cores', 'Cores', cpuSort,
      [{field: 'cpu_cores', title: 'CPU Cores', format: '.2f'},
       {field: 'cpu_nominal', title: 'Nominal Cores', format: '.0f'}],
      maxCPUNom > 0 ? {domain: [0, maxCPUNom]} : {},
      maxCPUNom > 0 ? maxCPUNom : null
    ));

    // === Memory RSS Chart ===
    specs.push(lineChart(
      'Memory RSS', width, height, multiVM,
      'memory_rss_gb', 'GB', memRSSSort,
      [{field: 'memory_rss_gb', title: 'RSS', format: '.2f'},
       {field: 'memory_nominal_gb', title: 'Nominal', format: '.1f'}],
      maxMemNom > 0 ? {domain: [0, maxMemNom]} : {},
      maxMemNom > 0 ? maxMemNom : null
    ));

    // === Memory Swap Chart ===
    specs.push(lineChart(
      'Memory Swap', width, height, multiVM,
      'memory_swap_gb', 'GB', memSwapSort,
      [{field: 'memory_swap_gb', title: 'Swap', format: '.2f'}],
      {}, null
    ));

    // === Memory Nominal Chart ===
    specs.push(lineChart(
      'Memory Nominal', width, height, multiVM,
      'memory_nominal_gb', 'GB', memRSSSort,
      [{field: 'memory_nominal_gb', title: 'Nominal GB', format: '.1f'}],
      {}, null
    ));

    // === Network Chart (mirrored: RX above, TX below) ===
    var netParamName = nextParamName();
    var netAreaLayer = {
      mark: {type: 'area', opacity: 0.3, clip: true},
      encoding: {
        x: {field: 'timestamp', type: 'temporal', title: 'Time'},
        y: {field: 'signed_rate', type: 'quantitative', title: 'Mbps'},
        color: multiVM
          ? {field: 'vm_name', type: 'nominal', title: 'VM', sort: netSort, legend: {orient: 'right'}}
          : {field: 'dir_label', type: 'nominal', title: 'Direction',
             scale: {domain: ['RX \u2191', 'TX \u2193'], range: ['#2ca02c', '#d62728']}},
        tooltip: [
          {field: 'vm_name', title: 'VM'},
          {field: 'timestamp', title: 'Time', type: 'temporal', format: '%Y-%m-%d %H:%M'},
          {field: 'dir_label', title: 'Direction'},
          {field: 'rate', title: 'Mbps', format: '.2f'}
        ]
      }
    };
    if (multiVM) {
      netAreaLayer.params = [{
        name: netParamName,
        select: {type: 'point', fields: ['vm_name']},
        bind: 'legend'
      }];
      netAreaLayer.encoding.opacity = {
        condition: {param: netParamName, value: 0.3},
        value: 0.02
      };
    }
    var netLineLayer = {
      mark: {type: 'line', strokeWidth: 1.5, clip: true},
      encoding: {
        x: {field: 'timestamp', type: 'temporal'},
        y: {field: 'signed_rate', type: 'quantitative'},
        color: multiVM
          ? {field: 'vm_name', type: 'nominal', sort: netSort}
          : {field: 'dir_label', type: 'nominal',
             scale: {domain: ['RX \u2191', 'TX \u2193'], range: ['#2ca02c', '#d62728']}},
        strokeDash: {field: 'dir_label', type: 'nominal',
                     scale: {domain: ['RX \u2191', 'TX \u2193'], range: [[], [4, 2]]},
                     legend: null}
      }
    };
    if (multiVM) {
      netLineLayer.encoding.opacity = {
        condition: {param: netParamName, value: 1},
        value: 0.1
      };
    }

    specs.push({
      title: 'Network (RX \u2191 / TX \u2193)',
      width: width,
      height: height,
      transform: [
        {fold: ['network_rx_mbps', 'network_tx_mbps'], as: ['direction', 'rate']},
        {calculate: "datum.direction === 'network_tx_mbps' ? -datum.rate : datum.rate", as: 'signed_rate'},
        {calculate: "datum.direction === 'network_rx_mbps' ? 'RX \u2191' : 'TX \u2193'", as: 'dir_label'}
      ],
      layer: [
        netAreaLayer,
        netLineLayer,
        {mark: {type: 'rule', strokeWidth: 0.5, color: '#ccc'}, encoding: {y: {datum: 0}}}
      ]
    });

    // === Disk Charts (debug only) ===
    if (debug) {
      specs.push(lineChart(
        'Disk Size (Provisioned)', width, height, multiVM,
        'disk_size_gb', 'GB', diskSort,
        [{field: 'disk_size_gb', title: 'Provisioned GB', format: '.1f'}],
        {}, null
      ));

      specs.push(lineChart(
        'Disk Used (Compressed)', width, height, multiVM,
        'disk_used_gb', 'GB', diskSort,
        [{field: 'disk_used_gb', title: 'Used (Compressed) GB', format: '.2f'}],
        {}, null
      ));

      specs.push(lineChart(
        'Disk Logical Used (Uncompressed)', width, height, multiVM,
        'disk_logical_used_gb', 'GB', diskSort,
        [{field: 'disk_logical_used_gb', title: 'Logical Used GB', format: '.2f'}],
        {}, null
      ));
    }

    return specs;
  }

  // Main entry point
  window.renderUsageCharts = async function(container, apiURL, vmNames, opts) {
    opts = opts || {};
    opts.vmNames = vmNames;
    var hours = opts.hours || 24;

    container.innerHTML = '<div class="usage-loading">Loading metrics...</div>';

    try {
      var url = apiURL + '?vm_names=' + encodeURIComponent(vmNames.join(',')) + '&hours=' + hours;
      var resp = await fetch(url);
      if (!resp.ok) {
        var errText = await resp.text();
        container.innerHTML = '<div class="usage-error">Failed to load metrics: ' + errText + '</div>';
        return;
      }
      var data = await resp.json();

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

      await loadScripts();

      var specs = makeChartSpecs(allData, opts);

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

      container.appendChild(controls);

      // Render each chart
      for (var j = 0; j < specs.length; j++) {
        var chartDiv = document.createElement('div');
        chartDiv.className = 'usage-chart';
        container.appendChild(chartDiv);

        var vlSpec = {
          $schema: 'https://vega.github.io/schema/vega-lite/v5.json',
          data: {values: allData},
          ...specs[j],
          config: chartConfig
        };

        await vegaEmbed(chartDiv, vlSpec, {actions: false, renderer: 'svg'});
      }

    } catch (err) {
      container.innerHTML = '<div class="usage-error">Error loading metrics: ' + err.message + '</div>';
    }
  };
})();
