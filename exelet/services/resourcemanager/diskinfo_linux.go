package resourcemanager

import "golang.org/x/sys/unix"

// statfsBlockSize returns the fundamental filesystem block size.
// On Linux this is Frsize (fragment size), which may differ from Bsize
// (preferred I/O block size) on filesystems like ZFS.
func statfsBlockSize(fs *unix.Statfs_t) int64 {
	return fs.Frsize
}
