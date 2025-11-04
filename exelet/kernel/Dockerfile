FROM ubuntu:22.04 AS kernel
ARG EXE_VERSION=0.0.0
ARG DISK_SIZE
ARG KERNEL_VERSION=6.12.57
ARG PACKAGES
ARG MIRROR=https://cdn.kernel.org/pub
RUN apt-get update && apt-get install -y build-essential libssl-dev flex autoconf automake bison libelf-dev bc rsync git kmod curl
RUN curl -sSL ${MIRROR}/linux/kernel/v6.x/linux-${KERNEL_VERSION}.tar.xz -o linux.tar.xz
RUN tar xvf linux.tar.xz && ln -sf linux-${KERNEL_VERSION} /linux
COPY . /src
WORKDIR /linux
RUN cp /src/config.$(uname -m) /linux/.config
RUN make olddefconfig
RUN sed -i -r "s/CONFIG_LOCALVERSION.*/CONFIG_LOCALVERSION=\"-exe-${EXE_VERSION}\"/g" .config
RUN if [ "$(uname -m)" = "x86_64" ]; then KCFLAGS="-Wa,-mx86-used-note=no" make bzImage -j `nproc`; cp /linux/arch/x86/boot/compressed/vmlinux.bin /kernel; fi
RUN if [ "$(uname -m)" = "aarch64" ]; then make -j `nproc`; cp /linux/arch/arm64/boot/Image /kernel; fi

FROM scratch
COPY --from=kernel /kernel /kernel
