package bench

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/3leaps/spanwit/internal/corpus"
)

// TestShardRootSplitsBalancedTree confirms a tree with several top-level
// directories is split into at most the requested number of processes, not one
// per directory.
func TestShardRootSplitsBalancedTree(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 9; i++ {
		if err := os.Mkdir(filepath.Join(root, string(rune('a'+i))), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}

	shards, err := shardRoot(root, 4)
	if err != nil {
		t.Fatalf("shard: %v", err)
	}
	if len(shards) != 4 {
		t.Fatalf("expected 4 shards for 9 dirs and a limit of 4, got %d", len(shards))
	}

	var total int
	for _, s := range shards {
		if len(s) == 0 {
			t.Error("empty shard")
		}
		total += len(s)
	}
	if total != 9 {
		t.Errorf("shards cover %d directories, want 9", total)
	}
}

// TestShardRootFallsBackForUnsplittableTrees covers the case the strategy
// cannot handle: loose files at the root, which belong to no directory shard.
// Falling back to one unsharded run is correct; silently dropping them is not.
func TestShardRootFallsBackForUnsplittableTrees(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "loose.bin"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	shards, err := shardRoot(root, 8)
	if err != nil {
		t.Fatalf("shard: %v", err)
	}
	if len(shards) != 1 || len(shards[0]) != 1 || shards[0][0] != root {
		t.Errorf("expected a single unsharded run over the root, got %v", shards)
	}
}

// TestShardedFindMatchesSingleFind is the correctness gate: a sharded run must
// enumerate the same tree as one find, or its speed is measuring a different
// job.
func TestShardedFindMatchesSingleFind(t *testing.T) {
	m := buildCorpus(t, corpus.Spec{Kind: corpus.KindWide, Scale: 40, FileSize: 32})

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cfg := RunConfig{Root: m.Root, Workers: 4, Timeout: 30 * time.Second}

	single := FindRunner()
	if _, ok, reason := single.Probe(ctx); !ok {
		t.Skipf("find unavailable: %s", reason)
	}
	singleRes, err := single.Run(ctx, cfg)
	if err != nil {
		t.Fatalf("single find: %v", err)
	}

	sharded := &ShardedFindRunner{}
	shardedRes, err := sharded.Run(ctx, cfg)
	if err != nil {
		t.Fatalf("sharded find: %v", err)
	}

	if sharded.ShardCount() < 2 {
		t.Errorf("wide corpus produced %d shard(s); expected the tree to split",
			sharded.ShardCount())
	}

	// A sharded run over top-level directories does not print the root
	// itself, so it sees exactly one entry fewer.
	diff := singleRes.EntriesExamined - shardedRes.EntriesExamined
	if diff != 1 {
		t.Errorf("sharded find saw %d entries, single find saw %d (difference %d, want 1)",
			shardedRes.EntriesExamined, singleRes.EntriesExamined, diff)
	}
	if shardedRes.Outcome != OutcomeComplete {
		t.Errorf("sharded run outcome %q: %s", shardedRes.Outcome, shardedRes.Err)
	}
}

// TestShardedFindHandlesManyTopLevelDirs covers the failure this comparator
// hit on its first real sweep: a tree with tens of thousands of top-level
// directories overflows the argument limit for a single find invocation.
//
// The corpus here is small enough to run quickly but has far more top-level
// entries than one exec should carry, so a regression reintroducing a single
// invocation per shard fails this test rather than a later benchmark.
func TestShardedFindHandlesManyTopLevelDirs(t *testing.T) {
	root := t.TempDir()
	const dirs = 400
	for i := 0; i < dirs; i++ {
		d := filepath.Join(root, "dir-with-a-deliberately-long-name-"+strconv.Itoa(i))
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(d, "f.bin"), []byte("x"), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	sharded := &ShardedFindRunner{}
	if _, ok, reason := sharded.Probe(ctx); !ok {
		t.Skipf("find unavailable: %s", reason)
	}

	res, err := sharded.Run(ctx, RunConfig{Root: root, Workers: 2, Timeout: 60 * time.Second})
	if err != nil {
		t.Fatalf("sharded run: %v", err)
	}
	if res.Outcome != OutcomeComplete {
		t.Fatalf("outcome %q: %s", res.Outcome, res.Err)
	}

	// Every directory and its file must be enumerated: 400 dirs + 400 files.
	if want := int64(dirs * 2); res.EntriesExamined != want {
		t.Errorf("enumerated %d entries, want %d", res.EntriesExamined, want)
	}
}

// TestBatchRootsCoversEverything confirms batching loses no starting point,
// which is the property that makes the argument-limit fix safe.
func TestBatchRootsCoversEverything(t *testing.T) {
	roots := make([]string, 1000)
	for i := range roots {
		roots[i] = strconv.Itoa(i)
	}

	var seen int
	for _, batch := range batchRoots(roots, 128) {
		if len(batch) > 128 {
			t.Fatalf("batch of %d exceeds the limit", len(batch))
		}
		seen += len(batch)
	}
	if seen != len(roots) {
		t.Errorf("batches cover %d roots, want %d", seen, len(roots))
	}

	if got := batchRoots(nil, 128); len(got) != 0 {
		t.Errorf("empty input produced %d batches", len(got))
	}
}

// TestShardedFindReportsIncompleteRunAsFailed confirms a run that could not
// enumerate the whole tree does not present itself as merely partial, which
// would let its throughput sit beside a complete run's.
func TestShardedFindReportsIncompleteRunAsFailed(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a", "b"} {
		if err := os.Mkdir(filepath.Join(root, name), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}

	ctx := context.Background()
	// A binary that does not exist makes every invocation fail to start.
	sharded := &ShardedFindRunner{Binary: "definitely-not-a-real-find-binary"}
	res, err := sharded.Run(ctx, RunConfig{Root: root, Workers: 2, Timeout: 10 * time.Second})
	if err == nil {
		t.Fatal("expected an error when no invocation could run")
	}
	if res.Outcome != OutcomeFailed {
		t.Errorf("outcome %q, want %q", res.Outcome, OutcomeFailed)
	}
}
