package coverageattestation

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// PreflightDestination resolves an explicit real-file destination without
// creating it. The destination must be outside every enumerated patient root;
// parent directories are never created implicitly.
func PreflightDestination(raw string, patientRoots []string) (string, error) {
	prepared, err := prepareDestination(raw, patientRoots, false)
	if err != nil {
		return "", err
	}
	return prepared.destination, nil
}

// preflightCanonicalDestination performs the definitive containment check for
// publication. Unlike the early CLI preflight, admitted roots are already the
// canonical paths recorded in the inventory header; resolving them again would
// let a subsequently retargeted user-facing symlink change the protected scope.
func preflightCanonicalDestination(raw string, admittedRoots []string) (string, error) {
	prepared, err := prepareDestination(raw, admittedRoots, true)
	if err != nil {
		return "", err
	}
	return prepared.destination, nil
}

type preparedDestination struct {
	destination string
	parentPath  string
	name        string
	parentInfo  os.FileInfo
}

func prepareDestination(raw string, patientRoots []string, rootsCanonical bool) (preparedDestination, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return preparedDestination{}, errors.New("coverage-attestation destination is required")
	}
	if raw == "-" {
		return preparedDestination{}, errors.New("coverage-attestation destination must be a real file, not stdout")
	}
	absolute, err := filepath.Abs(raw)
	if err != nil {
		return preparedDestination{}, fmt.Errorf("resolve coverage-attestation destination: %w", err)
	}
	absolute = filepath.Clean(absolute)
	if absolute == string(filepath.Separator) {
		return preparedDestination{}, errors.New("coverage-attestation destination cannot be a filesystem root")
	}
	if _, err := os.Lstat(absolute); err == nil {
		return preparedDestination{}, fmt.Errorf("coverage-attestation destination already exists: %s", absolute)
	} else if !errors.Is(err, os.ErrNotExist) {
		return preparedDestination{}, fmt.Errorf("inspect coverage-attestation destination: %w", err)
	}

	parent := filepath.Dir(absolute)
	resolvedParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return preparedDestination{}, fmt.Errorf("resolve coverage-attestation parent: %w", err)
	}
	parentInfo, err := os.Stat(resolvedParent)
	if err != nil {
		return preparedDestination{}, fmt.Errorf("inspect coverage-attestation parent: %w", err)
	}
	if !parentInfo.IsDir() {
		return preparedDestination{}, fmt.Errorf("coverage-attestation parent is not a directory: %s", resolvedParent)
	}
	destination := filepath.Join(resolvedParent, filepath.Base(absolute))
	if _, err := os.Lstat(destination); err == nil {
		return preparedDestination{}, fmt.Errorf("coverage-attestation destination already exists: %s", destination)
	} else if !errors.Is(err, os.ErrNotExist) {
		return preparedDestination{}, fmt.Errorf("inspect coverage-attestation destination: %w", err)
	}

	for _, rawRoot := range patientRoots {
		var root string
		if rootsCanonical {
			root, err = canonicalAdmittedRoot(rawRoot)
		} else {
			root, err = canonicalExistingPath(rawRoot)
		}
		if err != nil {
			return preparedDestination{}, fmt.Errorf("resolve patient root for coverage-attestation preflight: %w", err)
		}
		if pathContains(root, destination) {
			return preparedDestination{}, fmt.Errorf(
				"coverage-attestation destination must be outside enumerated root %s",
				root)
		}
	}
	return preparedDestination{
		destination: destination, parentPath: resolvedParent,
		name: filepath.Base(destination), parentInfo: parentInfo,
	}, nil
}

// WriteDocument validates the entire document before creating any file, then
// publishes it with no-overwrite semantics. Evidence must come from Collector:
// its non-serialized root identities bind this publication to the exact
// canonical directories admitted at inventory-header time.
func WriteDocument(destination string, document Document, evidence Evidence) error {
	return writeDocument(destination, document, evidence, publicationHooks{})
}

// WriteAuditDocument publishes a directory-audit attestation under the same
// destination, identity and durability guards as WriteDocument.
func WriteAuditDocument(destination string, document Document, evidence AuditEvidence) error {
	return writeDocument(destination, document, evidence, publicationHooks{})
}

// publishableEvidence is the root-identity binding that publication needs
// from either evidence profile.
type publishableEvidence interface {
	admittedRootPaths() ([]string, error)
	verifyAdmittedRootIdentities() error
}

type publicationHooks struct {
	beforeStage    func()
	afterStageSync func(stage string)
	beforeLink     func(stage string)
	afterLink      func()
}

func writeDocument(
	destination string,
	document Document,
	evidence publishableEvidence,
	hooks publicationHooks,
) error {
	payload, err := MarshalValidated(document)
	if err != nil {
		return err
	}
	admittedRoots, err := evidence.admittedRootPaths()
	if err != nil {
		return err
	}
	prepared, err := prepareDestination(destination, admittedRoots, true)
	if err != nil {
		return err
	}
	parent, err := pinParent(prepared)
	if err != nil {
		return err
	}
	defer func() { _ = parent.root.Close() }()
	return publishNew(parent, payload, admittedRoots, evidence.verifyAdmittedRootIdentities, hooks)
}

type pinnedParent struct {
	root     *os.Root
	path     string
	name     string
	identity os.FileInfo
}

func pinParent(prepared preparedDestination) (*pinnedParent, error) {
	root, err := os.OpenRoot(prepared.parentPath)
	if err != nil {
		return nil, fmt.Errorf("pin coverage-attestation parent: %w", err)
	}
	identity, err := root.Stat(".")
	if err != nil {
		_ = root.Close()
		return nil, fmt.Errorf("inspect pinned coverage-attestation parent: %w", err)
	}
	if !os.SameFile(prepared.parentInfo, identity) {
		_ = root.Close()
		return nil, errors.New("coverage-attestation parent changed during preflight")
	}
	return &pinnedParent{
		root: root, path: prepared.parentPath, name: prepared.name, identity: identity,
	}, nil
}

func (p *pinnedParent) validatePublicPath() error {
	public, err := os.Lstat(p.path)
	if err != nil {
		return fmt.Errorf("inspect coverage-attestation parent: %w", err)
	}
	if !public.IsDir() || !os.SameFile(p.identity, public) {
		return errors.New("coverage-attestation parent pathname no longer names the pinned directory")
	}
	return nil
}

func (p *pinnedParent) verifyStage(name string, expected os.FileInfo) error {
	actual, err := p.root.Lstat(name)
	if err != nil {
		return fmt.Errorf("inspect pinned coverage-attestation stage: %w", err)
	}
	if !expected.Mode().IsRegular() || !actual.Mode().IsRegular() || !os.SameFile(expected, actual) {
		return errors.New("coverage-attestation stage identity changed")
	}
	return nil
}

func (p *pinnedParent) removeStageIfExpected(name string, expected os.FileInfo) error {
	actual, err := p.root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect coverage-attestation stage for cleanup: %w", err)
	}
	if !expected.Mode().IsRegular() || !actual.Mode().IsRegular() || !os.SameFile(expected, actual) {
		return errors.New("coverage-attestation stage identity changed; refusing cleanup")
	}
	if err := p.root.Remove(name); err != nil {
		return fmt.Errorf("remove coverage-attestation stage: %w", err)
	}
	return nil
}

func (p *pinnedParent) verifyPublicFinal(expected os.FileInfo) error {
	if err := p.validatePublicPath(); err != nil {
		return err
	}
	pinnedFinal, err := p.root.Lstat(p.name)
	if err != nil {
		return fmt.Errorf("inspect pinned coverage-attestation destination: %w", err)
	}
	publicFinal, err := os.Lstat(filepath.Join(p.path, p.name))
	if err != nil {
		return fmt.Errorf("inspect public coverage-attestation destination: %w", err)
	}
	if !expected.Mode().IsRegular() || !pinnedFinal.Mode().IsRegular() ||
		!publicFinal.Mode().IsRegular() || !os.SameFile(expected, pinnedFinal) ||
		!os.SameFile(expected, publicFinal) {
		return errors.New("coverage-attestation destination pathname no longer names the pinned publication")
	}
	return nil
}

func publishNew(
	parent *pinnedParent,
	payload []byte,
	admittedRoots []string,
	verifyRoots func() error,
	hooks publicationHooks,
) (resultErr error) {
	// All writes and cleanup are relative to the pinned parent Root. A public
	// pathname replacement therefore cannot redirect a write into another tree.
	if hooks.beforeStage != nil {
		hooks.beforeStage()
	}
	if err := parent.validatePublicPath(); err != nil {
		return err
	}
	if err := assertOutsideCanonicalRoots(filepath.Join(parent.path, parent.name), admittedRoots); err != nil {
		return err
	}
	if _, err := parent.root.Lstat(parent.name); err == nil {
		return fmt.Errorf("coverage-attestation destination already exists: %s", filepath.Join(parent.path, parent.name))
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect coverage-attestation destination: %w", err)
	}
	if err := verifyRoots(); err != nil {
		return fmt.Errorf("verify coverage-attestation admitted roots: %w", err)
	}

	stage, file, stageIdentity, err := createStage(parent.root)
	if err != nil {
		return err
	}
	stagePresent := true
	defer func() {
		if stagePresent {
			if cleanupErr := parent.removeStageIfExpected(stage, stageIdentity); cleanupErr != nil && resultErr == nil {
				resultErr = cleanupErr
			}
		}
	}()

	if err := writeAll(file, payload); err != nil {
		_ = file.Close()
		return fmt.Errorf("write coverage-attestation stage: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync coverage-attestation stage: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close coverage-attestation stage: %w", err)
	}
	if hooks.afterStageSync != nil {
		hooks.afterStageSync(stage)
	}
	if hooks.beforeLink != nil {
		hooks.beforeLink(stage)
	}
	// This is the definitive pre-publish check: staging may take long enough
	// for a root to be moved, replaced, or retargeted after the first check.
	if err := verifyRoots(); err != nil {
		return fmt.Errorf("verify coverage-attestation admitted roots: %w", err)
	}
	if err := parent.validatePublicPath(); err != nil {
		return err
	}
	if _, err := parent.root.Lstat(parent.name); err == nil {
		return fmt.Errorf("coverage-attestation destination already exists: %s", filepath.Join(parent.path, parent.name))
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect coverage-attestation destination: %w", err)
	}
	if err := parent.verifyStage(stage, stageIdentity); err != nil {
		return err
	}
	if err := parent.root.Link(stage, parent.name); err != nil {
		return fmt.Errorf("publish coverage attestation without overwrite: %w", err)
	}
	if hooks.afterLink != nil {
		hooks.afterLink()
	}

	if err := verifyRoots(); err != nil {
		return fmt.Errorf("verify coverage-attestation admitted roots: %w", err)
	}
	if err := parent.validatePublicPath(); err != nil {
		return err
	}
	if err := parent.verifyStage(stage, stageIdentity); err != nil {
		return err
	}
	// Once the no-replace link is visible, never unlink the final pathname on
	// an error: another process could race and replace that name between an
	// identity check and cleanup. Any later durability/cleanup failure is
	// reported nonzero while the already-validated final file remains.
	if err := parent.verifyPublicFinal(stageIdentity); err != nil {
		return err
	}
	if err := syncPinnedDirectory(parent.root); err != nil {
		return fmt.Errorf("sync coverage-attestation publication: %w", err)
	}
	if err := parent.removeStageIfExpected(stage, stageIdentity); err != nil {
		return err
	}
	stagePresent = false
	if err := syncPinnedDirectory(parent.root); err != nil {
		return fmt.Errorf("sync coverage-attestation cleanup: %w", err)
	}
	// Cleanup and directory sync can themselves be delayed while another actor
	// changes the observed parent or admitted roots. Do not report success
	// unless the public final still names the pinned publication afterwards.
	if err := verifyRoots(); err != nil {
		return fmt.Errorf("verify coverage-attestation admitted roots: %w", err)
	}
	if err := parent.validatePublicPath(); err != nil {
		return err
	}
	if err := parent.verifyPublicFinal(stageIdentity); err != nil {
		return err
	}
	return nil
}

func createStage(parent *os.Root) (string, *os.File, os.FileInfo, error) {
	for range 16 {
		var nonce [12]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return "", nil, nil, fmt.Errorf("generate coverage-attestation stage name: %w", err)
		}
		stage := ".spanwit-coverage-attestation-" + hex.EncodeToString(nonce[:]) + ".tmp"
		file, err := parent.OpenFile(stage, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			identity, statErr := file.Stat()
			if statErr != nil {
				_ = file.Close()
				return "", nil, nil, fmt.Errorf("inspect coverage-attestation stage: %w", statErr)
			}
			if !identity.Mode().IsRegular() {
				_ = file.Close()
				return "", nil, nil, errors.New("coverage-attestation stage is not a regular file")
			}
			return stage, file, identity, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return "", nil, nil, fmt.Errorf("create coverage-attestation stage: %w", err)
		}
	}
	return "", nil, nil, errors.New("create coverage-attestation stage: collision limit reached")
}

func writeAll(file *os.File, payload []byte) error {
	for len(payload) > 0 {
		written, err := file.Write(payload)
		if err != nil {
			return err
		}
		if written == 0 {
			return errors.New("short write")
		}
		payload = payload[written:]
	}
	return nil
}

func canonicalExistingPath(raw string) (string, error) {
	absolute, err := filepath.Abs(strings.TrimSpace(raw))
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(filepath.Clean(absolute))
}

func canonicalAdmittedRoot(raw string) (string, error) {
	root := filepath.Clean(strings.TrimSpace(raw))
	if root == "." || !filepath.IsAbs(root) {
		return "", errors.New("canonical admitted root must be an absolute path")
	}
	return root, nil
}

func assertOutsideCanonicalRoots(destination string, admittedRoots []string) error {
	for _, rawRoot := range admittedRoots {
		root, err := canonicalAdmittedRoot(rawRoot)
		if err != nil {
			return fmt.Errorf("resolve patient root for coverage-attestation preflight: %w", err)
		}
		if pathContains(root, destination) {
			return fmt.Errorf(
				"coverage-attestation destination must be outside enumerated root %s",
				root)
		}
	}
	return nil
}

func pathContains(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return relative == "." ||
		(relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}
