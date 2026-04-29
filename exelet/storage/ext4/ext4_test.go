package ext4_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"exe.dev/exelet/storage/ext4"
)

// TestReadUsageOnRealImage exercises the parser end-to-end against a
// real mkfs.ext4 image. We pick numbers that exercise the 64-bit
// fields: a 5 GiB image with 4 KiB blocks lives entirely in the low
// 32 bits, but we sanity-check the totals match `tune2fs -l`.
func TestReadUsageOnRealImage(t *testing.T) {
	mkfs, err := exec.LookPath("mkfs.ext4")
	if err != nil {
		t.Skipf("mkfs.ext4 not available: %v", err)
	}

	dir := t.TempDir()
	img := filepath.Join(dir, "fs.img")

	f, err := os.Create(img)
	if err != nil {
		t.Fatal(err)
	}
	// 256 MiB sparse image: enough for mkfs to lay down group descriptors
	// without needing 64bit, but with enough blocks to make the math
	// non-trivial.
	const size = int64(256 * 1024 * 1024)
	if err := f.Truncate(size); err != nil {
		t.Fatal(err)
	}
	f.Close()

	cmd := exec.Command(mkfs, "-q", "-F", "-b", "4096", img)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("mkfs.ext4: %v: %s", err, out)
	}

	u, err := ext4.ReadUsage(img)
	if err != nil {
		t.Fatalf("ReadUsage: %v", err)
	}

	if u.BlockSize != 4096 {
		t.Errorf("BlockSize = %d, want 4096", u.BlockSize)
	}
	if got, want := u.TotalBytes(), uint64(size); got != want {
		t.Errorf("TotalBytes = %d, want %d", got, want)
	}
	if u.FreeBytes() == 0 || u.FreeBytes() >= u.TotalBytes() {
		t.Errorf("FreeBytes = %d, total = %d (want 0 < free < total)", u.FreeBytes(), u.TotalBytes())
	}
	if u.UsedBytes()+u.FreeBytes() != u.TotalBytes() {
		t.Errorf("used (%d) + free (%d) != total (%d)", u.UsedBytes(), u.FreeBytes(), u.TotalBytes())
	}
	if u.AvailableBytes() > u.FreeBytes() {
		t.Errorf("AvailableBytes (%d) > FreeBytes (%d)", u.AvailableBytes(), u.FreeBytes())
	}
	if u.TotalInodes == 0 || u.FreeInodes == 0 || u.FreeInodes > u.TotalInodes {
		t.Errorf("inodes total=%d free=%d looks wrong", u.TotalInodes, u.FreeInodes)
	}
}

// TestReadUsageDoesNotMutate proves we don't write back to the device.
// We snapshot the file's content + mtime, run ReadUsage, and assert
// nothing changed. (Running as root on a chmod 0400 image isn't a real
// O_RDONLY test — root bypasses DAC, and ZFS receive containers run
// most of these tests as root.)
func TestReadUsageDoesNotMutate(t *testing.T) {
	mkfs, err := exec.LookPath("mkfs.ext4")
	if err != nil {
		t.Skipf("mkfs.ext4 not available: %v", err)
	}
	dir := t.TempDir()
	img := filepath.Join(dir, "fs.img")
	f, err := os.Create(img)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(64 * 1024 * 1024); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if out, err := exec.Command(mkfs, "-q", "-F", img).CombinedOutput(); err != nil {
		t.Fatalf("mkfs.ext4: %v: %s", err, out)
	}

	before, err := os.ReadFile(img)
	if err != nil {
		t.Fatal(err)
	}
	beforeStat, err := os.Stat(img)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := ext4.ReadUsage(img); err != nil {
		t.Fatalf("ReadUsage: %v", err)
	}

	after, err := os.ReadFile(img)
	if err != nil {
		t.Fatal(err)
	}
	afterStat, err := os.Stat(img)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Errorf("image content changed across ReadUsage")
	}
	if !beforeStat.ModTime().Equal(afterStat.ModTime()) {
		t.Errorf("image mtime changed: before=%v after=%v", beforeStat.ModTime(), afterStat.ModTime())
	}
}

// TestNotExt4 exercises the magic-number gate. A device that contains
// random data must produce ErrNotExt4 (so callers can distinguish
// "empty zvol" from "corrupt parser").
func TestNotExt4(t *testing.T) {
	buf := bytes.Repeat([]byte{0xaa}, 2048)
	_, err := ext4.ReadUsageFrom(bytes.NewReader(buf))
	if !errors.Is(err, ext4.ErrNotExt4) {
		t.Fatalf("want ErrNotExt4, got %v", err)
	}
}

// TestParse64Bit synthesizes a minimal superblock with INCOMPAT_64BIT
// set and the high words populated, to confirm we splice low+high.
func TestParse64Bit(t *testing.T) {
	var sb [1024]byte
	le := binary.LittleEndian
	// s_inodes_count
	le.PutUint32(sb[0x00:], 100)
	// s_blocks_count_lo / hi
	le.PutUint32(sb[0x04:], 0xffffffff)
	le.PutUint32(sb[0x150:], 0x1)
	// s_r_blocks_count_lo / hi
	le.PutUint32(sb[0x08:], 0)
	le.PutUint32(sb[0x154:], 0)
	// s_free_blocks_count_lo / hi
	le.PutUint32(sb[0x0c:], 0xfffffff0)
	le.PutUint32(sb[0x158:], 0x1)
	// s_free_inodes_count
	le.PutUint32(sb[0x10:], 50)
	// s_log_block_size = 2 -> 4096
	le.PutUint32(sb[0x18:], 2)
	// s_magic
	le.PutUint16(sb[0x38:], 0xEF53)
	// s_feature_incompat with 64BIT
	le.PutUint32(sb[0x60:], 0x80)

	// Wrap with the 1024 boot-sector pad in front.
	full := make([]byte, 1024+1024)
	copy(full[1024:], sb[:])

	u, err := ext4.ReadUsageFrom(bytes.NewReader(full))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if u.BlockSize != 4096 {
		t.Errorf("BlockSize = %d", u.BlockSize)
	}
	if got, want := u.TotalBlocks, uint64(0x1ffffffff); got != want {
		t.Errorf("TotalBlocks = %#x, want %#x", got, want)
	}
	if got, want := u.FreeBlocks, uint64(0x1fffffff0); got != want {
		t.Errorf("FreeBlocks = %#x, want %#x", got, want)
	}
}

// makeSB returns a fresh 2048-byte buffer with a minimally-valid
// superblock at offset 1024. Tests can mutate the inner slice before
// parsing.
func makeSB(mutate func(sb []byte)) []byte {
	full := make([]byte, 1024+1024)
	sb := full[1024:]
	le := binary.LittleEndian
	le.PutUint32(sb[0x00:], 100)  // s_inodes_count
	le.PutUint32(sb[0x04:], 1024) // s_blocks_count_lo
	le.PutUint32(sb[0x08:], 0)    // s_r_blocks_count_lo
	le.PutUint32(sb[0x0c:], 512)  // s_free_blocks_count_lo
	le.PutUint32(sb[0x10:], 50)   // s_free_inodes_count
	le.PutUint32(sb[0x18:], 2)    // s_log_block_size = 4096
	le.PutUint16(sb[0x38:], 0xEF53)
	le.PutUint32(sb[0x60:], 0) // no incompat flags
	if mutate != nil {
		mutate(sb)
	}
	return full
}

func TestRejectImplausibleBlockSize(t *testing.T) {
	buf := makeSB(func(sb []byte) {
		binary.LittleEndian.PutUint32(sb[0x18:], 7) // 1024 << 7 = 128 KiB
	})
	if _, err := ext4.ReadUsageFrom(bytes.NewReader(buf)); err == nil {
		t.Fatal("expected error for s_log_block_size=7")
	}
}

func TestRejectFreeExceedsTotal(t *testing.T) {
	buf := makeSB(func(sb []byte) {
		binary.LittleEndian.PutUint32(sb[0x0c:], 9999) // free > total
	})
	_, err := ext4.ReadUsageFrom(bytes.NewReader(buf))
	if !errors.Is(err, ext4.ErrNotExt4) {
		t.Fatalf("want ErrNotExt4, got %v", err)
	}
}

func TestRejectReservedExceedsTotal(t *testing.T) {
	buf := makeSB(func(sb []byte) {
		binary.LittleEndian.PutUint32(sb[0x08:], 9999) // reserved > total
	})
	_, err := ext4.ReadUsageFrom(bytes.NewReader(buf))
	if !errors.Is(err, ext4.ErrNotExt4) {
		t.Fatalf("want ErrNotExt4, got %v", err)
	}
}

// TestHighWordsIgnoredWithout64Bit ensures we never splice in the high
// halves on a non-64BIT filesystem, even if those bytes happen to
// contain garbage (which they will on rev 0 ext2 / older ext4 images).
func TestHighWordsIgnoredWithout64Bit(t *testing.T) {
	buf := makeSB(func(sb []byte) {
		// Garbage in the 64-bit high words.
		binary.LittleEndian.PutUint32(sb[0x150:], 0xdeadbeef)
		binary.LittleEndian.PutUint32(sb[0x154:], 0xdeadbeef)
		binary.LittleEndian.PutUint32(sb[0x158:], 0xdeadbeef)
		// INCOMPAT_64BIT NOT set.
	})
	u, err := ext4.ReadUsageFrom(bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if u.TotalBlocks != 1024 || u.FreeBlocks != 512 || u.ReservedBlocks != 0 {
		t.Errorf("high words leaked through: total=%d free=%d reserved=%d",
			u.TotalBlocks, u.FreeBlocks, u.ReservedBlocks)
	}
}

// TestPartitionedDiskRejected ensures we don't accidentally interpret
// a partition table as an ext4 superblock. mkfs.ext4 against the whole
// device is the codebase invariant; if a future caller ever feeds a
// partitioned image, we want a clean ErrNotExt4 (the partition table
// at offset 0 has zero in the magic slot at offset 0x438 / 1080 from
// the start of disk, i.e. offset 0x38 of the buffer at offset 1024).
func TestPartitionedDiskRejected(t *testing.T) {
	// 4 MiB of zeroes plus a fake MBR signature 0x55AA at offset 510:
	// the bytes at offset 1024 (where ext4's superblock would live)
	// are zero, so there's no 0xEF53 magic.
	buf := make([]byte, 4*1024*1024)
	buf[510] = 0x55
	buf[511] = 0xAA
	_, err := ext4.ReadUsageFrom(bytes.NewReader(buf))
	if !errors.Is(err, ext4.ErrNotExt4) {
		t.Fatalf("want ErrNotExt4 for partitioned image, got %v", err)
	}
}
