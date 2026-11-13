//go:build darwin || linux

package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	spanwitschema "github.com/3leaps/spanwit/config/schema"
	"github.com/3leaps/spanwit/internal/contract"
	"github.com/3leaps/spanwit/internal/observe"
	"github.com/3leaps/spanwit/internal/profiles"
)

func buildTestProfilePack(t *testing.T, root string, maxBytes int64) (*profiles.Pack, []byte) {
	t.Helper()
	const (
		gi             = int64(1024 * 1024 * 1024)
		floorBytes     = 30 * gi
		recoveryTarget = 36 * gi
	)
	policy := map[string]any{
		"version":  1,
		"defaults": map[string]any{"min_age": "24h"},
		"paths": []any{
			map[string]any{
				"path":      root,
				"max_depth": 8,
				"targets":   []any{map[string]any{"signature": "development.rust.cargo-target"}},
			},
		},
	}
	doc := map[string]any{
		"$schema": profiles.Schema,
		"version": 0,
		"recipes": map[string]any{
			"cargo": map[string]any{
				"capability": "directory-prune",
				"budget": map[string]any{
					"max_bytes":      maxBytes,
					"eviction_order": profiles.EvictionOrderIdleOldestFirst,
					"authority":      profiles.AuthorityPropose,
				},
				"policy": policy,
			},
		},
		"profiles": map[string]any{
			"developer": map[string]any{
				"label":   "Developer",
				"recipes": []string{"cargo"},
				"volume_objective": map[string]any{
					"path":                  root,
					"floor_bytes":           floorBytes,
					"recovery_target_bytes": recoveryTarget,
					"max_sample_age":        "15m",
				},
			},
			"media": map[string]any{
				"label":   "Media",
				"recipes": []string{},
			},
		},
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	pack, err := profiles.Load(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	return pack, raw
}

func makeTestCargoTarget(t *testing.T, root, name string, payloadBytes int, age time.Duration) string {
	t.Helper()
	proj := filepath.Join(root, name)
	debugDir := filepath.Join(proj, "target", "debug")
	if err := os.MkdirAll(debugDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, "Cargo.toml"), []byte("[package]\nname = \""+name+"\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(debugDir, "lib.rlib"), bytes.Repeat([]byte("a"), payloadBytes), 0600); err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().Add(-age)
	if err := filepath.Walk(proj, func(p string, _ os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		return os.Chtimes(p, stamp, stamp)
	}); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(proj, "target")
}

func TestProfileActivationLifecycleExpectedRevisionAndPointerRebuild(t *testing.T) {
	stateDir := privateDir(t)
	scanRoot := privateDir(t)
	_ = makeTestCargoTarget(t, scanRoot, "crate-a", 4096, 72*time.Hour)

	pack1, _ := buildTestProfilePack(t, scanRoot, 100)
	pack2, _ := buildTestProfilePack(t, scanRoot, 200)

	// Initial status is inactive.
	st0, err := InspectProfileStatus(context.Background(), stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if st0.Active || st0.State != "inactive" {
		t.Fatalf("expected initial inactive status, got %+v", st0)
	}

	// Wrong expected-revision fails before activating.
	if _, err := ActivateProfile(context.Background(), stateDir, pack1, ActivateProfileOptions{
		Profile:          "developer",
		ExpectedRevision: pack2.Revision(),
	}); !errors.Is(err, ErrExpectedRevisionMismatch) {
		t.Fatalf("expected ErrExpectedRevisionMismatch, got %v", err)
	}

	// Activate pack1 expecting "none".
	st1, err := ActivateProfile(context.Background(), stateDir, pack1, ActivateProfileOptions{
		Profile:          "developer",
		ExpectedRevision: "none",
		IdempotencyKey:   "act-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !st1.Active || st1.PackRevision != pack1.Revision() || st1.Profile != "developer" || st1.JournalRevision != 1 {
		t.Fatalf("unexpected activated status: %+v", st1)
	}

	// Corrupt or remove the convenience pointer active-profile.json and verify
	// InspectProfileStatus rebuilds it from the authoritative journal head.
	pointerPath := filepath.Join(stateDir, profilePointerFile)
	if err := os.WriteFile(pointerPath, []byte(`{"state":"inactive","active":false}`), 0600); err != nil {
		t.Fatal(err)
	}
	rebuilt, err := InspectProfileStatus(context.Background(), stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if !rebuilt.Active || rebuilt.PackRevision != pack1.Revision() || rebuilt.JournalRevision != 1 {
		t.Fatalf("pointer was not rebuilt from authoritative journal head: %+v", rebuilt)
	}

	// Stale expected-revision ("none") when pack1 is active must be rejected.
	if _, err := ActivateProfile(context.Background(), stateDir, pack2, ActivateProfileOptions{
		Profile:          "developer",
		ExpectedRevision: "none",
	}); !errors.Is(err, ErrExpectedRevisionMismatch) {
		t.Fatalf("expected ErrExpectedRevisionMismatch on stale 'none', got %v", err)
	}

	// Transition to pack2 with expected-revision = pack1.Revision().
	st2, err := ActivateProfile(context.Background(), stateDir, pack2, ActivateProfileOptions{
		Profile:          "developer",
		ExpectedRevision: pack1.Revision(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !st2.Active || st2.PackRevision != pack2.Revision() || st2.PreviousRevision != pack1.Revision() || st2.JournalRevision != 2 {
		t.Fatalf("unexpected second activation status: %+v", st2)
	}

	// Deactivate with wrong expected-revision fails.
	if _, err := DeactivateProfile(context.Background(), stateDir, pack1.Revision(), ""); !errors.Is(err, ErrExpectedRevisionMismatch) {
		t.Fatalf("expected ErrExpectedRevisionMismatch on deactivate, got %v", err)
	}

	// Deactivate with matching expected-revision succeeds and retains both immutable pack bodies.
	stDeact, err := DeactivateProfile(context.Background(), stateDir, pack2.Revision(), "deact-1")
	if err != nil {
		t.Fatal(err)
	}
	if stDeact.Active || stDeact.State != "inactive" || stDeact.PreviousRevision != pack2.Revision() || stDeact.JournalRevision != 3 {
		t.Fatalf("unexpected deactivated status: %+v", stDeact)
	}
	for _, rev := range []string{pack1.Revision(), pack2.Revision()} {
		if _, err := readImmutablePackBytes(stateDir, rev); err != nil {
			t.Fatalf("referenced pack body %s was not retained after deactivation: %v", rev, err)
		}
	}
}

func TestProfileActivationRefusedWhileCoordinatorRunning(t *testing.T) {
	stateDir := privateDir(t)
	scanRoot := privateDir(t)
	pack, _ := buildTestProfilePack(t, scanRoot, 100)

	c, err := Open(stateDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTest(t, c)

	if _, err := ActivateProfile(context.Background(), stateDir, pack, ActivateProfileOptions{
		Profile: "developer",
	}); !errors.Is(err, ErrBusy) {
		t.Fatalf("expected ErrBusy when activating while daemon holds writer lock, got %v", err)
	}
	if _, err := DeactivateProfile(context.Background(), stateDir, "", ""); !errors.Is(err, ErrBusy) {
		t.Fatalf("expected ErrBusy when deactivating while daemon holds writer lock, got %v", err)
	}
}

func TestDaemonProposalsParityAndFailClosedOnMissingOrCorruptBody(t *testing.T) {
	stateDir := privateDir(t)
	scanRoot := privateDir(t)
	_ = makeTestCargoTarget(t, scanRoot, "crate-old", 8192, 96*time.Hour)
	_ = makeTestCargoTarget(t, scanRoot, "crate-new", 4096, 2*time.Hour)

	pack, _ := buildTestProfilePack(t, scanRoot, 100)
	if _, err := ActivateProfile(context.Background(), stateDir, pack, ActivateProfileOptions{
		Profile:          "developer",
		ExpectedRevision: "none",
	}); err != nil {
		t.Fatal(err)
	}

	const gi = int64(1024 * 1024 * 1024)
	fakeSample := observe.Sample{
		VolumeID:     "apfs:test-vol",
		MountPath:    "/",
		CapturedAt:   time.Now().UTC().Truncate(time.Microsecond),
		Basis:        observe.BasisStatfs,
		AvailBytes:   28 * gi, // below 30 GiB floor
		TotalBytes:   500 * gi,
		UsedBytes:    472 * gi,
		UsedPercent:  94.4,
		Level:        observe.LevelWarn,
		Coverage:     observe.CoverageComplete,
		SizeComplete: true,
	}
	sampler := &observe.FakeSampler{
		ByPath: map[string][]observe.Sample{
			scanRoot: {fakeSample, fakeSample},
		},
	}

	c, err := Open(stateDir, sampler)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTest(t, c)

	sampledStatus, err := c.Sample(context.Background(), scanRoot)
	if err != nil {
		t.Fatal(err)
	}
	gotProposals := c.EvaluateProposals(context.Background())
	if gotProposals.State != "evaluated" || gotProposals.ExecutionAuthorized {
		t.Fatalf("expected state=evaluated and execution_authorized=false, got %+v", gotProposals)
	}
	if len(gotProposals.Items) != 1 || gotProposals.Items[0].ProposedItemsCount != 1 || gotProposals.Items[0].ProposedItemsDigest == "" {
		t.Fatalf("expected 1 proposed item with digest, got %+v", gotProposals.Items)
	}

	// Verify offline PreviewWithOptions parity under identical sample and timestamp.
	evalTime, err := time.Parse("2006-01-02T15:04:05.000000Z", gotProposals.EvaluatedAt)
	if err != nil {
		t.Fatal(err)
	}
	offlineSampler := &observe.FakeSampler{
		ByPath: map[string][]observe.Sample{
			scanRoot: {*sampledStatus.Sample},
		},
	}
	offline, err := pack.PreviewWithOptions(context.Background(), "developer", profiles.PreviewOptions{
		Sampler: offlineSampler,
		Now:     evalTime,
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotProposals.Items[0].ProposedItemsDigest != offline.Recipes[0].ProposedItemsDigest ||
		!reflect.DeepEqual(gotProposals.Items[0].Budget, offline.Recipes[0].Budget) ||
		!reflect.DeepEqual(gotProposals.VolumeObjective, offline.VolumeObjective) {
		t.Fatalf("daemon proposal evaluation diverged from offline preview:\ndaemon=%+v\noffline=%+v", gotProposals, offline)
	}

	// Verify HTTP /v1/proposals and /v1/capabilities conform to spanwit-daemon-response.v1.schema.json.
	for _, path := range []string{"/v1/proposals", "/v1/capabilities"} {
		w := httptest.NewRecorder()
		Handler(c).ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 200 {
			t.Fatalf("GET %s status %d", path, w.Code)
		}
		if err := contract.ValidateJSON(spanwitschema.SpanwitDaemonResponseV1, w.Body.Bytes()); err != nil {
			t.Fatalf("GET %s schema validation failed: %v\nbody=%s", path, err, w.Body.String())
		}
	}

	// Corrupt the stored pack body on disk and verify EvaluateProposals transitions
	// to "unavailable" (never an empty "evaluated" list).
	hexPart, err := packDigestHex(pack.Revision())
	if err != nil {
		t.Fatal(err)
	}
	packFile := filepath.Join(stateDir, "packs", hexPart+".json")
	if err := os.WriteFile(packFile, []byte(`{"corrupted":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	corruptResp := c.EvaluateProposals(context.Background())
	if corruptResp.State != "unavailable" || len(corruptResp.Items) != 0 {
		t.Fatalf("expected corrupt pack body to yield state=unavailable, got %+v", corruptResp)
	}
	w := httptest.NewRecorder()
	Handler(c).ServeHTTP(w, httptest.NewRequest("GET", "/v1/proposals", nil))
	if err := contract.ValidateJSON(spanwitschema.SpanwitDaemonResponseV1, w.Body.Bytes()); err != nil {
		t.Fatalf("unavailable proposals failed schema validation: %v", err)
	}
}

func TestProfileActivationIdempotencyAndCompiledBindingDrift(t *testing.T) {
	stateDir := privateDir(t)
	home1 := privateDir(t)
	home2 := privateDir(t)
	if err := os.MkdirAll(filepath.Join(home1, "cache"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home2, "cache"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home1)

	pack1, _ := buildTestProfilePack(t, "~/cache", 100)
	pack2, _ := buildTestProfilePack(t, "~/cache", 200)

	// Activate with an explicit idempotency key.
	st1, err := ActivateProfile(context.Background(), stateDir, pack1, ActivateProfileOptions{
		Profile:          "developer",
		ExpectedRevision: "none",
		IdempotencyKey:   "stable-act-key-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !st1.Active || st1.JournalRevision != 1 {
		t.Fatalf("expected active rev 1, got %+v", st1)
	}

	// Replaying the exact same activation with the same idempotency key and original expected-revision
	// succeeds without advancing the journal revision.
	stReplay, err := ActivateProfile(context.Background(), stateDir, pack1, ActivateProfileOptions{
		Profile:          "developer",
		ExpectedRevision: "none",
		IdempotencyKey:   "stable-act-key-1",
	})
	if err != nil {
		t.Fatalf("expected idempotent replay to succeed, got %v", err)
	}
	if stReplay.JournalRevision != 1 || stReplay.PackRevision != pack1.Revision() {
		t.Fatalf("expected idempotent replay to return revision 1, got %+v", stReplay)
	}

	// Reusing the same idempotency key with a different pack revision returns ErrIdempotencyConflict.
	if _, err := ActivateProfile(context.Background(), stateDir, pack2, ActivateProfileOptions{
		Profile:          "developer",
		ExpectedRevision: "none",
		IdempotencyKey:   "stable-act-key-1",
	}); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("expected ErrIdempotencyConflict on conflicting payload with same key, got %v", err)
	}

	// Verify that changing HOME (so ~/cache resolves to home2/cache instead of home1/cache)
	// causes compiled_policy_digest drift and EvaluateProposals fails closed with "unavailable".
	t.Setenv("HOME", home2)
	c, err := Open(stateDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTest(t, c)

	drifted := c.EvaluateProposals(context.Background())
	if drifted.State != "unavailable" || len(drifted.Items) != 0 {
		t.Fatalf("expected compiled policy drift (HOME changed) to yield state=unavailable, got %+v", drifted)
	}
}

func TestInspectProfileStatusIsStrictlyReadOnlyAndConcurrentWithDaemon(t *testing.T) {
	parent := privateDir(t)
	missingStateDir := filepath.Join(parent, "does-not-exist")

	// 1. Inspecting a non-existent state directory returns inactive without creating the directory.
	stMissing, err := InspectProfileStatus(context.Background(), missingStateDir)
	if err != nil {
		t.Fatalf("InspectProfileStatus on missing dir: %v", err)
	}
	if stMissing.Active || stMissing.State != "inactive" {
		t.Fatalf("expected inactive on missing dir, got %+v", stMissing)
	}
	if _, statErr := os.Stat(missingStateDir); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("InspectProfileStatus created missing stateDir on disk: %v", statErr)
	}

	// 2. Inspecting an initialized daemon journal without a profile stream does not create streams/profile
	// and works while the Coordinator holds coordinator.lock.
	stateDir := privateDir(t)
	c, err := Open(stateDir, nil)
	if err != nil {
		t.Fatal(err)
	}

	stNoStream, err := InspectProfileStatus(context.Background(), stateDir)
	if err != nil {
		t.Fatalf("InspectProfileStatus while daemon holds lock: %v", err)
	}
	if stNoStream.Active || stNoStream.State != "inactive" {
		t.Fatalf("expected inactive when no profile stream exists, got %+v", stNoStream)
	}
	if profileStreamExistsOnDisk(filepath.Join(stateDir, "journal")) {
		t.Fatal("InspectProfileStatus created profile stream directory during read-only status check")
	}
	closeTest(t, c)

	// 3. Activate a profile, corrupt the pointer file, and verify Open reconciles active-profile.json
	// from the authoritative journal head.
	scanRoot := privateDir(t)
	pack, _ := buildTestProfilePack(t, scanRoot, 100)
	if _, err := ActivateProfile(context.Background(), stateDir, pack, ActivateProfileOptions{
		Profile:          "developer",
		ExpectedRevision: "none",
	}); err != nil {
		t.Fatal(err)
	}
	pointerPath := filepath.Join(stateDir, profilePointerFile)
	if err := os.WriteFile(pointerPath, []byte(`{"state":"inactive","active":false}`), 0600); err != nil {
		t.Fatal(err)
	}
	c2, err := Open(stateDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTest(t, c2)

	reconciledPtr, err := readPointerStatus(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if !reconciledPtr.Active || reconciledPtr.PackRevision != pack.Revision() {
		t.Fatalf("expected Open to rebuild stale active-profile.json from journal head, got %+v", reconciledPtr)
	}
}

func TestEvaluateProposalsDoesNotAliasSampleFromDifferentPath(t *testing.T) {
	stateDir := privateDir(t)
	volPathA := privateDir(t)
	volPathB := privateDir(t)

	pack, _ := buildTestProfilePack(t, volPathB, 100)
	if _, err := ActivateProfile(context.Background(), stateDir, pack, ActivateProfileOptions{
		Profile:          "developer",
		ExpectedRevision: "none",
	}); err != nil {
		t.Fatal(err)
	}

	const gi = int64(1024 * 1024 * 1024)
	now := time.Now().UTC().Truncate(time.Microsecond)
	sampleA := observe.Sample{
		VolumeID:     "apfs:vol-a",
		MountPath:    "/vol-a",
		CapturedAt:   now,
		Basis:        observe.BasisStatfs,
		AvailBytes:   100 * gi, // Well above floor
		TotalBytes:   500 * gi,
		UsedBytes:    400 * gi,
		UsedPercent:  80.0,
		Level:        observe.LevelOK,
		Coverage:     observe.CoverageComplete,
		SizeComplete: true,
	}
	sampleB := observe.Sample{
		VolumeID:     "apfs:vol-b",
		MountPath:    "/vol-b",
		CapturedAt:   now,
		Basis:        observe.BasisStatfs,
		AvailBytes:   20 * gi, // Below 30 GiB floor
		TotalBytes:   500 * gi,
		UsedBytes:    480 * gi,
		UsedPercent:  96.0,
		Level:        observe.LevelCritical,
		Coverage:     observe.CoverageComplete,
		SizeComplete: true,
	}
	sampler := &observe.FakeSampler{
		ByPath: map[string][]observe.Sample{
			volPathA: {sampleA},
			volPathB: {sampleB},
		},
	}

	c, err := Open(stateDir, sampler)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTest(t, c)

	// Daemon samples volPathA, while active profile's volume_objective.path is volPathB.
	if _, err := c.Sample(context.Background(), volPathA); err != nil {
		t.Fatal(err)
	}
	resp := c.EvaluateProposals(context.Background())
	if resp.VolumeObjective == nil {
		t.Fatal("missing volume_objective in proposals")
	}
	if resp.VolumeObjective.AvailBytes == nil || *resp.VolumeObjective.AvailBytes != 20*gi || resp.VolumeObjective.Outcome != profiles.ObjectiveOutcomeUnmetProtected {
		t.Fatalf("expected EvaluateProposals to sample volPathB (20 GiB) rather than aliasing volPathA (100 GiB), got %+v", resp.VolumeObjective)
	}
	if resp.CapacityRecordID != "" {
		t.Fatalf("expected empty CapacityRecordID when daemon sample path (%s) != volume_objective.path (%s), got %q", volPathA, volPathB, resp.CapacityRecordID)
	}
}
