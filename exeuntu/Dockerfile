# Exeuntu container image - a basic image for exe.dev.
FROM ubuntu:24.04

# Switch from dash to bash by default.
SHELL ["/bin/bash", "-euxo", "pipefail", "-c"]

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

# Install development tools
RUN apt-get update; \
	apt-get install -y --no-install-recommends \
		ca-certificates wget \
		git jq sqlite3 curl vim lsof iproute2 less \
		make python3-pip python-is-python3 tree net-tools file build-essential \
		pipx psmisc bsdmainutils sudo \
		unzip util-linux && \
	apt-get clean && \
	rm -rf /var/lib/apt/lists/* && \
	rm -rf /usr/share/{doc,doc-base,info,lintian,man,groff,locale,zoneinfo}/*

ENV GO_VERSION=1.25.0
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

# Install Go tools and clean up aggressively
RUN go install golang.org/x/tools/cmd/goimports@latest; \
	go install golang.org/x/tools/gopls@latest; \
	go install mvdan.cc/gofumpt@latest; \
	go clean -cache -testcache -modcache && \
	rm -rf /usr/local/go/pkg/tool/*/test2json \
		/usr/local/go/pkg/tool/*/vet \
		/usr/local/go/pkg/tool/*/doc \
		/usr/local/go/test \
		/usr/local/go/api \
		/usr/local/go/doc

ENV GOTOOLCHAIN=auto
ENV EXEUNTU=1
