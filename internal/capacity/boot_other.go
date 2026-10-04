//go:build !darwin

package capacity

import "context"

func defaultBootMetadata(context.Context) BootMetadata {
	return BootMetadata{}
}
