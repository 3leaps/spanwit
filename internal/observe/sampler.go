package observe

import (
	"context"
	"fmt"
	"sync"

	"github.com/3leaps/spanwit/internal/capacity"
)

// Sampler obtains a filesystem capacity sample for a path.
type Sampler interface {
	Sample(ctx context.Context, path string) (Sample, error)
}

// CapacitySampler uses the shared capacity collector (statfs-class).
type CapacitySampler struct{}

func (CapacitySampler) Sample(ctx context.Context, path string) (Sample, error) {
	fs, err := capacity.SampleFilesystemContext(ctx, path)
	if err != nil {
		return Sample{}, err
	}
	if fs.VolumeID == "" {
		return Sample{}, fmt.Errorf("filesystem sample for %s missing volume identity", path)
	}
	var usedPct float64
	if fs.Total > 0 {
		usedPct = float64(fs.Used) / float64(fs.Total) * 100
	}
	return Sample{
		VolumeID:     VolumeID(fs.VolumeID),
		MountPath:    fs.Mount,
		Basis:        BasisStatfs,
		AvailBytes:   fs.Available,
		TotalBytes:   fs.Total,
		UsedBytes:    fs.Used,
		UsedPercent:  usedPct,
		Level:        pressureLevel(fs.Available, usedPct),
		Coverage:     CoverageComplete,
		Source:       "capacity.SampleFilesystem",
		SizeComplete: true,
	}, nil
}

// pressureLevel mirrors space diagnostics thresholds.
func pressureLevel(avail int64, usedPct float64) string {
	const (
		gi          = int64(1024 * 1024 * 1024)
		critFree    = 10 * gi
		warnFree    = 25 * gi
		critPercent = 95.0
		warnPercent = 85.0
	)
	if avail < critFree || usedPct >= critPercent {
		return LevelCritical
	}
	if avail < warnFree || usedPct >= warnPercent {
		return LevelWarn
	}
	return LevelOK
}

// FakeSampler returns scripted samples for tests (path → queue of samples).
type FakeSampler struct {
	ByPath map[string][]Sample
	Err    error
	mu     sync.Mutex
}

func (f *FakeSampler) Sample(_ context.Context, path string) (Sample, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Err != nil {
		return Sample{}, f.Err
	}
	q := f.ByPath[path]
	if len(q) == 0 {
		return Sample{}, fmt.Errorf("fake sampler: no samples for %s", path)
	}
	s := q[0]
	f.ByPath[path] = q[1:]
	return s, nil
}
