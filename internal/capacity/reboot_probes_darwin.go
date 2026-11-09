//go:build darwin

package capacity

import (
	"context"

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

func statDevice(path string) (uint64, error) {
	var st unix.Stat_t
	if err := unix.Stat(path, &st); err != nil {
		return 0, err
	}
	return uint64(st.Dev), nil // #nosec G115 -- st_dev is an identity compared for equality only.
}

// darwinMountTable reads every mounted filesystem (source node and mount
// point) and stats each mount point. A stat failure is kept as StatOK=false
// so callers can treat that mount as unknown rather than absent.
func darwinMountTable() ([]MountEntry, error) {
	n, err := unix.Getfsstat(nil, unix.MNT_NOWAIT)
	if err != nil {
		return nil, err
	}
	buf := make([]unix.Statfs_t, n)
	n, err = unix.Getfsstat(buf, unix.MNT_NOWAIT)
	if err != nil {
		return nil, err
	}
	out := make([]MountEntry, 0, n)
	for _, fs := range buf[:n] {
		m := MountEntry{
			From: unix.ByteSliceToString(fs.Mntfromname[:]),
			On:   unix.ByteSliceToString(fs.Mntonname[:]),
		}
		if dev, err := statDevice(m.On); err == nil {
			m.Dev, m.StatOK = dev, true
		}
		out = append(out, m)
	}
	return out, nil
}

func defaultSwapUsage() SwapUsageFunc   { return darwinSwapUsage }
func defaultDeviceOf() DeviceOfFunc     { return statDevice }
func defaultMountTable() MountTableFunc { return darwinMountTable }
