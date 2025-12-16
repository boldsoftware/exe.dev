# D2 Documentation Build
# Uses go run to invoke d2, no separate installation needed.

D2 ?= go run oss.terrastruct.com/d2@latest

D2_FILES := $(wildcard *.d2)
SVG_FILES := $(D2_FILES:.d2=.svg)
PNG_FILES := $(D2_FILES:.d2=.png)
PDF_FILES := $(D2_FILES:.d2=.pdf)

.PHONY: all clean svg png pdf serve

all: svg

svg: $(SVG_FILES)

png: $(PNG_FILES)

pdf: $(PDF_FILES)

%.svg: %.d2
	$(D2) $< $@

%.png: %.d2
	$(D2) $< $@

%.pdf: %.d2
	$(D2) $< $@

clean:
	rm -f $(SVG_FILES) $(PNG_FILES) $(PDF_FILES)

# Serve the docs locally
serve: svg
	@echo "Serving on http://localhost:8000"
	python3 -m http.server 8000
