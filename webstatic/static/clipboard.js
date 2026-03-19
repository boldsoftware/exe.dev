// clipboard.js — shared copy-to-clipboard utility.
// Usage: <button onclick="copyToClipboard(this, 'text to copy')">
// The button should contain an SVG icon; it will be swapped to a checkmark on success.

const clipboardCheckSVG = '<svg viewBox="0 0 24 24" fill="none" stroke="#16a34a" stroke-width="2" style="width: 12px; height: 12px;"><polyline points="20 6 9 17 4 12"></polyline></svg>';

function copyToClipboard(btn, text) {
    navigator.clipboard.writeText(text).then(function() {
        var orig = btn.innerHTML;
        btn.innerHTML = clipboardCheckSVG;
        setTimeout(function() { btn.innerHTML = orig; }, 1500);
    });
}
