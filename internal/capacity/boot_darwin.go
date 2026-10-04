//go:build darwin

package capacity

import (
	"context"
	"fmt"
	"time"

	"golang.org/x/sys/unix"
)

const darwinBootObservationPath = "/private/var/run/utmpx"

// defaultBootMetadata uses the birth time of Darwin's per-boot utmpx file.
// The observation contains no hostname or machine identifier, remains stable
// within one boot, and changes when launchd recreates the file after reboot.
func defaultBootMetadata(ctx context.Context) BootMetadata {
	if err := ctx.Err(); err != nil {
		return BootMetadata{}
	}
	var stat unix.Stat_t
	if err := unix.Stat(darwinBootObservationPath, &stat); err != nil {
		return BootMetadata{}
	}
	seconds := stat.Btim.Sec
	nanoseconds := stat.Btim.Nsec
	if seconds <= 0 || nanoseconds < 0 || nanoseconds >= int64(time.Second) {
		return BootMetadata{}
	}
	return BootMetadata{
		ID:   fmt.Sprintf("darwin-utmpx:%d:%09d", seconds, nanoseconds),
		Time: time.Unix(seconds, nanoseconds).UTC(),
	}
}
