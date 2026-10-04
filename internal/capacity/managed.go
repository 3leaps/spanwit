package capacity

import (
	"context"
	"errors"
	"io"
	"os"
)

type ManagedMeasurement struct {
	Bytes    int64
	Objects  int
	Present  bool
	Complete bool
	Gaps     []CoverageGap
}

const managedReadBatch = 256

// MeasureManagedRoot sums allocated blocks without following symlinks or
// reading file contents. Directory entries are consumed in fixed-size batches.
func MeasureManagedRoot(ctx context.Context, root string, maxObjects int) ManagedMeasurement {
	if maxObjects <= 0 {
		maxObjects = DefaultManagedObjects
	}
	result := ManagedMeasurement{Complete: true, Gaps: []CoverageGap{}}
	if ctx.Err() != nil {
		result.Complete = false
		result.Gaps = append(result.Gaps, deadlineGap(
			"system_managed",
			"managed-root measurement was skipped because the capacity context budget was exhausted",
		))
		return result
	}
	info, err := os.Lstat(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return result
		}
		result.Complete = false
		result.Gaps = append(result.Gaps, managedGap(err, "system_managed"))
		return result
	}
	result.Present = true
	result.Bytes += allocatedBytes(info)
	result.Objects++

	stack := []string{root}
	for len(stack) > 0 {
		if err := ctx.Err(); err != nil {
			result.Complete = false
			result.Gaps = append(result.Gaps, CoverageGap{
				Code:   "timeout",
				Scope:  "system_managed",
				Detail: "managed-root measurement did not complete before the collector deadline",
			})
			return result
		}
		current := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		dir, err := os.Open(current)
		if err != nil {
			result.Complete = false
			result.Gaps = append(result.Gaps, managedGap(err, "system_managed"))
			continue
		}
		for {
			entries, readErr := dir.ReadDir(managedReadBatch)
			for _, entry := range entries {
				if result.Objects >= maxObjects {
					_ = dir.Close()
					result.Complete = false
					result.Gaps = append(result.Gaps, CoverageGap{
						Code:   "truncated",
						Scope:  "system_managed",
						Detail: "managed-root object limit reached",
					})
					return result
				}
				childInfo, infoErr := entry.Info()
				if infoErr != nil {
					if !errors.Is(infoErr, os.ErrNotExist) {
						result.Gaps = append(result.Gaps, managedGap(infoErr, "system_managed"))
					}
					result.Complete = false
					continue
				}
				result.Objects++
				result.Bytes += allocatedBytes(childInfo)
				if childInfo.IsDir() && childInfo.Mode()&os.ModeSymlink == 0 {
					stack = append(stack, current+string(os.PathSeparator)+entry.Name())
				}
			}
			if readErr == io.EOF {
				break
			}
			if readErr != nil {
				result.Complete = false
				result.Gaps = append(result.Gaps, managedGap(readErr, "system_managed"))
				break
			}
			if err := ctx.Err(); err != nil {
				result.Complete = false
				result.Gaps = append(result.Gaps, CoverageGap{
					Code:   "timeout",
					Scope:  "system_managed",
					Detail: "managed-root measurement did not complete before the collector deadline",
				})
				break
			}
		}
		_ = dir.Close()
	}
	return result
}

func collectManagedRoots(
	ctx context.Context,
	roots []ManagedRoot,
	limit int,
	measurer ManagedRootMeasurer,
) SystemManagedPlane {
	plane := SystemManagedPlane{
		CollectionStatus: StatusMeasured,
		Source:           "filesystem.allocated_blocks",
		Coverage:         emptyCoverage(),
		Policy:           ManagedPolicy,
		Entries:          []SystemManagedEntry{},
	}
	for _, root := range roots {
		if ctx.Err() != nil {
			plane.Coverage.Gaps = mergeCoverageGaps(plane.Coverage.Gaps, []CoverageGap{
				deadlineGap(
					"system_managed",
					"managed-root measurement was skipped because the capacity context budget was exhausted",
				),
			})
			if len(plane.Entries) == 0 {
				plane.CollectionStatus = StatusUnavailable
			} else {
				plane.CollectionStatus = StatusPartial
			}
			break
		}
		measurement := measurer(ctx, root.Path, limit)
		plane.Coverage.Gaps = mergeCoverageGaps(plane.Coverage.Gaps, measurement.Gaps)
		if !measurement.Present {
			if !measurement.Complete {
				plane.CollectionStatus = StatusUnavailable
			}
			continue
		}
		claim := measuredClaim(measurement.Bytes, "allocated_blocks", "lower")
		if !measurement.Complete {
			claim = partialClaim(measurement.Bytes, "allocated_blocks", "lower")
			plane.CollectionStatus = StatusPartial
		}
		plane.Entries = append(plane.Entries, SystemManagedEntry{
			ID:          root.ID,
			Path:        root.Path,
			TrustState:  "diagnostic-only",
			Allocated:   &claim,
			Remediation: root.Remediation,
		})
	}
	if len(plane.Coverage.Gaps) > 0 && plane.CollectionStatus == StatusMeasured {
		plane.CollectionStatus = StatusPartial
	}
	return plane
}

func managedGap(err error, scope string) CoverageGap {
	code := "command_failed"
	detail := "managed-root entry could not be measured"
	if errors.Is(err, os.ErrPermission) {
		code = "permission_denied"
		detail = "managed-root entry was denied by current privileges"
	}
	one := 1
	return CoverageGap{Code: code, Scope: scope, Detail: detail, OmittedObjects: &one}
}

func mergeCoverageGaps(existing, incoming []CoverageGap) []CoverageGap {
	for _, next := range incoming {
		merged := false
		for i := range existing {
			if existing[i].Code != next.Code || existing[i].Scope != next.Scope ||
				existing[i].Detail != next.Detail {
				continue
			}
			if next.OmittedObjects != nil {
				if existing[i].OmittedObjects == nil {
					zero := 0
					existing[i].OmittedObjects = &zero
				}
				*existing[i].OmittedObjects += *next.OmittedObjects
			}
			merged = true
			break
		}
		if !merged {
			existing = append(existing, next)
		}
	}
	return existing
}
