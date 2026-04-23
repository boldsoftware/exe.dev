(function () {
    "use strict";
    var footer = document.createElement("footer");
    footer.innerHTML =
        '<a href="/about">About</a>' +
        '<span>&bull;</span>' +
        '<a href="https://blog.exe.dev">Blog</a>' +
        '<span>&bull;</span>' +
        '<a href="https://discord.gg/jc9WQUfaxf">Discord</a>' +
        '<span>&bull;</span>' +
        '<a href="/docs/privacy-notice">Privacy</a>' +
        '<span>&bull;</span>' +
        '<a href="/security">Security</a>';
    document.body.appendChild(footer);
})();
