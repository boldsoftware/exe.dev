# Stage 1: Get Chrome/Chromium from chromedp/headless-shell
FROM docker.io/chromedp/headless-shell:stable AS chrome

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
		ca-certificates wget ripgrep \
		git jq sqlite3 curl vim neovim lsof iproute2 less nginx \
		make python3-pip python-is-python3 tree net-tools file build-essential \
		pipx psmisc bsdmainutils sudo socat \
		openssh-server openssh-client \
		iputils-ping socat netcat-openbsd \
		libcap2-bin \
		unzip util-linux rsync \
		ubuntu-server ubuntu-dev-tools ubuntu-standard \
		man-db manpages manpages-dev \
		mitmproxy \
		systemd systemd-sysv \
		atop btop iotop ncdu \
		golang-go git \
		libglib2.0-0 libnss3 libx11-6 libxcomposite1 libxdamage1 \
		libxext6 libxi6 libxrandr2 libgbm1 libgtk-3-0 \
		fonts-noto-color-emoji fonts-symbola \
		docker.io docker-buildx docker-compose-v2 \
		imagemagick ffmpeg \
		gh \
		dbus-user-session \
		&& apt-get remove -y pollinate ubuntu-fan && \
	# Allow non-root users to use ping without sudo by granting CAP_NET_RAW
	setcap cap_net_raw=+ep /usr/bin/ping && \
	fc-cache -f -v && \
	apt-get clean

# Install uv to /usr/local/bin
RUN curl -LsSf https://astral.sh/uv/install.sh | env UV_INSTALL_DIR=/usr/local/bin sh

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
		systemd-udevd.service \
		systemd-udevd-control.service \
		systemd-udevd-kernel.service \
		systemd-udev-trigger.service \
		systemd-udev-settle.service \
		systemd-hwdb-update.service \
		ubuntu-fan.service \
		ldconfig.service \
		unattended-upgrades.service \
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
	# systemd-logind is disabled but not masked. It's involved in populating the XDG runtime dir sockets... somehow
	systemctl disable docker.service containerd.service getty.target systemd-logind.service \
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
		   systemd-update-utmp.service \
		   atopacct.service \
		   sysstat.service \
                   systemd-hwdb-update.service \
		   multipathd.service && \
	mkdir -p /etc/systemd/system.conf.d && \
    		echo '[Manager]' > /etc/systemd/system.conf.d/container-overrides.conf && \
    		echo 'LogLevel=info' >> /etc/systemd/system.conf.d/container-overrides.conf && \
    		echo 'LogTarget=console' >> /etc/systemd/system.conf.d/container-overrides.conf && \
    		echo 'SystemCallArchitectures=native' >> /etc/systemd/system.conf.d/container-overrides.conf && \
	systemctl set-default multi-user.target

# Modify existing ubuntu user (UID 1000) to become exedev user
RUN usermod -l exedev -c "exe.dev user" ubuntu && \
	groupmod -n exedev ubuntu && \
	mv /home/ubuntu /home/exedev && \
	usermod -d /home/exedev exedev && \
	usermod -aG sudo exedev && \
	usermod -aG docker exedev && \
	sed -i 's/^ubuntu:/exedev:/' /etc/subuid /etc/subgid && \
	echo 'exedev ALL=(ALL) NOPASSWD:ALL' >> /etc/sudoers && \
	echo 'Defaults:exedev verifypw=any' >> /etc/sudoers && \
	# Manually enable linger, this should autopopulate /run/user/1000
	mkdir -p /var/lib/systemd/linger && \
	touch /var/lib/systemd/linger/exedev

ENV EXEUNTU=1

# https://github.com/trfore/docker-ubuntu2404-systemd/blob/main/Dockerfile suggests the following
# might be useful?
# STOPSIGNAL SIGRTMIN+3


# Copy the self-contained Chrome bundle from chromedp/headless-shell
COPY --from=chrome /headless-shell /headless-shell
ENV PATH="/usr/local/bin:/headless-shell:${PATH}"

RUN mkdir -p /home/exedev /home/exedev/.config/shelley && \
    chown exedev:exedev /home/exedev /home/exedev/.config /home/exedev/.config/shelley

USER exedev

WORKDIR /home/exedev

# Update PATH in .bashrc to include .local/bin and set XDG_RUNTIME_DIR for systemd user services
# XDG paths are not autopopulated despite the presense of libpam-systemd. Manually add them here.
RUN echo 'export PATH="$HOME/.local/bin:$PATH"' >> /home/exedev/.bashrc && \
    echo 'export XDG_RUNTIME_DIR="/run/user/$(id -u)"' >> /home/exedev/.bashrc && \
    echo 'export XDG_RUNTIME_DIR="/run/user/$(id -u)"' >> /home/exedev/.profile

# Configure git to use 'main' as default branch name
RUN git config --global init.defaultBranch main

# Switch back to root to install systemd service
USER root

# Disable Ubuntu's default MOTD (the sudo hint, etc.)
RUN rm -rf /etc/update-motd.d/* /etc/motd && touch /home/exedev/.hushlogin && chown exedev:exedev /home/exedev/.hushlogin

# Add custom MOTD to exedev's .bashrc (ignores .hushlogin - we handle that ourselves)
COPY motd-snippet.bash /tmp/motd-snippet.bash
RUN cat /tmp/motd-snippet.bash >> /home/exedev/.bashrc && rm /tmp/motd-snippet.bash

# Create systemd socket and service for Shelley (socket activation)
COPY shelley.socket /etc/systemd/system/shelley.socket
COPY shelley.service /etc/systemd/system/shelley.service
RUN chmod 644 /etc/systemd/system/shelley.socket /etc/systemd/system/shelley.service && \
    systemctl enable shelley.socket

# TODO(crawshaw/philip): This is called init so that exetini decides
# this wrapper script is an init, and exec's it rather than forking it.
# It would be better if you could indicate that via an env variable or something.
COPY init-wrapper.sh /usr/local/bin/init

# Install native codex; installs to /usr/local/bin
RUN ARCH=$(uname -m) && \
    case ${ARCH} in \
        x86_64) CODEX_ARCH="x86_64-unknown-linux-musl" ;; \
        aarch64|arm64) CODEX_ARCH="aarch64-unknown-linux-musl" ;; \
        *) echo "Unsupported architecture: ${ARCH}" && exit 1 ;; \
    esac && \
    CODEX_VERSION=$(curl -fsSL https://api.github.com/repos/openai/codex/releases/latest | jq -r '.tag_name') && \
    curl -fsSL "https://github.com/openai/codex/releases/download/${CODEX_VERSION}/codex-${CODEX_ARCH}.tar.gz" | \
    tar -xzC /usr/local/bin && \
    mv "/usr/local/bin/codex-${CODEX_ARCH}" /usr/local/bin/codex && \
    chmod +x /usr/local/bin/codex

# Create config directories for LLM agents
RUN mkdir -p /home/exedev/.claude /home/exedev/.codex && \
    chown -R exedev:exedev /home/exedev/.claude /home/exedev/.codex

# Copy LLM agent instructions to Claude, Codex, and Shelley config directories
# Shelley uses ~/.config/shelley/ (XDG convention, directory already created above)
COPY AGENTS.md /home/exedev/.config/shelley/AGENTS.md
RUN cp /home/exedev/.config/shelley/AGENTS.md /home/exedev/.claude/CLAUDE.md && \
    cp /home/exedev/.config/shelley/AGENTS.md /home/exedev/.codex/AGENTS.md && \
    chown exedev:exedev /home/exedev/.claude/CLAUDE.md /home/exedev/.codex/AGENTS.md /home/exedev/.config/shelley/AGENTS.md

# Install Claude to the native location (~/.local/bin) so auto-upgrades work correctly.
# Symlink to /usr/local/bin for system-wide PATH access.
RUN mkdir -p /home/exedev/.local/bin && \
    ARCH=$(uname -m | sed 's/x86_64/x64/;s/aarch64/arm64/') && \
    PLATFORM="linux-${ARCH}" && \
    STABLE_VERSION=$(curl -fsSL https://storage.googleapis.com/claude-code-dist-86c565f3-f756-42ad-8dfa-d59b1c096819/claude-code-releases/stable) && \
    EXPECTED_HASH=$(curl -fsSL "https://storage.googleapis.com/claude-code-dist-86c565f3-f756-42ad-8dfa-d59b1c096819/claude-code-releases/${STABLE_VERSION}/manifest.json" | jq -r ".platforms[\"${PLATFORM}\"].checksum") && \
    curl -fsSL "https://storage.googleapis.com/claude-code-dist-86c565f3-f756-42ad-8dfa-d59b1c096819/claude-code-releases/${STABLE_VERSION}/${PLATFORM}/claude" -o /home/exedev/.local/bin/claude && \
    echo "${EXPECTED_HASH}  /home/exedev/.local/bin/claude" | sha256sum -c - && \
    chmod +x /home/exedev/.local/bin/claude && \
    chown -R exedev:exedev /home/exedev/.local && \
    ln -s /home/exedev/.local/bin/claude /usr/local/bin/claude

# Custom nginx config and index page (nginx is installed but disabled by default)
COPY nginx.conf /etc/nginx/sites-available/default
COPY index.html /var/www/html/index.html
RUN chmod 644 /var/www/html/index.html

# Install xterm-ghostty terminfo for Ghostty terminal support
COPY xterm-ghostty.terminfo /tmp/xterm-ghostty.terminfo
RUN tic -x - < /tmp/xterm-ghostty.terminfo && rm /tmp/xterm-ghostty.terminfo

# Copy the pre-built shelley binary (built externally for the target architecture)
# This is placed late in the Dockerfile to maximize layer cache reuse, since
# shelley is the most frequently changing component.
COPY shelley /usr/local/bin/shelley
RUN chmod +x /usr/local/bin/shelley && /usr/local/bin/shelley -help

# Expose the web server ports
EXPOSE 8000 9999

LABEL "exe.dev/login-user"="exedev"
CMD ["/usr/local/bin/init"]
