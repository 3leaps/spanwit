package space

import (
	"github.com/3leaps/spanwit/internal/capacity"
	"github.com/3leaps/spanwit/internal/engine"
)

func diskPressure(path string) (Pressure, error) {
	sample, err := capacity.SampleFilesystem(path)
	if err != nil {
		return Pressure{}, err
	}
	return pressureFromSample(sample), nil
}

func pressureFromSample(sample capacity.FilesystemSample) Pressure {
	var usedPct float64
	if sample.Total > 0 {
		usedPct = float64(sample.Used) / float64(sample.Total) * 100
	}
	return Pressure{
		Path:        sample.Path,
		Mount:       sample.Mount,
		VolumeID:    sample.VolumeID,
		TotalBytes:  sample.Total,
		TotalHuman:  engine.HumanSize(sample.Total),
		UsedBytes:   sample.Used,
		UsedHuman:   engine.HumanSize(sample.Used),
		AvailBytes:  sample.Available,
		AvailHuman:  engine.HumanSize(sample.Available),
		UsedPercent: usedPct,
		Level:       pressureLevel(sample.Available, usedPct),
	}
}
