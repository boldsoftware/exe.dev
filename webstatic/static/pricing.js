(function () {
    "use strict";

    var individualPrices = [20, 40, 80, 160];
    var teamPrices = [25, 50, 100, 200];

    var select = document.getElementById("vm-size");
    var priceIndividual = document.getElementById("price-individual");
    var priceTeam = document.getElementById("price-team");

    function update() {
        var idx = parseInt(select.value, 10);
        priceIndividual.innerHTML = "$" + individualPrices[idx] + '<span class="price-period">/month</span>';
        priceTeam.innerHTML = "$" + teamPrices[idx] + '<span class="price-period">/month/user</span>';
    }

    select.addEventListener("change", update);
    update();
})();
