# Build Cloud Hypervisor and virtiofsd binaries for Lima host provisioning.

FROM rust:1.82-bullseye AS build

ARG CLOUD_HYPERVISOR_VERSION=48.0
ARG VIRTIOFSD_VERSION=1.13.2
ARG TARGETARCH
ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update && apt-get install -y \
    build-essential \
    curl \
    git \
    libcap-ng-dev \
    libseccomp-dev \
    pkg-config \
    && rm -rf /var/lib/apt/lists/*

RUN rustup toolchain install nightly --profile minimal && \
    rustup component add rustfmt --toolchain nightly && \
    rustup default nightly

WORKDIR /build

# Build Cloud Hypervisor
RUN curl -fsSL \
        "https://github.com/cloud-hypervisor/cloud-hypervisor/archive/refs/tags/v${CLOUD_HYPERVISOR_VERSION}.tar.gz" \
        -o cloud-hypervisor.tar.gz && \
    tar xzf cloud-hypervisor.tar.gz

WORKDIR /build/cloud-hypervisor-${CLOUD_HYPERVISOR_VERSION}

RUN cargo +nightly build --release

# Capture artifacts immediately to preserve permissions when reused from cache.
RUN install -Dm755 target/release/cloud-hypervisor /out/bin/cloud-hypervisor && \
    install -Dm755 target/release/ch-remote /out/bin/ch-remote

# Build virtiofsd
WORKDIR /build
RUN git clone --depth=1 --branch "v${VIRTIOFSD_VERSION}" \
        https://gitlab.com/virtio-fs/virtiofsd.git

WORKDIR /build/virtiofsd
RUN cargo +nightly build --release

RUN install -Dm755 target/release/virtiofsd /out/bin/virtiofsd

WORKDIR /out
RUN printf 'cloud_hypervisor_version=%s\nvirtiofsd_version=%s\narch=%s\n' \
        "${CLOUD_HYPERVISOR_VERSION}" \
        "${VIRTIOFSD_VERSION}" \
        "${TARGETARCH:-unknown}" > metadata

FROM scratch AS artifacts
COPY --from=build /out /out
CMD ["/out/bin/cloud-hypervisor"]
