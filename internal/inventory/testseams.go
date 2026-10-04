package inventory

import "io/fs"

// Test seams for packages that drive a directory-audit run end to end. They
// exist only to place deterministic barriers; production code never calls them.

// AuditHooksForTest mirrors the internal audit barriers.
type AuditHooksForTest struct {
	BeforeSelect func()
	BeforeRow    func(index int)
	AfterCommit  func()
}

// SetAuditHooksForTest installs deterministic audit barriers on opts.
func SetAuditHooksForTest(opts *Options, hooks *AuditHooksForTest) {
	if hooks == nil {
		opts.auditHooks = nil
		return
	}
	opts.auditHooks = &auditHooks{
		beforeSelect: hooks.BeforeSelect, beforeRow: hooks.BeforeRow, afterCommit: hooks.AfterCommit,
	}
}

// SetFileAllocationUnavailableForTest makes every file's allocated size
// unobservable until the returned restore function runs. Not safe for
// parallel tests.
func SetFileAllocationUnavailableForTest() (restore func()) {
	original := fileMetadataOf
	fileMetadataOf = func(info fs.FileInfo) fileMetadata {
		meta := original(info)
		meta.allocated = nil
		return meta
	}
	return func() { fileMetadataOf = original }
}
