(function () {
    "use strict";
    var nav = document.createElement("nav");
    nav.className = "topbar";
    nav.innerHTML =
        '<div class="topbar-logo"></div>' +
        '<div class="topbar-right">' +
            '<a href="/docs">docs</a>' +
            '<a href="/pricing">pricing</a>' +
            '<a href="/auth" class="topbar-login">Login / Register</a>' +
        '</div>';
    document.body.insertBefore(nav, document.body.firstChild);
})();
