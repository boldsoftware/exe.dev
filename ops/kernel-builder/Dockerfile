# Dockerfile to build a custom Linux kernel for Cloud Hypervisor with nftables support
# Uses Kata Containers' official build-kernel.sh script
# Based on: https://github.com/kata-containers/kata-containers/tree/main/tools/packaging/kernel

FROM ubuntu:24.04

ARG KERNEL_VERSION=6.12.42
ARG KATA_BRANCH=main
ENV DEBIAN_FRONTEND=noninteractive

# Install build dependencies
# Based on Kata's kernel build requirements
RUN apt-get update && apt-get install -y \
    build-essential \
    git \
    bc \
    bison \
    flex \
    libssl-dev \
    libelf-dev \
    libncurses-dev \
    wget \
    xz-utils \
    curl \
    cpio \
    kmod \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /workspace

# Clone Kata Containers repository
RUN git clone --depth=1 --branch=${KATA_BRANCH} \
    https://github.com/kata-containers/kata-containers.git

WORKDIR /workspace/kata-containers/tools/packaging/kernel

# Copy nftables config fragment and build script
COPY nftables.conf configs/fragments/nftables.conf
COPY build-kata-kernel.sh /usr/local/bin/

RUN chmod +x /usr/local/bin/build-kata-kernel.sh && \
    build-kata-kernel.sh

# Export the output directory
FROM scratch AS export
COPY --from=0 /output /
