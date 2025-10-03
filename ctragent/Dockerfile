# Stage 1: Get Chrome/Chromium from chromedp/headless-shell
FROM docker.io/chromedp/headless-shell:stable AS chrome

# Stage 2: Main application image
FROM ubuntu:24.04

# Git configuration arguments
ARG GIT_USER_NAME="Container User"
ARG GIT_USER_EMAIL="user@example.com"

# Switch from dash to bash by default.
SHELL ["/bin/bash", "-euxo", "pipefail", "-c"]

RUN echo 'Acquire::Queue-Mode "access";' > /etc/apt/apt.conf.d/99parallel

# attempt to keep package installs lean
RUN printf '%s\n' \
      'path-exclude=/usr/share/man/*' \
      'path-exclude=/usr/share/doc/*' \
      'path-exclude=/usr/share/doc-base/*' \
      'path-exclude=/usr/share/info/*' \
      'path-exclude=/usr/share/locale/*' \
      'path-exclude=/usr/share/groff/*' \
      'path-exclude=/usr/share/lintian/*' \
      'path-exclude=/usr/share/zoneinfo/*' \
    > /etc/dpkg/dpkg.cfg.d/01_nodoc

# Install system packages (removed chromium, will use headless-shell instead)
RUN apt-get update; \
	apt-get install -y --no-install-recommends \
		ca-certificates wget tmux \
		git jq sqlite3 gh ripgrep fzf python3 curl vim lsof iproute2 less \
		docker.io docker-compose-v2 docker-buildx \
		make python3-pip python-is-python3 tree net-tools file build-essential \
		pipx cargo psmisc bsdmainutils openssh-client sudo \
		unzip util-linux xz-utils \
		libglib2.0-0 libnss3 libx11-6 libxcomposite1 libxdamage1 \
		libxext6 libxi6 libxrandr2 libgbm1 libgtk-3-0 \
		fonts-noto-color-emoji fonts-symbola && \
	fc-cache -f -v && \
	apt-get clean && \
	rm -rf /var/lib/apt/lists/* && \
	rm -rf /usr/share/{doc,doc-base,info,lintian,man,groff,locale,zoneinfo}/*

# Create non-root user 'agent' and add to sudoers
RUN useradd -m -s /bin/bash agent && \
    echo "agent ALL=(ALL) NOPASSWD:ALL" >> /etc/sudoers

# Install Node.js 22 via nodeenv and install CLIs (claude, codex, happy) using that npm
RUN pipx install nodeenv && \
    NODE_VER=$(curl -fsSL https://nodejs.org/dist/index.json | jq -r '[.[] | select(.version | startswith("v22."))][0].version' | sed 's/^v//') && \
    echo "Installing Node.js ${NODE_VER} via nodeenv" && \
    /root/.local/bin/nodeenv --node=${NODE_VER} /opt/node22

# Ensure nodeenv binaries are preferred and npm globals go into /opt/node22
ENV PATH="/opt/node22/bin:${PATH}"
ENV NPM_CONFIG_PREFIX="/opt/node22"
ENV NPM_CONFIG_UPDATE_NOTIFIER="false"

# Install CLI tools globally using nodeenv's npm and verify
RUN npm i -g @anthropic-ai/claude-code @openai/codex happy-coder && \
    npm cache clean --force || true && \
    command -v claude && command -v codex && command -v happy

# Install Playwright MCP server
RUN npm i -g @playwright/mcp && \
    npm cache clean --force || true

RUN claude mcp add playwright npx @playwright/mcp@latest
RUN npm install -g playwright
RUN playwright install chromium --with-deps

RUN echo '{"storage-driver":"vfs", "bridge":"none", "iptables":false, "ip-forward": false}' \
	> /etc/docker/daemon.json

ENV GO_VERSION=1.25.1
ENV GOROOT=/usr/local/go
ENV GOPATH=/go
ENV PATH=$GOROOT/bin:$GOPATH/bin:$PATH

RUN ARCH=$(uname -m) && \
	case $ARCH in \
		x86_64) GOARCH=amd64 ;; \
		aarch64) GOARCH=arm64 ;; \
		*) echo "Unsupported architecture: $ARCH" && exit 1 ;; \
	esac && \
	wget -O go.tar.gz "https://golang.org/dl/go${GO_VERSION}.linux-${GOARCH}.tar.gz" && \
	tar -C /usr/local -xzf go.tar.gz && \
	rm go.tar.gz

# Create GOPATH directory
RUN mkdir -p "$GOPATH/src" "$GOPATH/bin" && chmod -R 755 "$GOPATH"

# While these binaries install generally useful supporting packages,
# the specific versions are rarely what a user wants so there is no
# point polluting the base image module with them.

RUN go install golang.org/x/tools/cmd/goimports@latest; \
	go install golang.org/x/tools/gopls@latest; \
	go install mvdan.cc/gofumpt@latest; \
	go install github.com/boinkor-net/tsnsrv/cmd/tsnsrv@latest; \
	go clean -cache -testcache -modcache

RUN npx playwright install --with-deps

# Copy the self-contained Chrome bundle from chromedp/headless-shell
COPY --from=chrome /headless-shell /headless-shell
ENV PATH="/headless-shell:${PATH}"

ENV GOTOOLCHAIN=auto
ENV SKETCH=1

# Configure git with build-time arguments
RUN git config --global user.name "${GIT_USER_NAME}" && git config --global user.email "${GIT_USER_EMAIL}"

# Set ownership of key directories to agent user
RUN chown -R agent:agent /opt/node22 /go

# Switch to agent user
USER agent
WORKDIR /home/agent
