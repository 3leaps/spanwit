//go:build !unix && !windows

package capacity

import "fmt"

func sampleFilesystem(path string) (FilesystemSample, error) {
	return FilesystemSample{}, fmt.Errorf("filesystem capacity is not supported on this platform for %s", path)
}
