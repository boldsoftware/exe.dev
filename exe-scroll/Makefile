# Developer Makefile for exe-scroll.
#
# exe-scroll imports Ghostty's `ghostty-vt` Zig module directly, so it needs a
# Ghostty checkout to build against. A bare `make` fetches Ghostty at the pinned
# revision into ./.ghostty automatically, so it works out of the box. To build
# against a checkout you already have, point GHOSTTY_SRC at it:
#
#   make                                     # -> zig-out/bin/exe-scroll
#   make test                                # build + run the e2e tests
#   make GHOSTTY_SRC=/path/to/ghostty        # use an existing checkout instead

ZIG ?= zig
OPTIMIZE ?= ReleaseFast
BIN := zig-out/bin/exe-scroll

# Pinned Ghostty revision (kept in sync with build-static.sh).
GHOSTTY_REV ?= 48d3e972d839999745368b156df396d9512fd17b
GHOSTTY_REPO_URL ?= https://github.com/ghostty-org/ghostty.git
# Where `make` clones Ghostty when GHOSTTY_SRC isn't supplied.
GHOSTTY_CACHE := .ghostty
# Use the caller's checkout if given, otherwise our managed clone.
GHOSTTY_SRC ?=

$(BIN): exe-scroll.zig build.zig build.zig.zon ghostty-src
	$(ZIG) build -Doptimize=$(OPTIMIZE)

.PHONY: build test clean
build: $(BIN)

# `zig build test` builds the binary and runs test_e2e.zig against it.
test: ghostty-src
	$(ZIG) build test

# Create the ./ghostty-src symlink the build.zig.zon dependency resolves
# through. If GHOSTTY_SRC is set, point at it; otherwise fetch a pinned clone.
.PHONY: ghostty-src
ghostty-src:
ifeq ($(strip $(GHOSTTY_SRC)),)
	@if [ ! -d "$(GHOSTTY_CACHE)/.git" ]; then \
		echo "Cloning Ghostty into $(GHOSTTY_CACHE)..."; \
		git clone --filter=tree:0 "$(GHOSTTY_REPO_URL)" "$(GHOSTTY_CACHE)"; \
	fi
	@git -C "$(GHOSTTY_CACHE)" cat-file -e "$(GHOSTTY_REV)^{commit}" 2>/dev/null \
		|| git -C "$(GHOSTTY_CACHE)" fetch --filter=tree:0 origin "$(GHOSTTY_REV)"
	@git -C "$(GHOSTTY_CACHE)" checkout --quiet --detach "$(GHOSTTY_REV)"
	@ln -sfn "$(GHOSTTY_CACHE)" ghostty-src
else
	@ln -sfn "$(GHOSTTY_SRC)" ghostty-src
endif

clean:
	rm -rf .zig-cache zig-out ghostty-src

# `make distclean` also removes the managed Ghostty clone.
.PHONY: distclean
distclean: clean
	rm -rf $(GHOSTTY_CACHE)
