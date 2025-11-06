# syntax=docker/dockerfile:1

# This Dockerfile builds our "read only volume" (rovol) binaries.
# They happen to be checked in, but once we move to "make" from "go build",
# we can just do this build step there. "make container/rovol/arm64" is
# roughly the build step.
#
# The rovol binaries aren't quite statically linked, but they come with their
# own musl, so they should run anywhere we care for them to run.

FROM alpine:3.19 AS build
ARG TARGETARCH
ARG OPENSSH_VERSION=9.9p1
ARG BUSYBOX_VERSION=1.36.1

# Install build dependencies (no OpenSSL needed!)
RUN apk add --no-cache \
  build-base \
  linux-headers \
  curl \
  tar \
  bzip2 \
  musl-dev \
  cmake \
  autoconf \
  automake

# Set up /exe.dev/lib with the interpreter so compilation can use it
RUN mkdir -p /exe.dev/lib && \
  cp -v /lib/ld-musl-*.so.1 /exe.dev/lib/ld-musl.so.1

WORKDIR /src
RUN curl -fsSLO https://cdn.openbsd.org/pub/OpenBSD/OpenSSH/portable/openssh-${OPENSSH_VERSION}.tar.gz \
  && tar xzf openssh-${OPENSSH_VERSION}.tar.gz
WORKDIR /src/openssh-${OPENSSH_VERSION}

# Configure with minimal features, no OpenSSL, using built-in crypto
# Set rpath and interpreter to use our /exe.dev paths
# Use -O1 to avoid compiler crashes and disable stack protector
ENV CFLAGS="-O1 -fno-strict-aliasing -fno-stack-protector"
# Ensure OpenSSH fallback shell is /exe.dev/bin/sh
ENV CPPFLAGS="-U_PATH_BSHELL -D_PATH_BSHELL=\"/exe.dev/bin/sh\""
ENV LDFLAGS="-Wl,-rpath,/exe.dev/lib -Wl,--dynamic-linker,/exe.dev/lib/ld-musl.so.1"
RUN ./configure \
  --prefix=/exe.dev \
  --exec-prefix=/exe.dev \
  --bindir=/exe.dev/bin \
  --sbindir=/exe.dev/bin \
  --libexecdir=/exe.dev/bin \
  --sysconfdir=/etc/ssh \
  --with-privsep-user=sshd \
  --with-privsep-path=/exe.dev/var/empty \
  --without-openssl \
  --without-zlib \
  --without-pam \
  --without-shadow \
  --without-libedit \
  --disable-lastlog \
  --disable-utmp \
  --disable-utmpx \
  --disable-wtmp \
  --disable-wtmpx \
  --disable-pututline \
  --disable-pututxline \
  --disable-security-key \
  --disable-pkcs11 \
  --without-kerberos5 \
  --without-ldns \
  --disable-strip \
  --with-security-key-builtin=no \
  --without-hardening

RUN \
  if grep -q '^#define USE_BTMP' config.h; then \
  sed -i 's/^#define USE_BTMP.*/\/\* #undef USE_BTMP \*\//' config.h; \
  fi


# Fix setlinebuf conflict with musl libc
# This is a known issue with OpenSSH 9.9p1 and Alpine's musl
RUN sed -i '/#define setlinebuf/d' openbsd-compat/bsd-misc.h

# Build all necessary binaries
# Retry on failure to work around intermittent compiler crashes
RUN make || make || make
RUN strip --strip-all --remove-section=.comment --remove-section=.note \
  sshd sshd-session sftp-server

# Create output directory structure
RUN mkdir -p /out/exe.dev/bin /out/exe.dev/lib

# Copy the built binaries (they already have correct interpreter and rpath)
COPY exelet/rovol/etc/ssh/sshd_config /out/exe.dev/etc/ssh/sshd_config
RUN cp -v sshd /out/exe.dev/bin/
RUN cp -v sshd-session /out/exe.dev/bin/
RUN cp -v sftp-server /out/exe.dev/bin/

# var/empty for priv separation
RUN mkdir -p /out/exe.dev/var/empty
RUN touch /out/exe.dev/var/empty/placeholder

# Build BusyBox from source for a capable shell and install as /exe.dev/bin/sh
WORKDIR /src
RUN curl -fsSLO https://busybox.net/downloads/busybox-${BUSYBOX_VERSION}.tar.bz2 \
  && tar xjf busybox-${BUSYBOX_VERSION}.tar.bz2
WORKDIR /src/busybox-${BUSYBOX_VERSION}
RUN make defconfig && \
  sed -i 's/^CONFIG_STATIC=y/# CONFIG_STATIC is not set/' .config || true && \
  if grep -q '^# CONFIG_STATIC is not set' .config; then :; else echo '# CONFIG_STATIC is not set' >> .config; fi && \
  sed -i 's/^# CONFIG_FEATURE_SH_STANDALONE is not set/CONFIG_FEATURE_SH_STANDALONE=y/' .config || \
  (grep -q '^CONFIG_FEATURE_SH_STANDALONE=y' .config || echo 'CONFIG_FEATURE_SH_STANDALONE=y' >> .config) && \
  sed -i 's/^# CONFIG_ASH is not set/CONFIG_ASH=y/' .config || \
  (grep -q '^CONFIG_ASH=y' .config || echo 'CONFIG_ASH=y' >> .config) && \
  sed -i 's/^# CONFIG_SH_IS_ASH is not set/CONFIG_SH_IS_ASH=y/' .config || \
  (grep -q '^CONFIG_SH_IS_ASH=y' .config || echo 'CONFIG_SH_IS_ASH=y' >> .config) && \
  sed -i 's/^# CONFIG_ASH_BASH_COMPAT is not set/CONFIG_ASH_BASH_COMPAT=y/' .config || true && \
  sed -i 's/^# CONFIG_ASH_JOB_CONTROL is not set/CONFIG_ASH_JOB_CONTROL=y/' .config || true && \
  yes "" | make oldconfig && \
  make busybox && \
  strip --strip-all busybox && \
  apk add --no-cache patchelf && \
  cp -v busybox /out/exe.dev/bin/sh && \
  patchelf --set-interpreter /exe.dev/lib/ld-musl.so.1 /out/exe.dev/bin/sh


# Build dtach using our shared script
COPY exelet/rovol/build-dtach.sh /build-dtach.sh
RUN /build-dtach.sh ${TARGETARCH}
RUN cp -v /src/dtach/dtach-*/dtach /out/exe.dev/bin/

# Check that rpath is set correctly
RUN readelf -d /out/exe.dev/bin/sshd | grep -E 'RPATH|RUNPATH' || echo "No RPATH found"

# Copy required shared libraries (should be minimal without OpenSSL)
RUN for bin in /out/exe.dev/bin/*; do \
  if [ -f "$bin" ]; then \
  ldd "$bin" 2>/dev/null | grep "=>" | awk '{print $3}' | while read lib; do \
  if [ -f "$lib" ] && [ ! -f "/out/exe.dev/lib/$(basename $lib)" ]; then \
  cp -v "$lib" /out/exe.dev/lib/; \
  fi \
  done; \
  fi \
  done

# Copy the musl interpreter as ld-musl.so.1 only
RUN cp -v /lib/ld-musl-*.so.1 /out/exe.dev/lib/ld-musl.so.1

# Check binary sizes for comparison
RUN echo "=== Binary sizes ===" && \
  ls -lh /out/exe.dev/bin/ && \
  echo "=== Library dependencies ===" && \
  ldd /out/exe.dev/bin/sshd 2>/dev/null || echo "No dynamic dependencies"

# exe-init
FROM golang:latest AS exe-init
COPY . /src
WORKDIR /src/cmd/exe-init
RUN CGO_ENABLED=0 go build -mod=mod -tags osusergo,netgo -ldflags "-extldflags=-static -w" -o /exe-init .

FROM scratch AS out
COPY --from=build /out/exe.dev /
COPY --from=exe-init /exe-init /bin/exe-init
