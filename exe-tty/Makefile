# make        build ./exe-tty for this machine
# make test   run the web and Go tests
#
# Needs mise, which installs the Go, Node.js, and pnpm pinned in mise.toml.
# The first build downloads the newest exe-scroll release to embed; delete
# exescroll/binary/exe-scroll-* to pick up a newer one.

RUN     := MISE_TRUSTED_CONFIG_PATHS=$(CURDIR) mise exec --
GOOS    ?= $(shell $(RUN) go env GOOS)
GOARCH  ?= $(shell $(RUN) go env GOARCH)
VERSION ?= dev

EXE_SCROLL := exescroll/binary/exe-scroll-$(GOOS)-$(GOARCH)
WEB_SRC    := $(shell find web/src -type f) $(wildcard web/*.json web/*.yaml web/*.ts web/*.html)

.PHONY: all web exe-scroll test

all: web exe-scroll
	GOOS=$(GOOS) GOARCH=$(GOARCH) CGO_ENABLED=0 $(RUN) go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o exe-tty .

web: web/dist/index.html

web/node_modules/.modules.yaml: web/package.json web/pnpm-lock.yaml
	cd web && $(RUN) pnpm install --frozen-lockfile
	touch $@

web/dist/index.html: web/node_modules/.modules.yaml $(WEB_SRC)
	cd web && $(RUN) pnpm run build

exe-scroll: $(EXE_SCROLL)

# GOOS and GOARCH are for the binary being built, not this tool.
$(EXE_SCROLL):
	GOOS= GOARCH= $(RUN) go run ./exescroll/fetch $@

test: web/node_modules/.modules.yaml exe-scroll
	cd web && $(RUN) pnpm run typecheck && $(RUN) pnpm run test
	$(RUN) go test ./...
