package corpus

import (
	"fmt"
	"io/fs"
	"math/rand"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// contentChunk is the reusable buffer size for writing file content.
const contentChunk = 64 << 10

// Build creates the corpus described by spec under root, which must already
// exist, and returns its ground-truth manifest.
//
// Every count in the returned manifest is recorded at plan time, before the
// corresponding object is created; if creation fails, Build returns an error
// rather than a manifest describing a tree that was not built.
func Build(root string, spec Spec) (*Manifest, error) {
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("corpus: stat root: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("corpus: root %q is not a directory", root)
	}

	caps, err := Probe(root)
	if err != nil {
		return nil, err
	}

	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("corpus: resolve root: %w", err)
	}

	b := &builder{
		spec:     spec,
		rng:      rand.New(rand.NewSource(spec.Seed)), //nolint:gosec // fixture determinism, not security
		buf:      make([]byte, contentChunk),
		dirsMade: map[string]bool{},
		man: &Manifest{
			ID:           spec.ID(),
			Kind:         spec.Kind,
			Spec:         spec,
			Root:         abs,
			Capabilities: caps,
		},
	}
	b.rng.Read(b.buf)
	b.modTime = spec.baseTime()

	if err := b.build(); err != nil {
		return nil, err
	}
	return b.man, nil
}

// builder accumulates the manifest while creating objects.
type builder struct {
	spec    Spec
	man     *Manifest
	rng     *rand.Rand
	buf     []byte
	modTime time.Time

	// deferredModes holds directories whose mode must be tightened after
	// their contents are written.
	deferredModes []deferredMode

	// dirsMade tracks directories already created and recorded, so an
	// ancestor shared by several shape elements is counted once.
	dirsMade map[string]bool
}

type deferredMode struct {
	rel  string
	mode fs.FileMode
}

func (b *builder) build() error {
	var err error
	switch b.spec.Kind {
	case KindMixedPermissions:
		err = b.buildMixedPermissions()
	case KindSymlinks:
		err = b.buildSymlinks()
	case KindSparseFiles:
		err = b.buildSparseFiles()
	case KindHardLinks:
		err = b.buildHardLinks()
	case KindWide:
		err = b.buildWide()
	case KindDeep:
		err = b.buildDeep()
	case KindManyEmptyDirs:
		err = b.buildManyEmptyDirs()
	case KindDevTree:
		err = b.buildDevTree()
	case KindLargeFiles:
		err = b.buildLargeFiles()
	default:
		err = fmt.Errorf("corpus: unhandled kind %q", b.spec.Kind)
	}
	if err != nil {
		return err
	}
	return b.applyDeferredModes()
}

// applyDeferredModes tightens directory permissions last, so content could be
// written first. Applied deepest-first so a parent is never sealed before its
// children are finished.
func (b *builder) applyDeferredModes() error {
	for i := len(b.deferredModes) - 1; i >= 0; i-- {
		d := b.deferredModes[i]
		if err := os.Chmod(b.abs(d.rel), d.mode); err != nil {
			return fmt.Errorf("corpus: chmod %s: %w", d.rel, err)
		}
	}
	return nil
}

func (b *builder) abs(rel string) string {
	return filepath.Join(b.man.Root, filepath.FromSlash(rel))
}

// nextModTime returns a deterministic, strictly decreasing modification time.
// Spacing objects one minute apart gives age-window filters a stable oracle
// without reading a clock.
func (b *builder) nextModTime() time.Time {
	t := b.modTime
	b.modTime = b.modTime.Add(-time.Minute)
	return t
}

func (b *builder) record(e Entry) {
	switch e.Class {
	case ClassDir:
		b.man.Dirs++
	case ClassFile:
		b.man.Files++
		b.man.ApparentBytes += e.ApparentBytes
	case ClassSymlink:
		b.man.Symlinks++
	}
	if e.Unreadable {
		b.man.Unreadable = append(b.man.Unreadable, e.RelPath)
	}
	if b.spec.RecordEntries {
		b.man.Entries = append(b.man.Entries, e)
	}
}

func (b *builder) omit(element, reason string) {
	b.man.Omitted = append(b.man.Omitted, Omission{Element: element, Reason: reason})
}

// mkdir creates a directory and every missing ancestor, recording each one.
//
// Recording only the requested path would undercount: creating "a/b/c" in one
// call produces three directories, and a manifest that counted one would
// disagree with any correct walk of the result. That is the same
// projection-presented-as-scope error the coverage-honesty fixtures exist to
// catch, so the oracle must not commit it.
func (b *builder) mkdir(rel string) error {
	cur := ""
	for _, part := range strings.Split(rel, "/") {
		if part == "" {
			continue
		}
		cur = join(cur, part)
		if b.dirsMade[cur] {
			continue
		}
		mt := b.nextModTime()
		if err := os.Mkdir(b.abs(cur), 0o755); err != nil {
			return fmt.Errorf("corpus: mkdir %s: %w", cur, err)
		}
		b.dirsMade[cur] = true
		b.record(Entry{RelPath: cur, Class: ClassDir, ModTime: mt})
	}
	return nil
}

// sealDir marks a directory unreadable once its contents are written, and
// records how many objects that seals away from any traversal. When the
// platform does not enforce mode bits the directory is left readable and the
// entry is not recorded as unreadable, so the manifest never claims a denial
// that will not occur.
func (b *builder) sealDir(rel string, hidden int64) {
	if !b.man.Capabilities.PermissionEnforcement {
		return
	}
	b.deferredModes = append(b.deferredModes, deferredMode{rel: rel, mode: 0o000})
	b.man.Unreadable = append(b.man.Unreadable, rel)
	b.man.UnreachableObjects += hidden
	if b.spec.RecordEntries {
		for i := range b.man.Entries {
			if b.man.Entries[i].RelPath == rel {
				b.man.Entries[i].Unreadable = true
				break
			}
		}
	}
}

// writeFile creates a regular file of the given apparent size with
// deterministic content.
func (b *builder) writeFile(rel string, size int64) error {
	mt := b.nextModTime()
	abs := b.abs(rel)
	f, err := os.OpenFile(abs, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("corpus: create %s: %w", rel, err)
	}
	remaining := size
	for remaining > 0 {
		n := int64(len(b.buf))
		if remaining < n {
			n = remaining
		}
		if _, err := f.Write(b.buf[:n]); err != nil {
			_ = f.Close()
			return fmt.Errorf("corpus: write %s: %w", rel, err)
		}
		remaining -= n
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("corpus: close %s: %w", rel, err)
	}
	if err := os.Chtimes(abs, mt, mt); err != nil {
		return fmt.Errorf("corpus: chtimes %s: %w", rel, err)
	}
	b.record(Entry{RelPath: rel, Class: ClassFile, ApparentBytes: size, ModTime: mt})
	return nil
}

// writeUnreadableFile creates a file the building user cannot open.
func (b *builder) writeUnreadableFile(rel string, size int64) error {
	if err := b.writeFile(rel, size); err != nil {
		return err
	}
	if !b.man.Capabilities.PermissionEnforcement {
		return nil
	}
	if err := os.Chmod(b.abs(rel), 0o000); err != nil {
		return fmt.Errorf("corpus: chmod %s: %w", rel, err)
	}
	b.man.Unreadable = append(b.man.Unreadable, rel)
	if b.spec.RecordEntries {
		for i := range b.man.Entries {
			if b.man.Entries[i].RelPath == rel {
				b.man.Entries[i].Unreadable = true
				break
			}
		}
	}
	return nil
}

// writeSparseFile creates a file whose apparent size is apparent bytes but
// which allocates only the final block.
func (b *builder) writeSparseFile(rel string, apparent int64) error {
	mt := b.nextModTime()
	abs := b.abs(rel)
	f, err := os.OpenFile(abs, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("corpus: create sparse %s: %w", rel, err)
	}
	if _, err := f.Seek(apparent-1, 0); err != nil {
		_ = f.Close()
		return fmt.Errorf("corpus: seek sparse %s: %w", rel, err)
	}
	if _, err := f.Write([]byte{0}); err != nil {
		_ = f.Close()
		return fmt.Errorf("corpus: write sparse %s: %w", rel, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("corpus: close sparse %s: %w", rel, err)
	}
	if err := os.Chtimes(abs, mt, mt); err != nil {
		return fmt.Errorf("corpus: chtimes %s: %w", rel, err)
	}
	b.record(Entry{RelPath: rel, Class: ClassFile, ApparentBytes: apparent, Sparse: true, ModTime: mt})
	return nil
}

// symlink creates a symbolic link recorded with its raw target.
func (b *builder) symlink(rel, target string, dangling bool) error {
	mt := b.nextModTime()
	if err := os.Symlink(filepath.FromSlash(target), b.abs(rel)); err != nil {
		return fmt.Errorf("corpus: symlink %s: %w", rel, err)
	}
	b.record(Entry{
		RelPath:    rel,
		Class:      ClassSymlink,
		LinkTarget: target,
		Dangling:   dangling,
		ModTime:    mt,
	})
	return nil
}

// hardLink adds a directory entry sharing srcRel's inode and records the group
// membership, which is what lets a test count those bytes once.
func (b *builder) hardLink(group, srcRel, dstRel string, size int64) error {
	if err := os.Link(b.abs(srcRel), b.abs(dstRel)); err != nil {
		return fmt.Errorf("corpus: link %s -> %s: %w", dstRel, srcRel, err)
	}
	b.record(Entry{
		RelPath:       dstRel,
		Class:         ClassFile,
		ApparentBytes: size,
		LinkGroup:     group,
		ModTime:       b.nextModTime(),
	})
	for i := range b.man.HardLinkGroups {
		if b.man.HardLinkGroups[i].Name == group {
			b.man.HardLinkGroups[i].Paths = append(b.man.HardLinkGroups[i].Paths, dstRel)
			return nil
		}
	}
	b.man.HardLinkGroups = append(b.man.HardLinkGroups, HardLinkGroup{
		Name:          group,
		Paths:         []string{srcRel, dstRel},
		ApparentBytes: size,
	})
	if b.spec.RecordEntries {
		for i := range b.man.Entries {
			if b.man.Entries[i].RelPath == srcRel {
				b.man.Entries[i].LinkGroup = group
				break
			}
		}
	}
	return nil
}

// Remove deletes a built corpus, restoring any mode bits that would otherwise
// prevent removal.
//
// A corpus containing 0000 directories cannot be removed by os.RemoveAll
// alone, so a test that forgot this would leak fixture trees into the
// temporary directory and, on some CI images, fail the next run rather than
// this one.
func Remove(m *Manifest) error {
	if m == nil || m.Root == "" {
		return nil
	}
	// Never chmod a symlink: os.Chmod follows it, so a link pointing at its
	// own parent (which the symlink corpus contains deliberately) would
	// strip that directory's traverse bit and make the tree unremovable.
	// Only directories need their mode restored for removal to proceed.
	_ = filepath.WalkDir(m.Root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			// A directory we could not descend into is exactly what needs
			// relaxing; WalkDir reports it with its own path.
			_ = os.Chmod(p, 0o700)
			return nil
		}
		if d.IsDir() && d.Type()&fs.ModeSymlink == 0 {
			_ = os.Chmod(p, 0o700)
		}
		return nil
	})
	if err := os.RemoveAll(m.Root); err != nil {
		return fmt.Errorf("corpus: remove %s: %w", m.Root, err)
	}
	return nil
}

// join builds a slash-separated relative path.
func join(parts ...string) string {
	return path.Join(parts...)
}

// writeFileIfPathFits creates a file, reporting false rather than erroring when
// the platform refuses the path for being too long.
//
// Separated from writeFile so only the shapes that deliberately approach the
// path limit tolerate it; everywhere else an over-long path stays an error.
func (b *builder) writeFileIfPathFits(rel string, size int64) (bool, error) {
	err := b.writeFile(rel, size)
	switch {
	case err == nil:
		return true, nil
	case isNameTooLong(err):
		return false, nil
	default:
		return false, err
	}
}

// parentOf returns the parent of a slash-separated relative path.
func parentOf(rel string) string {
	return path.Dir(rel)
}
