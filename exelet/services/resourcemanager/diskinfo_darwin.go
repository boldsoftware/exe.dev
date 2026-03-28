package resourcemanager

import "golang.org/x/sys/unix"

// statfsBlockSize returns the fundamental filesystem block size.
// On Darwin, Statfs_t has no Frsize field; Bsize is already the
// fundamental block size.
func statfsBlockSize(fs *unix.Statfs_t) int64 {
	return int64(fs.Bsize)
}
