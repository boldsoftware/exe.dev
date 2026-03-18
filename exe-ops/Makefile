.PHONY: build build-agent build-server build-ui build-agent-embed test clean fmt run-server run-agent

BIN_DIR := bin
TOKEN   ?= dev-token
DB      ?= exe-ops.db
ADDR    ?= :8080
SERVER  ?= http://localhost:8080
NAME    ?= $(shell hostname)
INTERVAL ?= 30s
TAGS    ?=

VERSION ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse HEAD 2>/dev/null || echo unknown)
DATE    ?= $(shell date -u '+%Y-%m-%dT%H:%M:%SZ')
LDFLAGS := -X exe.dev/exe-ops/version.Version=$(VERSION) -X exe.dev/exe-ops/version.Commit=$(COMMIT) -X exe.dev/exe-ops/version.Date=$(DATE)

# Target architectures for agent binaries embedded in the server.
AGENT_TARGETS ?= linux-amd64

build: build-agent build-server

build-agent:
	@mkdir -p $(BIN_DIR)
	go build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/exe-ops-agent ./cmd/exe-ops-agent

build-agent-embed:
	@for target in $(AGENT_TARGETS); do \
		os=$${target%%-*}; \
		arch=$${target##*-}; \
		echo "Building agent for $$os/$$arch"; \
		mkdir -p server/agentbin/binaries/$$target; \
		GOOS=$$os GOARCH=$$arch go build -ldflags "$(LDFLAGS)" -o server/agentbin/binaries/$$target/exe-ops-agent ./cmd/exe-ops-agent; \
	done

build-server: build-ui build-agent-embed
	@mkdir -p $(BIN_DIR)
	go build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/exe-ops-server ./cmd/exe-ops-server

build-ui:
	cd ui && npm ci && npm run build

run-server: build-server
	$(BIN_DIR)/exe-ops-server --addr $(ADDR) --db $(DB) --token $(TOKEN) --tls=false

run-agent: build-agent
	$(BIN_DIR)/exe-ops-agent --server $(SERVER) --token $(TOKEN) --name $(NAME) --interval $(INTERVAL) $(if $(TAGS),--tags $(TAGS))

test:
	go test ./...

fmt:
	goimports -w .
	gofmt -w .

clean:
	rm -rf $(BIN_DIR)
	rm -rf ui/dist
	mkdir -p ui/dist
	touch ui/dist/.gitkeep
	rm -f server/agentbin/binaries/*/exe-ops-agent
