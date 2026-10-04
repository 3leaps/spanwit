//go:build !unix

package coverageattestation

import "os"

func syncPinnedDirectory(*os.Root) error {
	return nil
}
