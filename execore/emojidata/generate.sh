#!/usr/bin/env bash
# Regenerate shortcodes.tsv from the goldmark-emoji definition source.
# Usage: ./generate.sh
set -euo pipefail
cd "$(dirname "$0")"
src=$(go list -m -f '{{.Dir}}' github.com/yuin/goldmark-emoji)/definition/github.go
python3 - "$src" shortcodes.tsv <<'PY'
import re, sys
src = open(sys.argv[1]).read()
out = open(sys.argv[2], "w")
pat = re.compile(r'Emoji\{Name: "([^"]+)", Unicode: \[\]int32\{([^}]+)\}, ShortNames: \[\]string\{([^}]+)\}\}')
for m in pat.finditer(src):
    name, uni, shorts = m.groups()
    codepoints = [int(x.strip()) for x in uni.split(",") if x.strip()]
    glyph = "".join(chr(c) for c in codepoints)
    for sh in re.findall(r'"([^"]+)"', shorts):
        out.write(f"{sh}\t{glyph}\t{name}\n")
out.close()
PY
echo "wrote $(wc -l <shortcodes.tsv) entries to shortcodes.tsv"
