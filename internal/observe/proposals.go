package observe

// BuildProposals emits dry-run-safe guidance from volume runtime (no delete).
//
// First-slice rule: pressure/growth alone is not catalog evidence. Emit only
// generic "run spanwit space" notes unless the caller later supplies catalog-
// backed recipe evidence (out of scope for this slice).
func BuildProposals(vols []*VolumeRuntime, growth map[VolumeID]GrowthResult) []Proposal {
	out := make([]Proposal, 0)
	for _, v := range vols {
		if v == nil {
			continue
		}
		level := v.Level
		if level != LevelWarn && level != LevelCritical {
			continue
		}
		ev := []string{
			"volume_id=" + string(v.Volume.ID),
			"level=" + level,
			"coverage=" + v.Coverage,
			"growth=" + growthStatus(growth, v.Volume.ID),
		}
		// Generic diagnosis note only — no cargo/go recipe ids without catalog evidence.
		out = append(out, Proposal{
			ID:       "propose-space-sense-" + string(v.Volume.ID),
			Kind:     "note",
			Title:    "Pressure elevated; run spanwit space for diagnosis (no auto-delete)",
			Evidence: ev,
		})
		if v.ReconcileNeeded {
			out = append(out, Proposal{
				ID:       "propose-reconcile-" + string(v.Volume.ID),
				Kind:     "note",
				Title:    "Observation degraded; full reconcile required before trusting growth/runway",
				Evidence: append(ev, "loss_reason="+BoundString(v.LossReason, MaxStateStringLen)),
			})
		}
	}
	return out
}

func growthStatus(g map[VolumeID]GrowthResult, id VolumeID) string {
	if g == nil {
		return "unavailable"
	}
	r, ok := g[id]
	if !ok {
		return "unavailable"
	}
	return r.Status
}
