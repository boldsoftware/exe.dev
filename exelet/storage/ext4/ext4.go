// Package ext4 provides a tiny, read-only reader for the ext4 superblock.
//
// It is used to report the guest's view of disk usage (used / free bytes)
// without involving the guest, by reading the ext4 primary superblock
// directly from a block device or image file. Only an O_RDONLY open
// and a single positional read of the 1024-byte superblock at offset
// 1024 are performed; no locks, no writes, no mounts. This is safe to
// do against a live zvol that is currently attached to a running VM:
// ext4 only rewrites the superblock on rare events (mount/unmount,
// allocator state flush) and a torn read just produces a slightly
// stale free-blocks count, never corruption.
//
// Note on totals: TotalBytes returns the raw block_count from the
// superblock, *not* statvfs f_blocks. ext4's statvfs subtracts
// metadata overhead (group descriptors, bitmaps, inode tables,
// journal); to a first approximation, expect TotalBytes to be a few
// percent larger than what `df` shows in the "1K-blocks" column.
// FreeBytes/AvailableBytes match statvfs f_bfree/f_bavail.
package ext4

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// Magic is the ext2/3/4 superblock magic number (s_magic).
const Magic uint16 = 0xEF53

// SuperblockOffset is the byte offset of the primary superblock in an
// ext2/3/4 filesystem image. The first 1024 bytes are reserved for the
// boot sector / partition table.
const SuperblockOffset = 1024

// superblockSize is the number of bytes we read. The full ext4
// superblock is 1024 bytes; we read it all so we can pick up the 64bit
// high-word fields at offsets 0x150-0x158.
const superblockSize = 1024

// Feature flags in s_feature_incompat we care about.
const featureIncompat64Bit uint32 = 0x80

// Usage is a snapshot of an ext4 filesystem's space accounting, as
// reported by its primary superblock.
type Usage struct {
	// BlockSize is the filesystem's logical block size in bytes
	// (1024 << s_log_block_size; typically 4096).
	BlockSize uint64
	// TotalBlocks is s_blocks_count (combined low+high if 64BIT).
	TotalBlocks uint64
	// FreeBlocks is s_free_blocks_count.
	FreeBlocks uint64
	// ReservedBlocks is s_r_blocks_count (root-reserved).
	ReservedBlocks uint64
	// TotalInodes / FreeInodes from the superblock.
	TotalInodes uint64
	FreeInodes  uint64
}

// TotalBytes is the filesystem's logical capacity (BlockSize * TotalBlocks).
func (u Usage) TotalBytes() uint64 { return u.BlockSize * u.TotalBlocks }

// FreeBytes is the number of bytes free to any user (BlockSize * FreeBlocks).
// Note this includes the root-reserved pool; AvailableBytes excludes it.
func (u Usage) FreeBytes() uint64 { return u.BlockSize * u.FreeBlocks }

// AvailableBytes mirrors statvfs's f_bavail: bytes available to a
// non-root user. Returns 0 if the reservation exceeds free space (which
// can legitimately happen on a near-full filesystem).
func (u Usage) AvailableBytes() uint64 {
	if u.FreeBlocks <= u.ReservedBlocks {
		return 0
	}
	return u.BlockSize * (u.FreeBlocks - u.ReservedBlocks)
}

// UsedBytes is the obvious complement: TotalBytes - FreeBytes.
func (u Usage) UsedBytes() uint64 {
	if u.FreeBlocks > u.TotalBlocks {
		return 0
	}
	return u.BlockSize * (u.TotalBlocks - u.FreeBlocks)
}

// ErrNotExt4 is returned when the bytes at the superblock offset don't
// have the ext4 magic number. This is the typical signal that the
// volume is empty, hosting some other filesystem, or partitioned (in
// which case the superblock lives inside a partition, not at offset
// 1024 of the disk).
var ErrNotExt4 = errors.New("ext4: superblock magic not found")

// ReadUsage opens path and parses its primary superblock. path may be
// a regular file (image), a block device, or a ZFS zvol; we never
// write to it.
//
// On block devices and zvols the host kernel caches reads in its bdev
// page cache. Guest writes to a zvol go through ZFS's own path and
// don't invalidate that cache, so a naive O_RDONLY read returns
// stale superblock bytes (we may never see new free-block counts
// for the lifetime of the host).
//
// To bypass the cache we ask the platform-specific layer to do an
// uncached read (Linux: O_DIRECT, with a sector-aligned buffer; other
// platforms: best-effort posix_fadvise). The 1024-byte primary
// superblock starts at offset 1024 and both are sector-aligned.
func ReadUsage(path string) (Usage, error) {
	buf, err := readSuperblockUncached(path)
	if err != nil {
		return Usage{}, err
	}
	return parseSuperblock(buf)
}

// ReadUsageFrom is the testable seam: it parses a superblock from any
// io.ReaderAt without owning the file handle.
func ReadUsageFrom(r io.ReaderAt) (Usage, error) {
	var buf [superblockSize]byte
	if _, err := r.ReadAt(buf[:], SuperblockOffset); err != nil {
		return Usage{}, fmt.Errorf("ext4: read superblock: %w", err)
	}
	return parseSuperblock(buf[:])
}

// parseSuperblock decodes the fields we care about. Field offsets and
// semantics are taken from include/uapi/linux/ext4.h.
func parseSuperblock(b []byte) (Usage, error) {
	if len(b) < superblockSize {
		return Usage{}, fmt.Errorf("ext4: short superblock buffer (%d bytes)", len(b))
	}

	le := binary.LittleEndian
	magic := le.Uint16(b[0x38:])
	if magic != Magic {
		return Usage{}, fmt.Errorf("%w (got 0x%04x)", ErrNotExt4, magic)
	}

	inodesCount := uint64(le.Uint32(b[0x00:]))
	blocksCountLo := uint64(le.Uint32(b[0x04:]))
	rBlocksCountLo := uint64(le.Uint32(b[0x08:]))
	freeBlocksLo := uint64(le.Uint32(b[0x0c:]))
	freeInodes := uint64(le.Uint32(b[0x10:]))
	logBlockSize := le.Uint32(b[0x18:])
	featureIncompat := le.Uint32(b[0x60:])

	var blocksCountHi, rBlocksCountHi, freeBlocksHi uint64
	if featureIncompat&featureIncompat64Bit != 0 {
		blocksCountHi = uint64(le.Uint32(b[0x150:]))
		rBlocksCountHi = uint64(le.Uint32(b[0x154:]))
		freeBlocksHi = uint64(le.Uint32(b[0x158:]))
	}

	// 1024 << s_log_block_size. Linux ext4 caps this at 6 (64 KiB block).
	// We allow a tiny bit of slack but reject values that would overflow
	// or describe an impossible filesystem.
	const maxLogBlockSize = 6
	if logBlockSize > maxLogBlockSize {
		return Usage{}, fmt.Errorf("ext4: implausible s_log_block_size %d", logBlockSize)
	}
	blockSize := uint64(1024) << logBlockSize

	totalBlocks := blocksCountLo | (blocksCountHi << 32)
	freeBlocks := freeBlocksLo | (freeBlocksHi << 32)
	reservedBlocks := rBlocksCountLo | (rBlocksCountHi << 32)

	// Reject implausibly large block counts so a torn 64-bit splice
	// (high word from a previous write, low word from a newer one)
	// can't blow up BlockSize*TotalBlocks. 2^52 4 KiB blocks is 16 EiB
	// — well past anything we'll see on a zvol.
	const maxBlocks = uint64(1) << 52
	if totalBlocks > maxBlocks {
		return Usage{}, fmt.Errorf("%w: implausible block count %d", ErrNotExt4, totalBlocks)
	}

	// Sanity: a free or reserved count exceeding total usually means a
	// torn read or a non-ext4 image that happened to have 0xEF53 in the
	// magic slot. Surface it as ErrNotExt4 so callers can ignore it.
	if freeBlocks > totalBlocks || reservedBlocks > totalBlocks {
		return Usage{}, fmt.Errorf("%w: inconsistent block counts (total=%d free=%d reserved=%d)",
			ErrNotExt4, totalBlocks, freeBlocks, reservedBlocks)
	}

	return Usage{
		BlockSize:      blockSize,
		TotalBlocks:    totalBlocks,
		FreeBlocks:     freeBlocks,
		ReservedBlocks: reservedBlocks,
		TotalInodes:    inodesCount,
		FreeInodes:     freeInodes,
	}, nil
}
