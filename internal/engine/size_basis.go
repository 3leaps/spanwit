package engine

import "fmt"

// Size bases. A plan reports every row on one basis: allocated wherever the
// build can read st_blocks, apparent (logical st_size) otherwise.
const (
	SizeBasisAllocated = "allocated"
	SizeBasisApparent  = "apparent"
)

// allocatedBytesOf reads an entry's allocation; a variable so tests can
// simulate a filesystem that does not report it.
var allocatedBytesOf = allocatedBytes

// DefaultSizeBasis is the basis a fresh plan uses on this build.
func DefaultSizeBasis() string {
	if AllocatedSizesSupported {
		return SizeBasisAllocated
	}
	return SizeBasisApparent
}

// sizePolicy separates the basis rows are reported on (headline) from the
// basis min_size is decided on (gate). They differ only when a plan asks for
// allocated on a build that cannot read allocation: rows then all report
// apparent (never mixed within one plan) and min_size, which the operator
// reviewed on allocated bytes, is undecidable rather than silently moved to
// the logical figure.
type sizePolicy struct {
	headline string
	gate     string
}

func resolveSizePolicy(requested string) (sizePolicy, error) {
	return resolveSizePolicyFor(requested, AllocatedSizesSupported)
}

// resolveSizePolicyFor is resolveSizePolicy for a build that can (or
// cannot) read allocation; split out so both builds' rules are testable.
func resolveSizePolicyFor(requested string, supported bool) (sizePolicy, error) {
	switch requested {
	case "":
		b := SizeBasisApparent
		if supported {
			b = SizeBasisAllocated
		}
		return sizePolicy{headline: b, gate: b}, nil
	case SizeBasisApparent:
		return sizePolicy{headline: SizeBasisApparent, gate: SizeBasisApparent}, nil
	case SizeBasisAllocated:
		if supported {
			return sizePolicy{headline: SizeBasisAllocated, gate: SizeBasisAllocated}, nil
		}
		return sizePolicy{headline: SizeBasisApparent, gate: SizeBasisAllocated}, nil
	default:
		return sizePolicy{}, fmt.Errorf("invalid size basis %q (want allocated|apparent)", requested)
	}
}

// onBasis returns a measurement's sum on basis and whether it is a lower
// bound. Allocated is a lower bound when the walk stopped early or any
// file's allocation was unreadable (always, on a build without st_blocks).
func onBasis(sized SizeResult, basis string) (int64, bool) {
	if basis == SizeBasisAllocated {
		return sized.AllocatedBytes, sized.Incomplete || sized.AllocatedUnmeasured
	}
	return sized.Bytes, sized.Incomplete
}

// logicalNoteRatio is how far (as a fraction of the larger figure) the
// logical sum must sit from an allocated headline before text shows it.
const logicalNoteRatio = 0.10

// LogicalNote returns a text suffix such as "  (125.0G logical)" when an
// allocated headline and its logical sum differ by more than 10% of the
// larger figure, and "" otherwise. JSON always carries both; text keeps the
// allocated figure as the headline and never leads with the logical one.
func LogicalNote(basis string, headline int64, logical *int64, incomplete bool) string {
	if basis != SizeBasisAllocated || logical == nil {
		return ""
	}
	a, l := float64(headline), float64(*logical)
	larger := max(a, l)
	if larger == 0 {
		return ""
	}
	diff := a - l
	if diff < 0 {
		diff = -diff
	}
	if diff/larger <= logicalNoteRatio {
		return ""
	}
	bound := ""
	if incomplete {
		bound = "≥"
	}
	return fmt.Sprintf("  (%s%s logical)", bound, HumanSize(*logical))
}
