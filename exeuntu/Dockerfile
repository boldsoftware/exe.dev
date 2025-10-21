# Stage 1: Get Chrome/Chromium from chromedp/headless-shell
FROM docker.io/chromedp/headless-shell:stable AS chrome

# Build generate-gateway-token and shelley as static binaries
FROM golang:1.25.3-alpine AS go-builder
ARG TARGETARCH
ARG TARGETOS=linux

# Install Node.js 22 from tarball and build tools - use alpine node package instead of tarball
RUN apk add --no-cache make nodejs npm && \
    node --version && npm --version

ENV PATH="/usr/local/bin:${PATH}"

# Copy the generate-gateway-token source code so we can build it
COPY exeuntu/generate-gateway-token/ /src/generate-gateway-token/
WORKDIR /src/generate-gateway-token/

# Build static binary for the target architecture
RUN GOOS=${TARGETOS} GOARCH=${TARGETARCH} CGO_ENABLED=0 \
    go build -ldflags="-s -w" -a -installsuffix cgo -o generate-gateway-token ./cmd

# Build Shelley using its Makefile
COPY shelley/ /src/shelley/
WORKDIR /src/shelley/
RUN make build

FROM ubuntu:24.04

# Switch from dash to bash by default.
SHELL ["/bin/bash", "-euxo", "pipefail", "-c"]

# Remove minimization restrictions and install packages with documentation
# We aim for a usable non-minimal system.
RUN sed -i 's|http://archive.ubuntu.com/ubuntu/|http://mirror://mirrors.ubuntu.com/mirrors.txt|' /etc/apt/sources.list && \
        rm -f /etc/dpkg/dpkg.cfg.d/excludes /etc/dpkg/dpkg.cfg.d/01_nodoc && \
	apt-get update && \
	# Pre-configure debconf to avoid interactive prompts
	echo 'debconf debconf/frontend select Noninteractive' | debconf-set-selections && \
	# Pre-configure pbuilder to avoid mirror prompt
	echo 'pbuilder pbuilder/mirrorsite string http://archive.ubuntu.com/ubuntu' | debconf-set-selections && \
	# Run unminimize with single 'y' response to restore documentation
	echo 'y' | DEBIAN_FRONTEND=noninteractive unminimize && \
	# Install man-db and reinstall all base packages to get their man pages back
	DEBIAN_FRONTEND=noninteractive apt-get install -y man-db && \
	DEBIAN_FRONTEND=noninteractive apt-get install -y --reinstall $(dpkg-query -f '${binary:Package} ' -W) && \
	mandb -c && \
	DEBIAN_FRONTEND=noninteractive apt-get install -y \
		ca-certificates wget \
		git jq sqlite3 curl vim lsof iproute2 less nginx \
		make python3-pip python-is-python3 tree net-tools file build-essential \
		pipx psmisc bsdmainutils sudo socat \
		openssh-server openssh-client \
		iputils-ping socat netcat-openbsd \
		libcap2-bin \
		unzip util-linux rsync \
		ubuntu-server ubuntu-dev-tools ubuntu-standard \
		man-db manpages manpages-dev \
		systemd systemd-sysv \
		atop iotop \
		golang-go git \
		libglib2.0-0 libnss3 libx11-6 libxcomposite1 libxdamage1 \
		libxext6 libxi6 libxrandr2 libgbm1 libgtk-3-0 \
		fonts-noto-color-emoji fonts-symbola \
		docker.io docker-buildx docker-compose-v2 \
		&& \
	# Allow non-root users to use ping without sudo by granting CAP_NET_RAW
	setcap cap_net_raw=+ep /usr/bin/ping && \
	fc-cache -f -v && \
	apt-get clean && \
	rm -rf /var/lib/apt/lists/*

# Configure systemd
RUN rm /etc/systemd/system/multi-user.target.wants/console-setup.service \
		/etc/systemd/system/multi-user.target.wants/ModemManager.service \
		/etc/systemd/system/multi-user.target.wants/snapd.* \
		/etc/systemd/system/multi-user.target.wants/unattended-upgrades.* \
		/etc/systemd/system/multi-user.target.wants/ubuntu-advantage.service && \
	systemctl mask -- getty.target \
		systemd-random-seed.service \
		iscsid.socket \
		dm-event.socket \
		man-db.timer \
		update-notifier-download.timer \
		update-notifier-motd.timer \
		atop-rotate.timer \
		dpkg-db-backup.timer \
		e2scrub_all.timer \
		etc-resolv.conf.mount \
		etc-hosts.mount \
		etc-hostname.mount \
		-.mount \
		systemd-resolved.service \
		systemd-remount-fs.service \
		systemd-sysusers.service \
		systemd-update-done.service \
		systemd-update-utmp.service \
		systemd-journal-catalog-update.service \
		modprobe@.service \
		systemd-modules-load.service \
		systemd-journal-flush.service \
		ldconfig.service \
		unattended-upgrades.service \
		systemd-logind.service \
		lxd-installer.socket \
	        console-getty.service \
		keyboard-setup.service \
		systemd-ask-password-console.path \
		systemd-ask-password-wall.path \
		ssh.socket \
		plymouth.service \
		plymouth-start.service \
		plymouth-quit.service \
		plymouth-quit-wait.service \
		plymouth-read-write.service \
		plymouth-switch-root.service \
		plymouth-switch-root-initramfs.service \
		plymouth-halt.service \
		plymouth-reboot.service \
		plymouth-poweroff.service \
		plymouth-kexec.service \
		apt-daily-upgrade.timer \
		apt-daily.timer \
		plymouth-log.service && \
	systemctl disable docker.service systemd-logind.service getty.target \
		nginx.service \
                   console-getty.service \
		   atop.service \
                   getty@.service \
                   snapd.socket \
		   motd-news.timer motd-news.service \
		    apport.service apport-autoreport.timer apport-autoreport.path apport-forward.socket \
		    snapd.snap-repair.timer snapd.snap-repair.service \
		    udisks2.service \
		   ufw.service \
		   lvm2-lvmpolld.socket \
                   systemd-ask-password-wall.service \
                   systemd-ask-password-console.service \
                   systemd-machine-id-commit.service \
                   systemd-modules-load.service \
                   systemd-sysctl.service \
                   systemd-firstboot.service \
                   systemd-udevd.service \
                   systemd-udev-trigger.service \
                   systemd-udev-settle.service \
		   e2scrub_reap.service \
		   systemd-user-sessions.service \
		   systemd-update-utmp.service \
		   atopacct.service \
		   sysstat.service \
                   systemd-hwdb-update.service && \
	mkdir -p /etc/systemd/system.conf.d && \
    		echo '[Manager]' > /etc/systemd/system.conf.d/container-overrides.conf && \
    		echo 'LogLevel=info' >> /etc/systemd/system.conf.d/container-overrides.conf && \
    		echo 'LogTarget=console' >> /etc/systemd/system.conf.d/container-overrides.conf && \
    		echo 'SystemCallArchitectures=native' >> /etc/systemd/system.conf.d/container-overrides.conf && \
	systemctl set-default multi-user.target

# Modify existing ubuntu user (UID 1000) to become exedev user
RUN usermod -l exedev ubuntu && \
	groupmod -n exedev ubuntu && \
	mv /home/ubuntu /home/exedev && \
	usermod -d /home/exedev exedev && \
	usermod -aG sudo exedev && \
	usermod -aG docker exedev && \
	echo 'exedev ALL=(ALL) NOPASSWD:ALL' >> /etc/sudoers

ENV EXEUNTU=1

# https://github.com/trfore/docker-ubuntu2404-systemd/blob/main/Dockerfile suggests the following
# might be useful?
# STOPSIGNAL SIGRTMIN+3

# Install Node.js v22.20.0 to /usr/local
RUN ARCH=$(uname -m) && \
	case $ARCH in \
		x86_64) NODE_ARCH=x64 ;; \
		aarch64) NODE_ARCH=arm64 ;; \
		*) echo "Unsupported architecture: $ARCH" && exit 1 ;; \
	esac && \
	cd /tmp && \
	wget -q -O node.tar.xz "https://nodejs.org/dist/v22.20.0/node-v22.20.0-linux-${NODE_ARCH}.tar.xz" && \
	tar -C /usr/local --strip-components=1 -xJf node.tar.xz && \
	rm node.tar.xz && \
	node --version && npm --version && npm install -g @anthropic-ai/claude-code @openai/codex

# Copy the pre-built static binaries from the builder stage
COPY --from=go-builder /src/generate-gateway-token/generate-gateway-token /usr/local/bin/generate-gateway-token
RUN chmod +x /usr/local/bin/generate-gateway-token

COPY --from=go-builder /src/shelley/bin/shelley /usr/local/bin/shelley
RUN chmod +x /usr/local/bin/shelley

# Copy the self-contained Chrome bundle from chromedp/headless-shell
COPY --from=chrome /headless-shell /headless-shell
ENV PATH="/usr/local/bin:/headless-shell:${PATH}"

RUN mkdir -p /home/exedev/src && chown exedev:exedev /home/exedev/src

USER exedev

# Add claude settings file
# TODO(philip,etc.): I've not yet had Claude work with our LLM
# gateway, so let's just have it installed without for now.
# COPY exeuntu/claude-settings.json /home/exedev/.claude.json

WORKDIR /home/exedev/src

# Initialize git repo for the welcome example (optional)
RUN git config --global user.email "system@exe.dev" && \
    git config --global user.name "Exe.dev System" && \
    git init .

# Copy welcome source code and build it
COPY --chown=exedev:exedev exeuntu/welcome/ ./
RUN make build

# Switch back to root to install systemd service
USER root

# Install and enable the welcome web server systemd service
RUN cp /home/exedev/src/welcome.service /etc/systemd/system/welcome.service && \
    systemctl enable welcome.service

# Install polkit rule to allow exedev user to manage welcome service
COPY exeuntu/welcome-manage.polkit /etc/polkit-1/rules.d/50-welcome-manage.rules
RUN chmod 644 /etc/polkit-1/rules.d/50-welcome-manage.rules

# Create systemd service for Shelley
COPY exeuntu/shelley.service /etc/systemd/system/shelley.service
RUN chmod 644 /etc/systemd/system/shelley.service && systemctl enable shelley.service

# TODO(crawshaw/philip): This is called init so that exetini decides
# this wrapper script is an init, and exec's it rather than forking it.
# It would be better if you could indicate that via an env variable or something.
COPY exeuntu/init-wrapper.sh /usr/local/bin/init

# Expose the web server ports
EXPOSE 8000 9999

LABEL "exe.dev/login-user"="exedev"
CMD ["/usr/local/bin/init"]
