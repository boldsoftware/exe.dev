//go:build linux

package ext4

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// BLKFLSBUF ioctl: fsync_bdev() + invalidate_bdev(). Documented in
// <linux/fs.h>; equivalent to `blockdev --flushbufs`.
const blkflsbuf = 0x1261

// readSuperblockUncached reads the primary ext4 superblock from path,
// bypassing the host's page cache. On block devices and zvols, the
// host kernel caches reads in its bdev page cache. Guest writes to
// a zvol go through ZFS's own path (virtio-blk → zvol → ARC) and
// don't invalidate that cache, so a naive O_RDONLY read returns
// stale superblock bytes for the lifetime of the host.
//
// We try strategies in order until one succeeds; this is
// belt-and-suspenders since we've seen different (kernel, zfs)
// combinations honor different mechanisms:
//
//  1. O_DIRECT pread with a sector-aligned mmap'd buffer. Truly
//     bypasses the page cache. Works reliably with ZFS ≥ 2.2.
//  2. BLKFLSBUF ioctl + buffered read. The ioctl drops the entire
//     bdev cache; the next read goes to disk. Reliable on older
//     kernels.
//  3. POSIX_FADV_DONTNEED on a page-aligned window + buffered read.
//     Asks the kernel nicely to evict the relevant pages.
//
// All three require CAP_SYS_ADMIN on the device; exelet runs as
// root in production and CI. If none succeed we still return the
// (possibly stale) buffered data — better than failing.
func readSuperblockUncached(path string) ([]byte, error) {
	if buf, ok, err := readDirect(path); ok {
		return buf, err
	}
	return readWithCacheDrop(path)
}

func readDirect(path string) ([]byte, bool, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECT|unix.O_CLOEXEC, 0)
	if err != nil {
		if err == unix.EINVAL || err == unix.ENOTSUP {
			return nil, false, nil
		}
		return nil, true, fmt.Errorf("ext4: open %s: %w", path, err)
	}
	defer unix.Close(fd)

	// O_DIRECT requires buffer, offset, and length to be aligned to
	// the device's logical block size (typically 512 or 4096). We
	// allocate a 4 KiB page-aligned buffer covering the first 4 KiB
	// of the device, which contains the primary superblock at
	// offset 1024.
	const readSize = 4096
	buf, err := allocAlignedBuffer(readSize, readSize)
	if err != nil {
		return nil, true, err
	}
	n, err := unix.Pread(fd, buf, 0)
	if err != nil {
		if err == unix.EINVAL {
			return nil, false, nil
		}
		return nil, true, fmt.Errorf("ext4: pread %s: %w", path, err)
	}
	if n < SuperblockOffset+superblockSize {
		return nil, true, fmt.Errorf("ext4: short pread: %d bytes", n)
	}
	out := make([]byte, superblockSize)
	copy(out, buf[SuperblockOffset:SuperblockOffset+superblockSize])
	return out, true, nil
}

func readWithCacheDrop(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		return nil, fmt.Errorf("ext4: open %s: %w", path, err)
	}
	defer f.Close()

	// Best-effort: drop the bdev cache (BLKFLSBUF) and ask the
	// kernel to evict the page covering the superblock
	// (POSIX_FADV_DONTNEED). Errors are ignored — if neither works
	// we'll just return whatever the kernel has cached.
	_, _, _ = unix.Syscall(unix.SYS_IOCTL, f.Fd(), uintptr(blkflsbuf), 0)
	_ = unix.Fadvise(int(f.Fd()), 0, 4096, unix.FADV_DONTNEED)

	buf := make([]byte, superblockSize)
	if _, err := f.ReadAt(buf, SuperblockOffset); err != nil {
		return nil, fmt.Errorf("ext4: read superblock: %w", err)
	}
	return buf, nil
}

// allocAlignedBuffer returns a byte slice of length n whose backing
// data is aligned to align bytes (a power of two). Required for
// O_DIRECT reads.
func allocAlignedBuffer(n, align int) ([]byte, error) {
	raw, err := syscall.Mmap(-1, 0, n+align,
		syscall.PROT_READ|syscall.PROT_WRITE,
		syscall.MAP_PRIVATE|syscall.MAP_ANONYMOUS)
	if err != nil {
		return nil, fmt.Errorf("ext4: mmap aligned buffer: %w", err)
	}
	off := int(uintptr(unsafe.Pointer(&raw[0])) & uintptr(align-1))
	if off != 0 {
		off = align - off
	}
	return raw[off : off+n : off+n], nil
}
