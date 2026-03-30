"""Tests for codereview.py parse_review and renderers."""

import os
import sys

sys.path.insert(0, os.path.dirname(__file__))
from codereview import parse_review, render_markdown, render_json


def test_parse_single_item():
    text = """<item>
Some issue description.
<option>
Fix it this way
</option>
<option>
Stet
</option>
</item>"""
    elements = parse_review(text)
    assert len(elements) == 1
    assert elements[0]["body"] == "Some issue description."
    assert len(elements[0]["options"]) == 2
    assert elements[0]["options"][0]["text"] == "Fix it this way"
    assert elements[0]["options"][1]["text"] == "Stet"


def test_parse_multiple_items():
    text = """<item>
First issue.
<option>
Option A
</option>
</item>
<item>
Second issue.
<option>
Option B
</option>
<option>
Option C
</option>
</item>"""
    elements = parse_review(text)
    assert len(elements) == 2
    assert elements[0]["body"] == "First issue."
    assert len(elements[0]["options"]) == 1
    assert elements[1]["body"] == "Second issue."
    assert len(elements[1]["options"]) == 2


def test_parse_multiline_body():
    text = """<item>
Line one.

```python
code_here()
```

Line three.
<option>
Fix
</option>
</item>"""
    elements = parse_review(text)
    assert len(elements) == 1
    assert "Line one." in elements[0]["body"]
    assert "code_here()" in elements[0]["body"]
    assert "Line three." in elements[0]["body"]


def test_parse_multiline_option():
    text = """<item>
Issue.
<option>
Do this thing
that spans lines
</option>
</item>"""
    elements = parse_review(text)
    assert "spans lines" in elements[0]["options"][0]["text"]


def test_parse_empty_input():
    assert parse_review("") == []
    assert parse_review("no tags here") == []


def test_parse_ignores_text_outside_items():
    text = """preamble text
<item>
Issue.
<option>
Fix
</option>
</item>
trailing text"""
    elements = parse_review(text)
    assert len(elements) == 1
    assert elements[0]["body"] == "Issue."


def test_render_markdown():
    elements = [
        {"body": "First issue.", "options": [{"text": "Fix it"}, {"text": "Stet"}]},
        {"body": "Second issue.", "options": [{"text": "Do X"}]},
    ]
    md = render_markdown(elements)
    assert "1. First issue." in md
    assert "  1a. Fix it" in md
    assert "  1b. Stet" in md
    assert "2. Second issue." in md
    assert "  2a. Do X" in md


def test_render_json():
    elements = [
        {"body": "Issue.", "options": [{"text": "Fix"}, {"text": "Skip"}]},
    ]
    result = render_json(elements)
    assert len(result["items"]) == 1
    item = result["items"][0]
    assert item["number"] == 1
    assert item["body"] == "Issue."
    assert item["options"][0] == {"letter": "a", "text": "Fix"}
    assert item["options"][1] == {"letter": "b", "text": "Skip"}


if __name__ == "__main__":
    for name, fn in list(globals().items()):
        if name.startswith("test_") and callable(fn):
            fn()
            print(f"  pass: {name}")
    print("All tests passed.")
