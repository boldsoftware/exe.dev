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
		man-db manpages manpages-dev && \
	apt-get clean && \
	rm -rf /var/lib/apt/lists/*

# Modify existing ubuntu user (UID 1000) to become exedev user
RUN usermod -l exedev ubuntu && \
	groupmod -n exedev ubuntu && \
	mv /home/ubuntu /home/exedev && \
	usermod -d /home/exedev exedev && \
	usermod -aG sudo exedev && \
	echo 'exedev ALL=(ALL) NOPASSWD:ALL' >> /etc/sudoers

ENV EXEUNTU=1

# Add claude script to PATH (in /usr/bin to avoid conflict with npm's /usr/local/bin/claude)
COPY claude /usr/bin/claude
RUN chmod +x /usr/bin/claude

USER exedev
