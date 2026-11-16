package engine

// Execution journal states reported on every executed prune
// (vocabulary execution_journal).
const (
	ExecutionJournaled             = "journaled"
	ExecutionUnavailableOnPlatform = "unavailable_on_platform"
)

// Assertions an execution can run under (vocabulary execution_assertion).
const (
	ExecutionAssertionApprovalBound = "approval_bound"
	ExecutionAssertionOpen          = "open"
)
