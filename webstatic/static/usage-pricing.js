// Usage pricing calculator
(function () {
  var HOURS = 720; // 30 days
  var RATES = {
    disk: 0.08,           // per GiB per month
    cpu: 0.05,            // per core per hour
    activeMem: 0.016,     // per GiB per hour
    inactiveMem: 0.08,    // per GiB per month
    vmMin: 0.001,         // per hour
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

  function recalc() {
    var idlePct = Math.min(Math.max(val('idle-pct'), 0), 100);
    var activeHours = HOURS * (1 - idlePct / 100);
    var idleHours = HOURS * (idlePct / 100);

    document.getElementById('idle-pct-display').textContent = idlePct + '%';

    var disk = val('disk');
    var cpuActive = val('cpu-active');
    var memActive = val('mem-active');
    var cpuIdle = val('cpu-idle');
    var memIdleActive = val('mem-idle-active') / 1024; // MiB -> GiB
    var memIdleInactive = val('mem-idle-inactive') / 1024; // MiB -> GiB

    var diskCost = disk * RATES.disk;
    var cpuCost = (cpuActive * activeHours + cpuIdle * idleHours) * RATES.cpu;
    var activeMemCost = (memActive * activeHours + memIdleActive * idleHours) * RATES.activeMem;
    var inactiveMemCost = memIdleInactive * RATES.inactiveMem * (idlePct / 100);
    var vmCost = RATES.vmMin * HOURS;
    var total = diskCost + cpuCost + activeMemCost + inactiveMemCost + vmCost;

    document.getElementById('calc-disk').textContent = fmt(diskCost);
    document.getElementById('calc-cpu').textContent = fmt(cpuCost);
    document.getElementById('calc-active-mem').textContent = fmt(activeMemCost);
    document.getElementById('calc-inactive-mem').textContent = fmt(inactiveMemCost);
    document.getElementById('calc-vm').textContent = fmt(vmCost);
    document.getElementById('calc-total').textContent = fmt(total);
  }

  for (var i = 0; i < ids.length; i++) {
    document.getElementById(ids[i]).addEventListener('input', recalc);
  }

  recalc();
})();
