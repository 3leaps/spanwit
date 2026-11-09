//go:build !darwin

package capacity

import "context"

// Off darwin there is no releasable swap store and no held-open plane, so
// the probes are never consulted; they fail closed if they are.
func defaultSwapUsage() SwapUsageFunc {
	return func(context.Context) (int64, error) { return 0, errSwapUsageUnsupported }
}
func defaultMountTable() MountTableFunc { return nil }
