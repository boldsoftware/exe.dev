// Usage pricing calculator
(function () {
  var HOURS = 720; // 30 days
  var RATES = {
    disk: 0.08,           // per GiB per month
    cpu: 0.05,            // per core per hour
    activeMem: 0.016,     // per GiB per hour
    inactiveMem: 0.08,    // per GiB per month
  };

  var ids = [
    'disk', 'cpu-active', 'mem-active',
    'cpu-idle', 'mem-idle-active', 'mem-idle-inactive',
    'idle-pct',
  ];

  function val(id) {
    return parseFloat(document.getElementById(id).value) || 0;
  }

  function fmt(n) {
    return '$' + n.toFixed(2);
  }

  function set(id, v) {
    document.getElementById(id).textContent = fmt(v);
  }

  function recalc() {
    var idlePct = Math.min(Math.max(val('idle-pct'), 0), 100);
    var activePct = 100 - idlePct;
    var activeHours = HOURS * (activePct / 100);
    var idleHours = HOURS * (idlePct / 100);

    document.getElementById('idle-pct-display').textContent = idlePct + '%';
    document.getElementById('calc-head-idle').textContent = 'Idle (' + idlePct + '%)';
    document.getElementById('calc-head-active').textContent = 'Active (' + activePct + '%)';
    document.getElementById('calc-hours-idle').textContent = idleHours.toFixed(0) + ' hr';
    document.getElementById('calc-hours-active').textContent = activeHours.toFixed(0) + ' hr';

    var disk = val('disk');
    var cpuActive = val('cpu-active');
    var memActive = val('mem-active');
    var cpuIdle = val('cpu-idle');
    var memIdleActive = val('mem-idle-active');
    var memIdleInactive = val('mem-idle-inactive');

    // Disk: monthly, not split by idle/active.
    var diskTotal = disk * RATES.disk;

    // CPU split.
    var cpuIdleCost = cpuIdle * idleHours * RATES.cpu;
    var cpuActiveCost = cpuActive * activeHours * RATES.cpu;
    var cpuTotal = cpuIdleCost + cpuActiveCost;

    // Active memory split.
    var amemIdleCost = memIdleActive * idleHours * RATES.activeMem;
    var amemActiveCost = memActive * activeHours * RATES.activeMem;
    var amemTotal = amemIdleCost + amemActiveCost;

    // Inactive memory: idle column only, billed monthly proportional to idle time.
    var imemIdleCost = memIdleInactive * RATES.inactiveMem * (idlePct / 100);
    var imemTotal = imemIdleCost;

    var colIdle = cpuIdleCost + amemIdleCost + imemIdleCost;
    var colActive = cpuActiveCost + amemActiveCost;
    var total = diskTotal + colIdle + colActive;

    set('calc-disk-total', diskTotal);
    set('calc-cpu-idle', cpuIdleCost);
    set('calc-cpu-active', cpuActiveCost);
    set('calc-cpu-total', cpuTotal);
    set('calc-amem-idle', amemIdleCost);
    set('calc-amem-active', amemActiveCost);
    set('calc-amem-total', amemTotal);
    set('calc-imem-idle', imemIdleCost);
    set('calc-imem-total', imemTotal);
    set('calc-col-idle', colIdle);
    set('calc-col-active', colActive);
    set('calc-total', total);
  }

  for (var i = 0; i < ids.length; i++) {
    document.getElementById(ids[i]).addEventListener('input', recalc);
  }

  recalc();
})();
