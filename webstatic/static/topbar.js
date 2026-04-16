(function () {
    "use strict";
    var nav = document.createElement("nav");
    nav.className = "topbar";
    nav.innerHTML =
        '<div class="topbar-left">' +
            '<a href="/" class="topbar-logo"><img src="/static/exy.png" alt="exe.dev" width="24" height="24" /></a>' +
            '<a href="/" class="topbar-logo-text">exe.dev</a>' +
            '<a href="/docs" class="topbar-link">docs</a>' +
            '<a href="https://blog.exe.dev" class="topbar-link">blog</a>' +
            '<a href="/pricing" class="topbar-link">pricing</a>' +
        '</div>' +
        '<div class="topbar-right">' +
            '<a href="/auth" class="topbar-login"><span class="topbar-login-full">Login / Register</span><span class="topbar-login-short">Login</span></a>' +
        '</div>';
    document.body.insertBefore(nav, document.body.firstChild);
})();
