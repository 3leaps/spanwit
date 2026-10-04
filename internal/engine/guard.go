package engine

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Authorization provenance kinds. A candidate may only be deleted when it
// carries a known, non-inferred provenance that names the code path which
// produced it. An empty or unrecognized value fails closed: neither a bare
// allowlist, catalog metadata, nor filepath.Dir(candidate) is authorization.
const (
	// ProvenanceNone marks a candidate with no deletion authorization (e.g. bare
	// --allowlist, which is dry-run convenience only). It never authorizes delete.
	ProvenanceNone = ""
	// ProvenanceConfigProfile marks a candidate discovered under an enabled
	// config path-profile; the profile root is the authorization boundary.
	ProvenanceConfigProfile = "config-profile"
	// ProvenanceExactPlanAnalysis marks a candidate replayed from a space report
	// exact_plan; the carried analysis_root is the authorization boundary.
	ProvenanceExactPlanAnalysis = "exact-plan-analysis-root"
)

// Containment relations between a candidate and its authorization boundary.
const (
	// ContainmentDescendant (default) requires the candidate to be a strict
	// descendant of the boundary. Boundary equality is refused.
	ContainmentDescendant = "descendant"
	// ContainmentSelfOrDescendant additionally permits candidate == boundary.
	// Only a registered capability that legitimately deletes its own root (e.g.
	// a location cache directory) may opt into this; config discovery and
	// exact-plan replay do not (see allowedRelations).
	ContainmentSelfOrDescendant = "self-or-descendant"
)

// allowedRelations binds each known provenance to the containment relations it
// may request. Config discovery and exact-plan replay are strict-descendant
// only: they can never delete a directory that equals its boundary. Only a
// future code-registered location capability may add ContainmentSelfOrDescendant
// (e.g. deleting a cache directory that is itself that capability's boundary).
// Catalog metadata or a class ceiling can never mint a provenance and therefore
// can never construct an authorized request.
var allowedRelations = map[string]map[string]bool{
	ProvenanceConfigProfile:     {ContainmentDescendant: true},
	ProvenanceExactPlanAnalysis: {ContainmentDescendant: true},
}

// DeletionRequest is the request-shaped input to the shared deletion guard.
// Every destructive caller constructs one; there is no path to os.RemoveAll that
// does not pass through AuthorizeDeletion + RemoveGuarded.
type DeletionRequest struct {
	// Target is the directory proposed for deletion. It must already be absolute
	// and clean (the guard does not reinterpret it against the working directory).
	Target string
	// Boundary is the non-inferred authorization root the target must be
	// contained under, already absolute and clean. It must come from a
	// code-registered source (config profile root, carried analysis_root, or a
	// future capability boundary) — never from the target string, its parent,
	// catalog metadata, or an allowlist entry.
	Boundary string
	// Relation is the permitted containment relation. Empty means
	// ContainmentDescendant (strict). It must be allowed for the provenance.
	Relation string
	// Provenance identifies the authorization source. ProvenanceNone or an
	// unknown value is a hard refusal.
	Provenance string
}

// DeletionToken is an opaque, validated authorization to delete one specific
// directory identity. It is produced only by AuthorizeDeletion and consumed only
// by RemoveGuarded, closing the gap between validating one path and deleting
// another.
type DeletionToken struct {
	target string
	info   os.FileInfo // Lstat at authorize time; identity is re-checked before delete
	birth  birthTime   // inode creation time at authorize time, where the platform reports one
}

// birthTime is an inode creation time. ok is false where the platform or
// filesystem does not report one, in which case it carries no claim.
type birthTime struct {
	sec  int64
	nsec int64
	ok   bool
}

// equal reports whether two witnesses describe the same inode creation. Two
// witnesses are comparable only when both are present; an absent witness makes
// no claim either way (the caller keeps the device+inode comparison).
func (b birthTime) equal(other birthTime) bool {
	return b.sec == other.sec && b.nsec == other.nsec
}

// Target returns the absolute, validated path the token authorizes.
func (t DeletionToken) Target() string { return t.target }

// guardPreDeleteHook, when non-nil, runs inside RemoveGuarded immediately before
// the final identity re-check. It exists only for tests that inject a path
// identity replacement between authorization and deletion; production leaves it
// nil.
var guardPreDeleteHook func(path string)

// deviceIDForTest, when non-nil, replaces the platform device lookup used by the
// nested-mount preflight so that refusal can be exercised deterministically on
// any platform. Production leaves it nil.
var deviceIDForTest func(path string) (uint64, bool, error)

// AuthorizeDeletion validates a deletion request and returns a token that
// RemoveGuarded will honor. It refuses (fail-closed) on missing/unknown
// provenance, a relation not permitted for that provenance, relative/unclean
// inputs, protected roots (volume roots, home by identity), non-directories,
// symlink path components, containment violations (escape, boundary equality
// without an opt-in, sibling-prefix confusion), volume changes, and cross-mount
// crossings.
func AuthorizeDeletion(req DeletionRequest) (DeletionToken, error) {
	allowed, ok := allowedRelations[req.Provenance]
	if !ok {
		return DeletionToken{}, fmt.Errorf("refusing delete: missing or unknown authorization provenance %q", req.Provenance)
	}
	relation := req.Relation
	if relation == "" {
		relation = ContainmentDescendant
	}
	if relation != ContainmentDescendant && relation != ContainmentSelfOrDescendant {
		return DeletionToken{}, fmt.Errorf("refusing delete: unknown containment relation %q", relation)
	}
	if !allowed[relation] {
		return DeletionToken{}, fmt.Errorf("refusing delete: containment relation %q is not permitted for provenance %q (strict descendant required)", relation, req.Provenance)
	}

	boundary, err := requireCleanAbs(req.Boundary)
	if err != nil {
		return DeletionToken{}, fmt.Errorf("authorization boundary: %w", err)
	}
	target, err := requireCleanAbs(req.Target)
	if err != nil {
		return DeletionToken{}, fmt.Errorf("delete target: %w", err)
	}

	// A volume root can never be an authorization boundary (it would authorize
	// deleting anything on the volume) and never a delete target.
	if isVolumeRoot(boundary) {
		return DeletionToken{}, fmt.Errorf("refusing delete: authorization boundary is a volume root %s", boundary)
	}
	if err := refuseProtectedTarget(target); err != nil {
		return DeletionToken{}, err
	}

	// No symlink components on either path (leaf included). A user-controlled
	// symlink must never redirect the boundary or the target.
	if err := ValidatePathNoSymlinkComponents(boundary); err != nil {
		return DeletionToken{}, fmt.Errorf("authorization boundary: %w", err)
	}
	if err := ValidatePathNoSymlinkComponents(target); err != nil {
		return DeletionToken{}, fmt.Errorf("delete target: %w", err)
	}

	// Component-aware containment (no string-prefix false friends such as
	// /root/a vs /root/ab).
	if err := checkContainment(boundary, target, relation); err != nil {
		return DeletionToken{}, err
	}

	// Default-refuse crossing onto a different mount/volume beneath the boundary;
	// lexical containment alone is insufficient authority to delete there.
	if err := checkSameMount(boundary, target); err != nil {
		return DeletionToken{}, err
	}

	info, err := os.Lstat(target)
	if err != nil {
		return DeletionToken{}, fmt.Errorf("delete target: stat: %w", err)
	}
	if !info.IsDir() {
		return DeletionToken{}, fmt.Errorf("refusing to delete non-directory %s", target)
	}
	birth, err := birthWitness(target)
	if err != nil {
		return DeletionToken{}, fmt.Errorf("delete target: creation-time witness: %w", err)
	}
	return DeletionToken{target: target, info: info, birth: birth}, nil
}

// RemoveGuarded deletes the directory a token authorizes. Immediately before
// deletion it re-validates symlink components, re-compares directory identity
// (os.SameFile plus an inode creation-time witness, which detects a replacement
// that reused the authorized inode), and refuses if the recursive tree would
// descend into a nested mount on a different device. It then verifies the path
// is gone.
//
// Residual risk: os.RemoveAll resolves by path, so a race after the final check
// cannot be fully excluded without descriptor-relative (no-follow) removal. On
// platforms or filesystems that report no creation time, replacement detection
// falls back to device+inode alone and an inode-reusing replacement is not
// detected. This guard minimizes and documents those windows; it does not claim
// race-freedom.
func RemoveGuarded(tok DeletionToken) error {
	if tok.target == "" || tok.info == nil {
		return fmt.Errorf("refusing delete: uninitialized authorization token")
	}
	if guardPreDeleteHook != nil {
		guardPreDeleteHook(tok.target)
	}
	// TOCTOU-aware re-checks immediately before deletion. Re-validating symlink
	// components here (not earlier) catches an ancestor being swapped to a symlink
	// after authorization even when the leaf inode is unchanged (os.SameFile alone
	// would not catch an ancestor redirect).
	if err := ValidatePathNoSymlinkComponents(tok.target); err != nil {
		return err
	}
	cur, err := os.Lstat(tok.target)
	if err != nil {
		return fmt.Errorf("re-stat before delete: %w", err)
	}
	if cur.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing delete: %s became a symlink after authorization", tok.target)
	}
	if !cur.IsDir() {
		return fmt.Errorf("refusing delete: %s is no longer a directory", tok.target)
	}
	if !os.SameFile(tok.info, cur) {
		return fmt.Errorf("refusing delete: %s identity changed after authorization (possible replacement)", tok.target)
	}
	// Device+inode equality is not sufficient on its own: Linux readily reuses an
	// inode number, so a directory removed and recreated between authorization and
	// deletion can satisfy os.SameFile. Creation time is fixed for an inode's life
	// and so distinguishes the authorized directory from a replacement that
	// inherited its inode. Where the platform reports no witness the comparison is
	// skipped and the documented residual applies.
	curBirth, err := birthWitness(tok.target)
	if err != nil {
		return fmt.Errorf("refusing delete: creation-time witness failed for %s: %w", tok.target, err)
	}
	if tok.birth.ok && curBirth.ok && !tok.birth.equal(curBirth) {
		return fmt.Errorf("refusing delete: %s identity changed after authorization (possible replacement)", tok.target)
	}
	// Refuse if the recursive delete would descend into a nested mount on a
	// different device (os.RemoveAll would otherwise delete across that boundary).
	if err := refuseNestedMounts(tok.target); err != nil {
		return err
	}
	if err := os.RemoveAll(tok.target); err != nil {
		return err
	}
	if _, err := os.Lstat(tok.target); err == nil {
		return fmt.Errorf("path still exists after removal")
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("verify delete: %w", err)
	}
	return nil
}

// requireCleanAbs validates that p is a non-empty, already-absolute, already-clean
// path. It deliberately does NOT rewrite the input: authority must not be
// reinterpreted against the process working directory or normalized through "..".
func requireCleanAbs(p string) (string, error) {
	if strings.TrimSpace(p) == "" {
		return "", fmt.Errorf("path is required")
	}
	if !filepath.IsAbs(p) {
		return "", fmt.Errorf("path must be absolute (authority is not reinterpreted against the working directory): %q", p)
	}
	if filepath.Clean(p) != p {
		return "", fmt.Errorf("path must be already-clean (no ., .., or redundant separators): %q", p)
	}
	return p, nil
}

// isVolumeRoot reports whether p is a filesystem/volume root on any platform
// (unix "/", a Windows drive root such as C:\, or a UNC share root). A path is a
// root when it is its own parent.
func isVolumeRoot(p string) bool {
	return filepath.Dir(p) == p
}

// refuseProtectedTarget refuses deleting a volume root or the user's home
// directory outright, regardless of the carried boundary. Home is matched by
// filesystem identity (os.SameFile), not only by spelling, so an alternate
// alias/mount to the same directory cannot bypass the guard.
func refuseProtectedTarget(target string) error {
	if isVolumeRoot(target) {
		return fmt.Errorf("refusing to delete volume root %s", target)
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil
	}
	hc := filepath.Clean(home)
	if hc == target {
		return fmt.Errorf("refusing to delete home directory %s", target)
	}
	hi, herr := os.Stat(hc)
	ti, terr := os.Lstat(target)
	if herr == nil && terr == nil && os.SameFile(hi, ti) {
		return fmt.Errorf("refusing to delete home directory (identity match) %s", target)
	}
	return nil
}

// checkContainment enforces the boundary→target relation using component-aware
// filepath.Rel semantics rather than string prefixes.
func checkContainment(boundary, target, relation string) error {
	if filepath.VolumeName(boundary) != filepath.VolumeName(target) {
		return fmt.Errorf("refusing delete: target %s is on a different volume than boundary %s", target, boundary)
	}
	if boundary == target {
		if relation == ContainmentSelfOrDescendant {
			return nil
		}
		return fmt.Errorf("refusing delete: target %s equals authorization boundary (strict descendant required)", target)
	}
	rel, err := filepath.Rel(boundary, target)
	if err != nil {
		return fmt.Errorf("refusing delete: target %s is not relatable to boundary %s: %w", target, boundary, err)
	}
	rel = filepath.ToSlash(rel)
	if rel == "." {
		if relation == ContainmentSelfOrDescendant {
			return nil
		}
		return fmt.Errorf("refusing delete: target %s equals authorization boundary (strict descendant required)", target)
	}
	if rel == ".." || strings.HasPrefix(rel, "../") {
		return fmt.Errorf("refusing delete: target %s escapes authorization boundary %s", target, boundary)
	}
	return nil
}

// checkSameMount refuses a target that sits on a different mount/volume than its
// boundary. Where the platform cannot report device identity, it does not block
// (best-effort), and the documented residual applies.
func checkSameMount(boundary, target string) error {
	same, err := sameMount(boundary, target)
	if err != nil {
		return fmt.Errorf("refusing delete: cannot verify %s shares a volume with boundary %s: %w", target, boundary, err)
	}
	if !same {
		return fmt.Errorf("refusing delete: target %s crosses onto a different mount than boundary %s (an explicit boundary on that volume is required)", target, boundary)
	}
	return nil
}

// lookupDeviceID returns the device id for a path, honoring the test hook.
func lookupDeviceID(path string) (uint64, bool, error) {
	if deviceIDForTest != nil {
		return deviceIDForTest(path)
	}
	return deviceID(path)
}

// refuseNestedMounts walks the target subtree and refuses deletion if any
// descendant directory sits on a different device than the target root — i.e. a
// nested mount that os.RemoveAll would otherwise delete across. Where device
// identity cannot be determined (non-unix, or an undeterminable entry) it does
// not block; lexical containment and the boundary/target mount check still apply.
// Symlinks are not followed (WalkDir does not descend them).
func refuseNestedMounts(root string) error {
	rootDev, ok, err := lookupDeviceID(root)
	if err != nil {
		return fmt.Errorf("refusing delete: nested-mount preflight failed for %s: %w", root, err)
	}
	if !ok {
		return nil
	}
	var nested string
	walkErr := filepath.WalkDir(root, func(p string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if !d.IsDir() {
			return nil
		}
		dev, ok, err := lookupDeviceID(p)
		if err != nil {
			return err
		}
		if ok && dev != rootDev {
			nested = p
			return filepath.SkipAll
		}
		return nil
	})
	if walkErr != nil {
		return fmt.Errorf("refusing delete: nested-mount preflight failed for %s: %w", root, walkErr)
	}
	if nested != "" {
		return fmt.Errorf("refusing delete: %s contains a nested mount at %s on a different device (an explicit boundary on that volume is required)", root, nested)
	}
	return nil
}
