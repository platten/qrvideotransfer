package pipeline

import "golang.org/x/sys/unix"

// platformDiskFree returns the bytes available to an unprivileged process in dir.
func platformDiskFree(dir string) (int64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return 0, err
	}
	return int64(st.Bavail) * int64(st.Bsize), nil //nolint:gosec // block counts fit
}
