package corpus

import (
	"errors"
	"fmt"
	"syscall"
)

// Shape layout constants. These are fixed rather than scaled so that a shape's
// character does not change with scale: a wide corpus stays wide as it grows,
// and a deep one stays deep.
const (
	wideDirCount    = 4   // directories holding the wide fan-out
	emptyDirBucket  = 100 // empty directories per bucket dir
	hardLinkFanout  = 3   // directory entries per shared inode
	devTreeFileSpan = 50  // approximate files per synthetic project
)

// buildMixedPermissions interleaves readable directories with directories and
// files the building user cannot open. Scale sets the number of each.
func (b *builder) buildMixedPermissions() error {
	if !b.man.Capabilities.PermissionEnforcement {
		b.omit(CapPermissionEnforcement, b.man.Capabilities.Reasons[CapPermissionEnforcement])
	}
	size := b.spec.fileSize()

	for i := 0; i < b.spec.Scale; i++ {
		readable := fmt.Sprintf("readable-%04d", i)
		if err := b.mkdir(readable); err != nil {
			return err
		}
		for j := 0; j < 2; j++ {
			if err := b.writeFile(join(readable, fmt.Sprintf("file-%02d.bin", j)), size); err != nil {
				return err
			}
		}

		sealed := fmt.Sprintf("sealed-%04d", i)
		if err := b.mkdir(sealed); err != nil {
			return err
		}
		hidden := int64(0)
		for j := 0; j < 2; j++ {
			if err := b.writeFile(join(sealed, fmt.Sprintf("file-%02d.bin", j)), size); err != nil {
				return err
			}
			hidden++
		}
		// The sealed directory itself stays visible to its parent's
		// listing; only its contents become unreachable.
		b.sealDir(sealed, hidden)
	}

	// An unreadable file inside a readable directory: visible to a listing,
	// but its content cannot be opened. Metadata-only traversals see it
	// normally, which is the distinction being fixtured.
	if err := b.mkdir("mixed"); err != nil {
		return err
	}
	return b.writeUnreadableFile(join("mixed", "sealed-file.bin"), size)
}

// buildSymlinks creates file links, a directory link, dangling links, a
// two-node cycle, and a self-referential link. Scale sets the number of
// targets and of each repeated link type.
func (b *builder) buildSymlinks() error {
	size := b.spec.fileSize()
	if err := b.mkdir("targets"); err != nil {
		return err
	}
	for i := 0; i < b.spec.Scale; i++ {
		if err := b.writeFile(join("targets", fmt.Sprintf("file-%04d.bin", i)), size); err != nil {
			return err
		}
	}

	if !b.man.Capabilities.Symlinks {
		b.omit(CapSymlinks, b.man.Capabilities.Reasons[CapSymlinks])
		return nil
	}

	if err := b.mkdir("links"); err != nil {
		return err
	}
	for i := 0; i < b.spec.Scale; i++ {
		target := fmt.Sprintf("../targets/file-%04d.bin", i)
		if err := b.symlink(join("links", fmt.Sprintf("to-file-%04d", i)), target, false); err != nil {
			return err
		}
		missing := fmt.Sprintf("../targets/absent-%04d.bin", i)
		if err := b.symlink(join("links", fmt.Sprintf("dangling-%04d", i)), missing, true); err != nil {
			return err
		}
	}

	// A directory symlink: a traversal that follows it revisits the whole
	// targets tree and double-counts every file under it.
	if err := b.symlink(join("links", "to-targets"), "../targets", false); err != nil {
		return err
	}
	// A self-referential link and a two-node cycle: a traversal that follows
	// directory links without cycle detection does not terminate here.
	if err := b.symlink(join("links", "self"), ".", false); err != nil {
		return err
	}
	if err := b.symlink(join("links", "cycle-a"), "cycle-b", false); err != nil {
		return err
	}
	return b.symlink(join("links", "cycle-b"), "cycle-a", false)
}

// buildSparseFiles creates files whose apparent size greatly exceeds their
// allocated size. Scale sets the file count; FileSize sets the apparent size.
func (b *builder) buildSparseFiles() error {
	if err := b.mkdir("sparse"); err != nil {
		return err
	}
	if !b.man.Capabilities.SparseFiles {
		b.omit(CapSparseFiles, b.man.Capabilities.Reasons[CapSparseFiles])
		return nil
	}
	apparent := b.spec.fileSize()
	for i := 0; i < b.spec.Scale; i++ {
		if err := b.writeSparseFile(join("sparse", fmt.Sprintf("sparse-%04d.img", i)), apparent); err != nil {
			return err
		}
	}
	// A dense file of the same apparent size, so a comparison has a control
	// whose allocated size does match its apparent size.
	return b.writeFile(join("sparse", "dense-control.bin"), minInt64(apparent, 1<<20))
}

// buildHardLinks creates groups of directory entries sharing one inode. Scale
// sets the number of groups.
func (b *builder) buildHardLinks() error {
	size := b.spec.fileSize()
	if err := b.mkdir("linked"); err != nil {
		return err
	}
	if !b.man.Capabilities.HardLinks {
		b.omit(CapHardLinks, b.man.Capabilities.Reasons[CapHardLinks])
		for i := 0; i < b.spec.Scale; i++ {
			if err := b.writeFile(join("linked", fmt.Sprintf("base-%04d.bin", i)), size); err != nil {
				return err
			}
		}
		return nil
	}

	for i := 0; i < b.spec.Scale; i++ {
		group := fmt.Sprintf("group-%04d", i)
		base := join("linked", fmt.Sprintf("base-%04d.bin", i))
		if err := b.writeFile(base, size); err != nil {
			return err
		}
		for j := 1; j < hardLinkFanout; j++ {
			alias := join("linked", fmt.Sprintf("alias-%04d-%d.bin", i, j))
			if err := b.hardLink(group, base, alias, size); err != nil {
				return err
			}
		}
	}
	return nil
}

// buildWide creates a few directories each holding many entries. This is the
// shape that punishes any traversal sorting a directory before descending.
func (b *builder) buildWide() error {
	size := b.spec.fileSize()
	for d := 0; d < wideDirCount; d++ {
		dir := fmt.Sprintf("wide-%d", d)
		if err := b.mkdir(dir); err != nil {
			return err
		}
		for i := 0; i < b.spec.Scale; i++ {
			if err := b.writeFile(join(dir, fmt.Sprintf("entry-%06d.bin", i)), size); err != nil {
				return err
			}
		}
	}
	return nil
}

// buildDeep creates a single nested chain of Scale directories with a file at
// the bottom and one every eighth level, so a traversal's depth handling and
// path-length behavior are exercised.
//
// Every platform has a maximum path length, and a deep enough chain reaches
// it: macOS refuses around depth 174 for this naming scheme. That ceiling is
// recorded as an omission and the corpus is completed at the depth actually
// reached, rather than failing the build. A traversal still gets a genuinely
// deep tree, and any claim made from it states the depth it really covered
// instead of the depth that was requested.
func (b *builder) buildDeep() error {
	size := b.spec.fileSize()
	rel := "deep"
	if err := b.mkdir(rel); err != nil {
		return err
	}

	reached := 0
	truncated := false

	for level := 0; level < b.spec.Scale && !truncated; level++ {
		next := join(rel, fmt.Sprintf("l%03d", level))
		if err := b.mkdir(next); err != nil {
			if isNameTooLong(err) {
				truncated = true
				break
			}
			return err
		}
		rel = next
		reached++

		if level%8 == 0 {
			// A file path is longer than the directory holding it, so the
			// limit is reached here first.
			written, err := b.writeFileIfPathFits(join(rel, "marker.bin"), size)
			if err != nil {
				return err
			}
			if !written {
				truncated = true
			}
		}
	}

	// Place the leaf at the deepest level that can still hold a file, backing
	// out one directory at a time. Without this the corpus would end with no
	// leaf at all, and a traversal test asserting one would fail for a reason
	// that has nothing to do with the traversal.
	for {
		written, err := b.writeFileIfPathFits(join(rel, "leaf.bin"), size)
		if err != nil {
			return err
		}
		if written {
			break
		}
		truncated = true
		parent := parentOf(rel)
		if parent == rel || parent == "." || parent == "" {
			break
		}
		rel = parent
	}

	if truncated {
		b.omit("deep-levels", fmt.Sprintf(
			"platform path length limit reached at depth %d of %d requested",
			reached, b.spec.Scale))
	}
	return nil
}

// buildManyEmptyDirs creates a large population of empty directories, spread
// across buckets so the shape stays distinct from the wide shape.
func (b *builder) buildManyEmptyDirs() error {
	for i := 0; i < b.spec.Scale; i++ {
		bucket := fmt.Sprintf("bucket-%04d", i/emptyDirBucket)
		if i%emptyDirBucket == 0 {
			if err := b.mkdir(bucket); err != nil {
				return err
			}
		}
		if err := b.mkdir(join(bucket, fmt.Sprintf("empty-%06d", i))); err != nil {
			return err
		}
	}
	return nil
}

// buildDevTree approximates a development tree: several projects, each with
// source files and build- and cache-shaped output directories full of small
// files. Scale sets the approximate total file count.
func (b *builder) buildDevTree() error {
	size := b.spec.fileSize()
	projects := b.spec.Scale / devTreeFileSpan
	if projects < 1 {
		projects = 1
	}
	perProject := b.spec.Scale / projects
	if perProject < 4 {
		perProject = 4
	}

	for p := 0; p < projects; p++ {
		proj := fmt.Sprintf("project-%04d", p)
		if err := b.mkdir(proj); err != nil {
			return err
		}
		// A signature file, so this corpus also exercises context-aware
		// discovery rather than enumeration alone.
		if err := b.writeFile(join(proj, "Cargo.toml"), 256); err != nil {
			return err
		}

		dirs := []string{
			join(proj, "src"),
			join(proj, "target", "debug", "deps"),
			join(proj, "node_modules", "pkg", "dist"),
			join(proj, ".cache"),
		}
		for _, d := range dirs {
			if err := b.mkdir(d); err != nil {
				return err
			}
		}
		for i := 0; i < perProject; i++ {
			dir := dirs[i%len(dirs)]
			name := fmt.Sprintf("unit-%05d.o", i)
			if err := b.writeFile(join(dir, name), size); err != nil {
				return err
			}
		}
	}
	return nil
}

// buildLargeFiles creates a small number of large files, the media and
// VM-image shape where per-entry overhead is irrelevant and read throughput is
// not what a metadata walk measures.
func (b *builder) buildLargeFiles() error {
	size := b.spec.fileSize()
	if err := b.mkdir("large"); err != nil {
		return err
	}
	for i := 0; i < b.spec.Scale; i++ {
		if err := b.writeFile(join("large", fmt.Sprintf("volume-%03d.img", i)), size); err != nil {
			return err
		}
	}
	return nil
}

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

// isNameTooLong reports whether an error is the platform refusing a path for
// exceeding its length limit.
//
// Checked against the errno rather than the message text so it does not depend
// on the C library's wording or locale.
func isNameTooLong(err error) bool {
	return errors.Is(err, syscall.ENAMETOOLONG)
}
