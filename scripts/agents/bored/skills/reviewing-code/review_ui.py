#!/usr/bin/env python3
"""Standalone web feedback UI for code reviews.

Usage: review_ui.py <review-dir>

Reads final-review.json from the review directory. Opens a browser with a form
for each review item. The user selects options and adds comments, then submits.
The plain text response is printed to stdout.
"""

import json
import os
import secrets
import signal
import sys
import webbrowser
from http.server import HTTPServer, BaseHTTPRequestHandler
from urllib.parse import parse_qs

HTML_TEMPLATE = """\
<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<title>Code Review</title>
<style>
  * { box-sizing: border-box; margin: 0; padding: 0; }
  body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Helvetica, Arial, sans-serif;
         max-width: 800px; margin: 0 auto; padding: 16px; color: #1a1a1a; background: #fafafa; }
  h1 { font-size: 1.3em; margin-bottom: 10px; }
  .item { background: #fff; border: 1px solid #ddd; border-radius: 5px; padding: 10px 12px;
          margin-bottom: 8px; }
  .item-number { font-weight: 600; font-size: 1em; margin-bottom: 4px; }
  .item-body { white-space: pre-wrap; font-family: "SF Mono", Monaco, "Cascadia Code",
               "Roboto Mono", Consolas, monospace; font-size: 0.82em; line-height: 1.4;
               background: #f6f6f6; padding: 8px; border-radius: 3px; margin-bottom: 6px;
               overflow-x: auto; }
  .options { margin-bottom: 4px; }
  .option-row { display: flex; align-items: baseline; gap: 5px; padding: 2px 0; }
  .option-row input[type="radio"] { margin-top: 2px; flex-shrink: 0; }
  .option-label { font-size: 0.85em; }
  .option-letter { font-weight: 600; }
  .custom-input { width: 100%; padding: 4px 6px; border: 1px solid #ccc; border-radius: 3px;
                  font-size: 0.85em; margin-top: 2px; }
  .footer { margin-top: 12px; }
  .footer textarea { width: 100%; height: 60px; padding: 6px; border: 1px solid #ccc;
                     border-radius: 4px; font-size: 0.85em; font-family: inherit; }
  .footer label { font-weight: 600; display: block; margin-bottom: 4px; }
  button[type="submit"] { margin-top: 10px; padding: 8px 20px; background: #0969da;
                          color: #fff; border: none; border-radius: 5px; font-size: 0.95em;
                          cursor: pointer; }
  button[type="submit"]:hover { background: #0550ae; }
</style>
</head>
<body>
<h1>Code Review Feedback</h1>
<form method="POST" action="/">
ITEMS_PLACEHOLDER
<div class="footer">
  <label for="overall">Overall comments</label>
  <textarea id="overall" name="overall" placeholder="Optional overall comments..."></textarea>
</div>
<button type="submit">Submit</button>
</form>
</body>
</html>
"""


def escape_html(s):
    return s.replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;").replace('"', "&quot;")


def build_html(items, csrf_token):
    parts = [f'<input type="hidden" name="csrf_token" value="{escape_html(csrf_token)}">']
    for item in items:
        num = item["number"]
        body = escape_html(item["body"])
        options_html = []
        # "no selection" default
        options_html.append(
            f'<div class="option-row">'
            f'<input type="radio" name="item_{num}" id="item_{num}_none" value="" checked>'
            f'<label class="option-label" for="item_{num}_none"><em>(no selection)</em></label>'
            f'</div>'
        )
        for opt in item["options"]:
            letter = escape_html(opt["letter"])
            text = escape_html(opt["text"])
            options_html.append(
                f'<div class="option-row">'
                f'<input type="radio" name="item_{num}" id="item_{num}_{letter}" value="{letter}">'
                f'<label class="option-label" for="item_{num}_{letter}">'
                f'<span class="option-letter">{num}{letter}.</span> {text}</label>'
                f'</div>'
            )
        parts.append(
            f'<div class="item">'
            f'<div class="item-number">Item {num}</div>'
            f'<div class="item-body">{body}</div>'
            f'<div class="options">{"".join(options_html)}</div>'
            f'<input type="text" class="custom-input" name="comment_{num}" '
            f'placeholder="Optional comment for item {num}...">'
            f'</div>'
        )
    return HTML_TEMPLATE.replace("ITEMS_PLACEHOLDER", "\n".join(parts))


def build_response(items, form_data):
    lines = []
    for item in items:
        num = item["number"]
        choice = form_data.get(f"item_{num}", [""])[0]
        comment = form_data.get(f"comment_{num}", [""])[0].strip()
        if choice and comment:
            lines.append(f"{num}{choice} {comment}")
        elif choice:
            lines.append(f"{num}{choice}")
        elif comment:
            lines.append(f"{num} {comment}")
        else:
            lines.append(f"{num} (left blank)")
    overall = form_data.get("overall", [""])[0].strip()
    text = "\n".join(lines)
    if overall:
        text += "\n\n" + overall
    return text


def make_handler(items, result_holder, csrf_token):
    html = build_html(items, csrf_token)
    html_bytes = html.encode()

    class Handler(BaseHTTPRequestHandler):
        def do_GET(self):
            self.send_response(200)
            self.send_header("Content-Type", "text/html; charset=utf-8")
            self.send_header("Content-Length", str(len(html_bytes)))
            self.end_headers()
            self.wfile.write(html_bytes)

        def do_POST(self):
            length = int(self.headers.get("Content-Length", 0))
            body = self.rfile.read(length).decode()
            form_data = parse_qs(body, keep_blank_values=True)
            token = form_data.get("csrf_token", [""])[0]
            if not secrets.compare_digest(token, csrf_token):
                self.send_response(403)
                self.end_headers()
                return
            result_holder["text"] = build_response(items, form_data)
            reply = b"<html><body><h2>Submitted.</h2><script>setTimeout(function(){window.close()},100)</script></body></html>"
            self.send_response(200)
            self.send_header("Content-Type", "text/html; charset=utf-8")
            self.send_header("Content-Length", str(len(reply)))
            self.end_headers()
            self.wfile.write(reply)

        def log_message(self, format, *a):
            pass  # suppress request logging

    return Handler


def main():
    if len(sys.argv) != 2:
        print("usage: review_ui.py <review-dir>", file=sys.stderr)
        sys.exit(1)

    review_dir = sys.argv[1]
    json_file = os.path.join(review_dir, "final-review.json")
    try:
        with open(json_file) as f:
            data = json.load(f)
    except (OSError, json.JSONDecodeError) as e:
        print(f"error: could not read JSON file: {e}", file=sys.stderr)
        sys.exit(1)

    items = data.get("items", [])
    if not items:
        print("error: no items in JSON", file=sys.stderr)
        sys.exit(1)

    result_holder = {}
    csrf_token = secrets.token_urlsafe(32)
    handler_class = make_handler(items, result_holder, csrf_token)
    server = HTTPServer(("127.0.0.1", 0), handler_class)
    port = server.server_address[1]

    def shutdown_handler(sig, frame):
        print("interrupted", file=sys.stderr)
        server.server_close()
        sys.exit(1)

    signal.signal(signal.SIGINT, shutdown_handler)
    signal.signal(signal.SIGTERM, shutdown_handler)

    url = f"http://127.0.0.1:{port}/"
    print(f"Opening browser at {url}", file=sys.stderr)
    webbrowser.open(url)

    # Serve until we get a POST submission.
    while "text" not in result_holder:
        server.handle_request()

    server.server_close()
    print(result_holder["text"])


if __name__ == "__main__":
    main()
