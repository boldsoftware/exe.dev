FROM ubuntu:24.04

# Switch from dash to bash by default.
SHELL ["/bin/bash", "-euxo", "pipefail", "-c"]

# Remove minimization restrictions and install packages with documentation
# We aim for a usable non-minimal system.
RUN rm -f /etc/dpkg/dpkg.cfg.d/excludes /etc/dpkg/dpkg.cfg.d/01_nodoc && \
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
		git jq sqlite3 curl vim lsof iproute2 less \
		make python3-pip python-is-python3 tree net-tools file build-essential \
		pipx psmisc bsdmainutils sudo socat \
		openssh-server openssh-client \
		iputils-ping socat netcat-openbsd \
		unzip util-linux rsync \
		ubuntu-server ubuntu-dev-tools ubuntu-standard \
		man-db manpages manpages-dev \
		systemd systemd-sysv \
		golang-go git && \
	apt-get clean && \
	rm -rf /var/lib/apt/lists/*

# Configure systemd
RUN rm /etc/systemd/system/multi-user.target.wants/console-setup.service \
		/etc/systemd/system/multi-user.target.wants/ModemManager.service \
		/etc/systemd/system/multi-user.target.wants/snapd.* \
		/etc/systemd/system/multi-user.target.wants/ubuntu-advantage.service && \
	systemctl mask getty.target console-getty.service && \
	systemctl disable systemd-logind.service getty.target \
                   console-getty.service \
                   getty@.service \
                   snapd.socket \
                   systemd-ask-password-wall.service \
                   systemd-ask-password-console.service \
                   systemd-machine-id-commit.service \
                   systemd-modules-load.service \
                   systemd-sysctl.service \
                   systemd-firstboot.service \
                   systemd-udevd.service \
                   systemd-udev-trigger.service \
                   systemd-udev-settle.service \
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
	echo 'exedev ALL=(ALL) NOPASSWD:ALL' >> /etc/sudoers

ENV EXEUNTU=1

# https://github.com/trfore/docker-ubuntu2404-systemd/blob/main/Dockerfile suggests the following
# might be useful?
# STOPSIGNAL SIGRTMIN+3

# Add claude script to PATH (in /usr/bin to avoid conflict with npm's /usr/local/bin/claude)
COPY claude /usr/bin/claude
RUN chmod +x /usr/bin/claude

# Add gateway token generator script
COPY generate-gateway-token /usr/local/bin/generate-gateway-token
RUN chmod +x /usr/local/bin/generate-gateway-token

RUN mkdir -p /app && chown exedev /app

USER exedev

# Add claude settings file
COPY claude-settings.json /home/exedev/.claude.json

WORKDIR /app

# Initialize git repo and add welcome code
RUN git config --global user.email "system@exe.dev" && \
    git config --global user.name "Exe.dev System" && \
    git init .

# Copy welcome source code
COPY welcome/ ./

# Create .gitignore to exclude binary
RUN echo 'webserver' > .gitignore

# Commit the initial code to git (excluding binary)
RUN git add . && \
    git commit -m "Initial welcome web server code"

# Build the Go web server
RUN go build -o webserver main.go && rm -rf ~/.cache

# TODO(crawshaw/philip): This is called init so that exetini decides
# this wrapper script is an init, and exec's it rather than forking it.
# It would be better if you could indicate that via an env variable or something.
COPY init-wrapper.sh /usr/local/bin/init

USER root
CMD ["/usr/local/bin/init"]
