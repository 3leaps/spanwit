package capacity

import (
	"context"
	"fmt"
)

// SampleFilesystem performs the platform-native filesystem observation.
func SampleFilesystem(path string) (FilesystemSample, error) {
	return SampleFilesystemContext(context.Background(), path)
}

// SampleFilesystemContext performs the platform-native filesystem observation
// after checking the caller budget. Native statfs-style syscalls are not
// interruptible on every supported platform, so the context is checked both
// before and after the syscall and provides a best-effort wall-clock boundary.
func SampleFilesystemContext(ctx context.Context, path string) (FilesystemSample, error) {
	if err := ctx.Err(); err != nil {
		return FilesystemSample{}, err
	}
	sample, err := sampleFilesystem(path)
	if err != nil {
		return FilesystemSample{}, err
	}
	if err := ctx.Err(); err != nil {
		return FilesystemSample{}, err
	}
	if sample.Total < 0 || sample.Used < 0 || sample.Available < 0 {
		return FilesystemSample{}, fmt.Errorf("filesystem sample %s returned negative capacity", path)
	}
	return sample, nil
}
