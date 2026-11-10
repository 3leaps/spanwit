//go:build darwin

package capacity

import (
	"context"
	"errors"

	"golang.org/x/sys/unix"
)

// darwinSwapUsage reads sysctl vm.swapusage (struct xsw_usage) and returns
// xsu_total: the bytes currently reserved by swap files. A short buffer or a
// value that does not fit int64 is an error, never a clamped figure.
func darwinSwapUsage(ctx context.Context) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	raw, err := unix.SysctlRaw("vm.swapusage")
	if err != nil {
		return 0, err
	}
	return parseXswUsageTotal(raw)
}

// mountTableSlack is spare capacity for mounts that appear between the
// counting call and the filling call. A full buffer means the table may be
// incomplete, so it is reported as an error rather than read as complete.
const mountTableSlack = 16

// darwinMountTable reads every mounted filesystem with MNT_NOWAIT (cached
// statistics: never blocks on a network mount). Each row's device is its
// f_fsid, taken from the same row as its source and mount point.
func darwinMountTable() ([]MountEntry, error) {
	n, err := unix.Getfsstat(nil, unix.MNT_NOWAIT)
	if err != nil {
		return nil, err
	}
	buf := make([]unix.Statfs_t, n+mountTableSlack)
	n, err = unix.Getfsstat(buf, unix.MNT_NOWAIT)
	if err != nil {
		return nil, err
	}
	if n >= len(buf) {
		return nil, errors.New("mount table grew while it was read")
	}
	out := make([]MountEntry, 0, n)
	for _, fs := range buf[:n] {
		out = append(out, MountEntry{
			From: unix.ByteSliceToString(fs.Mntfromname[:]),
			On:   unix.ByteSliceToString(fs.Mntonname[:]),
			Dev:  uint64(uint32(fs.Fsid.Val[0])), // #nosec G115 -- fsid.val[0] is the dev_t bit pattern; compared for equality only.
		})
	}
	return out, nil
}

func defaultSwapUsage() SwapUsageFunc   { return darwinSwapUsage }
func defaultMountTable() MountTableFunc { return darwinMountTable }
