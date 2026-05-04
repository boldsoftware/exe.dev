(function () {
    "use strict";

    // Compute for People — size selector
    var vmSizes = [
        { cpu: 2,  ram: 8 },
        { cpu: 4,  ram: 16 },
        { cpu: 8,  ram: 32 },
        { cpu: 16, ram: 64 }
    ];
    var personalPrices = [20, 40, 80, 160];
    var teamPrices = [25, 50, 100, 200];

    var vmSlider = document.getElementById("vm-size");
    var vmLabel = document.getElementById("vm-size-label");
    var pricePersonal = document.getElementById("price-personal");
    var priceTeam = document.getElementById("price-team");

    function updatePeople() {
        var idx = parseInt(vmSlider.value, 10);
        var s = vmSizes[idx];
        vmLabel.textContent = s.cpu + " vCPU / " + s.ram + " GB RAM";
        pricePersonal.innerHTML = "$" + personalPrices[idx] + '<span class="price-period">/month</span>';
        priceTeam.innerHTML = "$" + teamPrices[idx] + '<span class="price-period">/user/month</span>';
    }

    vmSlider.addEventListener("input", updatePeople);
    updatePeople();

    // Platform — explicit size steps + custom top slot
    var platformCpus = [32, 48, 64, 96, 128, 192, 256, 384, 512, 640];
    var cpuHourRate = 0.07;

    var platformSlider = document.getElementById("platform-size");
    var platformLabel = document.getElementById("platform-size-label");
    var pricePlatform = document.getElementById("price-platform");

    function updatePlatform() {
        var idx = parseInt(platformSlider.value, 10);
        if (idx >= platformCpus.length) {
            platformLabel.textContent = "768+ vCPU / custom";
            pricePlatform.innerHTML = '<a href="mailto:support@exe.dev">Contact Us</a>';
        } else {
            var cpu = platformCpus[idx];
            var ram = cpu * 4;
            platformLabel.textContent = cpu + " vCPU / " + ram + " GB RAM";
            var hourly = (cpu * cpuHourRate).toFixed(2);
            pricePlatform.innerHTML = "$" + hourly + '<span class="price-period">/hr</span>';
        }
    }

    platformSlider.addEventListener("input", updatePlatform);
    updatePlatform();
})();
