package coverageattestation

import (
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/3leaps/spanwit/internal/inventory"
)

const DefaultMaxGaps = 4096

// Evidence is the bounded, reconciled input for one producer attestation. It
// comes from the same stream as stdout and never replays entries or re-walks.
type Evidence struct {
	Header  inventory.Header
	Gaps    []inventory.Gap
	Summary inventory.Summary

	admittedRoots []admittedRootIdentity
}

// admittedRootIdentity is intentionally non-serialized evidence captured at
// header time. It binds later publication to the exact directories the walker
// admitted, rather than to mutable user aliases or path strings alone.
type admittedRootIdentity struct {
	id   string
	path string
	info os.FileInfo
}

// Collector tees inventory events to the ordinary sink while retaining only
// the coverage-relevant header, affecting gaps, and terminal summary.
type Collector struct {
	downstream inventory.Sink
	maxGaps    int

	mu       sync.Mutex
	header   inventory.Header
	roots    []admittedRootIdentity
	gaps     []inventory.Gap
	summary  inventory.Summary
	overflow bool
}

func NewCollector(downstream inventory.Sink, maxGaps int) (*Collector, error) {
	if downstream == nil {
		return nil, errors.New("coverage-attestation collector requires a downstream sink")
	}
	if maxGaps < 0 {
		return nil, errors.New("coverage-attestation gap limit cannot be negative")
	}
	if maxGaps == 0 {
		maxGaps = DefaultMaxGaps
	}
	return &Collector{downstream: downstream, maxGaps: maxGaps}, nil
}

func (c *Collector) Header(header inventory.Header) error {
	identities, err := captureAdmittedRoots(header.Roots)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.header = header
	c.header.Roots = append([]inventory.Root(nil), header.Roots...)
	c.header.Exclusions = append([]inventory.Exclusion(nil), header.Exclusions...)
	c.roots = append([]admittedRootIdentity(nil), identities...)
	c.mu.Unlock()
	return c.downstream.Header(header)
}

func (c *Collector) Entry(entry inventory.Entry) error {
	return c.downstream.Entry(entry)
}

func (c *Collector) Directory(directory inventory.Directory) error {
	downstream, ok := c.downstream.(inventory.DirectorySink)
	if !ok {
		return errors.New("coverage-attestation downstream does not support directory records")
	}
	return downstream.Directory(directory)
}

func (c *Collector) Gap(gap inventory.Gap) error {
	if gap.AffectsCompleteness {
		c.mu.Lock()
		if len(c.gaps) < c.maxGaps {
			c.gaps = append(c.gaps, gap)
		} else {
			c.overflow = true
		}
		c.mu.Unlock()
	}
	return c.downstream.Gap(gap)
}

func (c *Collector) Summary(summary inventory.Summary) error {
	if err := c.downstream.Summary(summary); err != nil {
		return err
	}
	c.mu.Lock()
	c.summary = summary
	c.summary.Roots = append([]inventory.RootSummary(nil), summary.Roots...)
	c.mu.Unlock()
	return nil
}

func (c *Collector) Evidence() (Evidence, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.header.RunID == "" {
		return Evidence{}, errors.New("coverage-attestation evidence has no inventory header")
	}
	if c.summary.Lifecycle == "" {
		return Evidence{}, errors.New("coverage-attestation evidence has no terminal summary")
	}
	if c.overflow {
		return Evidence{}, fmt.Errorf(
			"coverage-attestation affecting-gap limit exceeded (%d); no attestation emitted",
			c.maxGaps)
	}
	return Evidence{
		Header: c.header, Gaps: append([]inventory.Gap(nil), c.gaps...),
		Summary: c.summary, admittedRoots: append([]admittedRootIdentity(nil), c.roots...),
	}, nil
}

func (e Evidence) admittedRootPaths() ([]string, error) {
	return admittedRootPaths(e.Header.Roots, e.admittedRoots)
}

func (e Evidence) verifyAdmittedRootIdentities() error {
	return verifyAdmittedRootIdentities(e.Header.Roots, e.admittedRoots)
}

// captureAdmittedRoots records the identity of each admitted root directory
// at header time.
func captureAdmittedRoots(roots []inventory.Root) ([]admittedRootIdentity, error) {
	identities := make([]admittedRootIdentity, 0, len(roots))
	for _, root := range roots {
		info, err := os.Lstat(root.Path)
		if err != nil {
			return nil, fmt.Errorf("capture admitted inventory root %q: %w", root.ID, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return nil, fmt.Errorf("capture admitted inventory root %q: not a real directory", root.ID)
		}
		identities = append(identities, admittedRootIdentity{
			id: root.ID, path: root.Path, info: info,
		})
	}
	return identities, nil
}

func admittedRootPaths(headerRoots []inventory.Root, admitted []admittedRootIdentity) ([]string, error) {
	if len(headerRoots) == 0 || len(admitted) == 0 {
		return nil, errors.New("coverage-attestation evidence has no admitted-root identities")
	}
	if len(headerRoots) != len(admitted) {
		return nil, errors.New("coverage-attestation evidence lacks admitted-root identities")
	}
	paths := make([]string, 0, len(admitted))
	for index, root := range headerRoots {
		identity := admitted[index]
		if identity.id != root.ID || identity.path != root.Path || identity.info == nil {
			return nil, errors.New("coverage-attestation admitted-root identity does not match header")
		}
		paths = append(paths, identity.path)
	}
	return paths, nil
}

func verifyAdmittedRootIdentities(headerRoots []inventory.Root, admitted []admittedRootIdentity) error {
	if _, err := admittedRootPaths(headerRoots, admitted); err != nil {
		return err
	}
	for _, identity := range admitted {
		info, err := os.Lstat(identity.path)
		if err != nil {
			return fmt.Errorf("verify admitted inventory root %q: %w", identity.id, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !os.SameFile(identity.info, info) {
			return fmt.Errorf("admitted inventory root %q changed before attestation publication", identity.id)
		}
		resolved, err := os.Stat(identity.path)
		if err != nil {
			return fmt.Errorf("verify admitted inventory root %q: %w", identity.id, err)
		}
		if !os.SameFile(identity.info, resolved) {
			return fmt.Errorf("admitted inventory root %q retargeted before attestation publication", identity.id)
		}
	}
	return nil
}
