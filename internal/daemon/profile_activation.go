//go:build darwin || linux

package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/3leaps/recordprims"
	"github.com/3leaps/spanwit/internal/observe"
	"github.com/3leaps/spanwit/internal/profiles"
	"github.com/google/uuid"
	"golang.org/x/sys/unix"
)

const (
	ProfileActivationSchema = "https://schemas.3leaps.dev/spanwit/profile-activation/v0.json"
	profileStreamID         = "profile"
	profileRecordID         = "active-profile"
	profilePointerFile      = "active-profile.json"
)

var (
	ErrExpectedRevisionMismatch = errors.New("expected_revision_mismatch")
	ErrIdempotencyConflict      = errors.New("idempotency_conflict")
)

// ProfileActivationPayload is the durable record payload persisted on the
// "profile" journal stream. The journal head is the sole authoritative state;
// active-profile.json is a rebuildable convenience pointer.
type ProfileActivationPayload struct {
	Schema               string   `json:"$schema"`
	Version              int      `json:"version"`
	Active               bool     `json:"active"`
	Profile              string   `json:"profile,omitempty"`
	PackRevision         string   `json:"pack_revision,omitempty"`
	PreviousRevision     string   `json:"previous_revision,omitempty"`
	ResolvedRoots        []string `json:"resolved_roots,omitempty"`
	VolumeObjectivePath  string   `json:"volume_objective_path,omitempty"`
	CompiledPolicyDigest string   `json:"compiled_policy_digest,omitempty"`
	IdempotencyKey       string   `json:"idempotency_key,omitempty"`
	UpdatedAt            string   `json:"updated_at"`
}

// ProfileStatus summarizes the authoritative active-profile state.
type ProfileStatus struct {
	State                string   `json:"state"`
	Active               bool     `json:"active"`
	JournalRevision      uint64   `json:"journal_revision,omitempty"`
	Profile              string   `json:"profile,omitempty"`
	PackRevision         string   `json:"pack_revision,omitempty"`
	PreviousRevision     string   `json:"previous_revision,omitempty"`
	ResolvedRoots        []string `json:"resolved_roots,omitempty"`
	VolumeObjectivePath  string   `json:"volume_objective_path,omitempty"`
	CompiledPolicyDigest string   `json:"compiled_policy_digest,omitempty"`
	UpdatedAt            string   `json:"updated_at,omitempty"`
}

// ProposalItem summarizes one recipe's proposal evaluation under an active profile.
type ProposalItem struct {
	RecipeID             string                     `json:"recipe_id"`
	State                string                     `json:"state"`
	Roots                []string                   `json:"roots"`
	ProposedItemsDigest  string                     `json:"proposed_items_digest,omitempty"`
	ProposedItemsCount   int                        `json:"proposed_items_count"`
	ProposedReclaimBytes int64                      `json:"proposed_reclaim_bytes"`
	Budget               *profiles.BudgetEvaluation `json:"budget,omitempty"`
	Candidates           []profiles.Candidate       `json:"candidates"`
	Warnings             []string                   `json:"warnings"`
}

// ProposalsResponse is the read-only response for GET /v1/proposals.
type ProposalsResponse struct {
	Protocol             int                                 `json:"protocol"`
	State                string                              `json:"state"`
	Profile              string                              `json:"profile,omitempty"`
	PackRevision         string                              `json:"pack_revision,omitempty"`
	CompiledPolicyDigest string                              `json:"compiled_policy_digest,omitempty"`
	CapacityRecordID     string                              `json:"capacity_record_id,omitempty"`
	EvaluatedAt          string                              `json:"evaluated_at,omitempty"`
	ExecutionAuthorized  bool                                `json:"execution_authorized"`
	VolumeObjective      *profiles.VolumeObjectiveEvaluation `json:"volume_objective,omitempty"`
	Items                []ProposalItem                      `json:"items"`
	NextCursor           any                                 `json:"next_cursor"`
}

// ActivateProfile Options configures offline profile activation.
type ActivateProfileOptions struct {
	Profile          string
	ExpectedRevision string
	IdempotencyKey   string
}

// ActivateProfile requires the daemon to be stopped (acquiring the same
// coordinator.lock), verifies the expected active revision against the
// authoritative journal head, persists the immutable exact pack bytes under
// packs/<sha256>.json before appending the activation record, and writes the
// rebuildable pointer.
func ActivateProfile(ctx context.Context, dir string, pack *profiles.Pack, opts ActivateProfileOptions) (ProfileStatus, error) {
	if pack == nil {
		return ProfileStatus{}, fmt.Errorf("profile pack is required")
	}
	binding, err := pack.ResolveBinding(opts.Profile)
	if err != nil {
		return ProfileStatus{}, err
	}
	c, err := Open(dir, nil)
	if err != nil {
		return ProfileStatus{}, err
	}
	defer func() { _ = c.Close() }()
	return c.activateProfileLocked(ctx, pack.Bytes(), binding, opts)
}

// DeactivateProfile requires the daemon to be stopped, verifies the expected
// active revision against the authoritative journal head, and appends a durable
// revocation record while preserving all referenced pack bodies.
func DeactivateProfile(ctx context.Context, dir, expectedRevision, idempotencyKey string) (ProfileStatus, error) {
	c, err := Open(dir, nil)
	if err != nil {
		return ProfileStatus{}, err
	}
	defer func() { _ = c.Close() }()
	return c.deactivateProfileLocked(ctx, expectedRevision, idempotencyKey)
}

// InspectProfileStatus reads the profile status without mutating the state
// directory, acquiring coordinator.lock, or creating journal streams. When the
// profile journal stream exists and is not locked by a concurrent writer, it
// reads and verifies the authoritative journal head directly; otherwise it falls
// back to the verified convenience pointer active-profile.json.
func InspectProfileStatus(ctx context.Context, dir string) (ProfileStatus, error) {
	dirFD, canonical, err := OpenVerifiedDir(dir, false, 0700)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return ProfileStatus{State: "inactive", Active: false}, nil
		}
		return ProfileStatus{}, err
	}
	_ = unix.Close(dirFD)

	journalDir := filepath.Join(canonical, "journal")
	if _, err := os.Lstat(filepath.Join(journalDir, "STORE")); err == nil {
		if _, err := journalUsage(ctx, journalDir, journalWatermark); err != nil {
			return ProfileStatus{}, err
		}
		store, openErr := recordprims.Open(journalDir, recordprims.OpenOptions{})
		if openErr == nil {
			defer func() { _ = store.Close() }()
			if profileStreamExistsOnDisk(journalDir) {
				stream, streamErr := store.OpenStream(profileStreamID, recordprims.ModeRevisioned)
				if streamErr == nil {
					payload, nextRev, _, headErr := readStreamHead(ctx, stream)
					if headErr != nil {
						return ProfileStatus{}, headErr
					}
					if payload == nil {
						return ProfileStatus{State: "inactive", Active: false}, nil
					}
					st := statusFromPayload(*payload, nextRev-1)
					if !payload.Active {
						return st, nil
					}
					if err := verifyActivePayloadIntegrity(canonical, *payload); err != nil {
						return ProfileStatus{}, err
					}
					return st, nil
				}
				if !errors.Is(streamErr, recordprims.ErrLocked) {
					return ProfileStatus{}, streamErr
				}
			} else {
				return ProfileStatus{State: "inactive", Active: false}, nil
			}
		} else if !errors.Is(openErr, recordprims.ErrLocked) {
			return ProfileStatus{}, openErr
		}
	}
	return readPointerStatus(canonical)
}

func profileStreamExistsOnDisk(journalDir string) bool {
	sum := sha256.Sum256([]byte(profileStreamID))
	streamDirName := fmt.Sprintf("%x", sum[:])[:32]
	jpath := filepath.Join(journalDir, "streams", streamDirName, "journal.ndjson")
	info, err := os.Lstat(jpath)
	return err == nil && info.Mode().IsRegular()
}

func verifyActivePayloadIntegrity(stateDir string, payload ProfileActivationPayload) error {
	raw, err := readImmutablePackBytes(stateDir, payload.PackRevision)
	if err != nil {
		return err
	}
	pack, err := profiles.Load(bytes.NewReader(raw))
	if err != nil {
		return err
	}
	if pack.Revision() != payload.PackRevision {
		return fmt.Errorf("pack revision mismatch")
	}
	binding, err := pack.ResolveBinding(payload.Profile)
	if err != nil {
		return err
	}
	if binding.CompiledPolicyDigest != payload.CompiledPolicyDigest {
		return fmt.Errorf("compiled policy binding drifted from activated revision")
	}
	return nil
}

func (c *Coordinator) activateProfileLocked(ctx context.Context, rawPack []byte, binding profiles.Binding, opts ActivateProfileOptions) (ProfileStatus, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.store == nil {
		return ProfileStatus{}, fmt.Errorf("coordinator_closed")
	}
	if journalStopped(c.Status().State) {
		return ProfileStatus{}, fmt.Errorf("%s: operator intervention required", c.Status().State)
	}
	if err := c.admitJournal(ctx); err != nil {
		return ProfileStatus{}, err
	}
	stream, err := c.openProfileStream()
	if err != nil {
		return ProfileStatus{}, err
	}
	current, nextRev, supersedes, err := readStreamHead(ctx, stream)
	if err != nil {
		return ProfileStatus{}, err
	}
	if opts.IdempotencyKey != "" && current != nil && current.IdempotencyKey == opts.IdempotencyKey {
		if current.Active &&
			current.Profile == binding.Profile &&
			current.PackRevision == binding.PackRevision &&
			current.CompiledPolicyDigest == binding.CompiledPolicyDigest &&
			matchesExpectedRevision(opts.ExpectedRevision, current.PreviousRevision) {
			status := statusFromPayload(*current, nextRev-1)
			_ = writePointerStatus(c.owner.Dir, status)
			return status, nil
		}
		return ProfileStatus{}, fmt.Errorf("%w: key %q already used with different parameters", ErrIdempotencyConflict, opts.IdempotencyKey)
	}
	currentRev := ""
	if current != nil && current.Active {
		currentRev = current.PackRevision
	}
	if !matchesExpectedRevision(opts.ExpectedRevision, currentRev) {
		return ProfileStatus{}, fmt.Errorf("%w: active=%q expected=%q", ErrExpectedRevisionMismatch, currentRev, opts.ExpectedRevision)
	}
	// Persist immutable exact input bytes before appending the authoritative journal record.
	if err := persistImmutablePackBytes(c.owner.Dir, binding.PackRevision, rawPack); err != nil {
		return ProfileStatus{}, err
	}
	now := time.Now().UTC().Truncate(time.Microsecond).Format("2006-01-02T15:04:05.000000Z")
	payload := ProfileActivationPayload{
		Schema:               ProfileActivationSchema,
		Version:              0,
		Active:               true,
		Profile:              binding.Profile,
		PackRevision:         binding.PackRevision,
		PreviousRevision:     currentRev,
		ResolvedRoots:        binding.ResolvedRoots,
		VolumeObjectivePath:  binding.VolumeObjectivePath,
		CompiledPolicyDigest: binding.CompiledPolicyDigest,
		IdempotencyKey:       opts.IdempotencyKey,
		UpdatedAt:            now,
	}
	if err := c.appendProfileRecord(ctx, stream, nextRev, supersedes, payload, opts.IdempotencyKey); err != nil {
		return ProfileStatus{}, err
	}
	status := statusFromPayload(payload, nextRev)
	_ = writePointerStatus(c.owner.Dir, status)
	return status, nil
}

func (c *Coordinator) deactivateProfileLocked(ctx context.Context, expectedRevision, idempotencyKey string) (ProfileStatus, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.store == nil {
		return ProfileStatus{}, fmt.Errorf("coordinator_closed")
	}
	if journalStopped(c.Status().State) {
		return ProfileStatus{}, fmt.Errorf("%s: operator intervention required", c.Status().State)
	}
	if err := c.admitJournal(ctx); err != nil {
		return ProfileStatus{}, err
	}
	stream, err := c.openProfileStream()
	if err != nil {
		return ProfileStatus{}, err
	}
	current, nextRev, supersedes, err := readStreamHead(ctx, stream)
	if err != nil {
		return ProfileStatus{}, err
	}
	if idempotencyKey != "" && current != nil && current.IdempotencyKey == idempotencyKey {
		if !current.Active && matchesExpectedRevision(expectedRevision, current.PreviousRevision) {
			status := statusFromPayload(*current, nextRev-1)
			_ = writePointerStatus(c.owner.Dir, status)
			return status, nil
		}
		return ProfileStatus{}, fmt.Errorf("%w: key %q already used with different parameters", ErrIdempotencyConflict, idempotencyKey)
	}
	currentRev := ""
	if current != nil && current.Active {
		currentRev = current.PackRevision
	}
	if !matchesExpectedRevision(expectedRevision, currentRev) {
		return ProfileStatus{}, fmt.Errorf("%w: active=%q expected=%q", ErrExpectedRevisionMismatch, currentRev, expectedRevision)
	}
	now := time.Now().UTC().Truncate(time.Microsecond).Format("2006-01-02T15:04:05.000000Z")
	payload := ProfileActivationPayload{
		Schema:           ProfileActivationSchema,
		Version:          0,
		Active:           false,
		PreviousRevision: currentRev,
		IdempotencyKey:   idempotencyKey,
		UpdatedAt:        now,
	}
	if err := c.appendProfileRecord(ctx, stream, nextRev, supersedes, payload, idempotencyKey); err != nil {
		return ProfileStatus{}, err
	}
	status := statusFromPayload(payload, nextRev)
	_ = writePointerStatus(c.owner.Dir, status)
	return status, nil
}

func matchesExpectedRevision(expected, actual string) bool {
	switch expected {
	case "":
		return true
	case "none":
		return actual == ""
	default:
		return expected == actual
	}
}

func (c *Coordinator) openProfileStream() (*recordprims.Stream, error) {
	if c.profileStream != nil {
		return c.profileStream, nil
	}
	st, err := c.store.OpenStream(profileStreamID, recordprims.ModeRevisioned)
	if err != nil {
		c.setState("journal_unavailable")
		return nil, err
	}
	c.profileStream = st
	return st, nil
}

func (c *Coordinator) appendProfileRecord(
	ctx context.Context,
	stream *recordprims.Stream,
	revision uint64,
	supersedes *recordprims.Pin,
	payload ProfileActivationPayload,
	idempotencyKey string,
) error {
	record := map[string]any{
		"v":                1,
		"stream":           profileStreamID,
		"record_id":        profileRecordID,
		"revision":         revision,
		"payload_contract": "contract: profile-activation/v0",
		"payload":          payload,
		"produced_at": map[string]string{
			"instant": payload.UpdatedAt,
			"source":  "producer_clock",
		},
		"provenance": map[string]string{
			"actor_kind": "process",
			"actor":      "profile-activator",
		},
		"classification": map[string]string{
			"retention":   "standard",
			"sensitivity": "1-confidential",
		},
	}
	if supersedes != nil {
		record["supersedes"] = map[string]any{
			"revision": supersedes.Revision,
			"digest":   supersedes.Digest,
		}
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	key := idempotencyKey
	if key == "" {
		key = uuid.NewString()
	}
	receipt, err := stream.Append(ctx, recordprims.Write{Record: raw, IdempotencyKey: key})
	if err != nil {
		c.setState("journal_unavailable")
		return err
	}
	if receipt.Outcome != recordprims.OutcomeAppended && receipt.Outcome != recordprims.OutcomeRevised {
		c.setState("journal_unavailable")
		return fmt.Errorf("journal outcome %s", receipt.Outcome)
	}
	return nil
}

type frameCollector struct {
	lastFrame []byte
	hasFrame  bool
}

func (fc *frameCollector) Frame(_ context.Context, h recordprims.FrameHeader, r io.Reader) error {
	if h.Kind != recordprims.FrameKindRecord {
		return nil
	}
	b, err := io.ReadAll(io.LimitReader(r, profiles.MaxDocumentBytes+65536))
	if err != nil {
		return err
	}
	fc.lastFrame = b
	fc.hasFrame = true
	return nil
}

func validateActivationPayloadFormat(p ProfileActivationPayload) error {
	if p.Schema != ProfileActivationSchema || p.Version != 0 {
		return fmt.Errorf("invalid profile activation record schema %q version %d", p.Schema, p.Version)
	}
	if !p.Active {
		return nil
	}
	if p.Profile == "" {
		return fmt.Errorf("active profile record missing profile name")
	}
	if _, err := packDigestHex(p.PackRevision); err != nil {
		return err
	}
	if _, err := packDigestHex(p.CompiledPolicyDigest); err != nil {
		return err
	}
	if p.VolumeObjectivePath != "" && !filepath.IsAbs(p.VolumeObjectivePath) {
		return fmt.Errorf("active profile record has non-absolute volume_objective_path %q", p.VolumeObjectivePath)
	}
	return nil
}

func readStreamHead(ctx context.Context, stream *recordprims.Stream) (*ProfileActivationPayload, uint64, *recordprims.Pin, error) {
	head, ok, err := stream.Head(profileRecordID)
	if err != nil {
		return nil, 0, nil, err
	}
	if !ok {
		return nil, 1, nil, nil
	}
	sink := &frameCollector{}
	res, err := stream.Release(ctx, recordprims.UseRequest{
		Purpose: recordprims.PurposeServe,
		Closure: recordprims.ClosureFullContent,
	}, sink)
	if err != nil {
		return nil, 0, nil, err
	}
	if res.Outcome != recordprims.ReleaseReleased || !sink.hasFrame {
		return nil, 0, nil, fmt.Errorf("profile stream head unreadable: %s", res.Outcome)
	}
	var env struct {
		RecordID string                   `json:"record_id"`
		Revision uint64                   `json:"revision"`
		Payload  ProfileActivationPayload `json:"payload"`
	}
	if err := json.Unmarshal(sink.lastFrame, &env); err != nil {
		return nil, 0, nil, err
	}
	if env.RecordID != profileRecordID || env.Revision != head.Pin.Revision {
		return nil, 0, nil, fmt.Errorf("profile stream head mismatch")
	}
	if err := validateActivationPayloadFormat(env.Payload); err != nil {
		return nil, 0, nil, err
	}
	pin := head.Pin
	return &env.Payload, head.Pin.Revision + 1, &pin, nil
}

func statusFromPayload(p ProfileActivationPayload, rev uint64) ProfileStatus {
	if !p.Active {
		return ProfileStatus{
			State:            "inactive",
			Active:           false,
			JournalRevision:  rev,
			PreviousRevision: p.PreviousRevision,
			UpdatedAt:        p.UpdatedAt,
		}
	}
	return ProfileStatus{
		State:                "active",
		Active:               true,
		JournalRevision:      rev,
		Profile:              p.Profile,
		PackRevision:         p.PackRevision,
		PreviousRevision:     p.PreviousRevision,
		ResolvedRoots:        append([]string(nil), p.ResolvedRoots...),
		VolumeObjectivePath:  p.VolumeObjectivePath,
		CompiledPolicyDigest: p.CompiledPolicyDigest,
		UpdatedAt:            p.UpdatedAt,
	}
}

func (c *Coordinator) loadAuthoritativeProfile(ctx context.Context) (ProfileStatus, *profiles.Pack, *ProfileActivationPayload, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.store == nil {
		return ProfileStatus{}, nil, nil, fmt.Errorf("coordinator_closed")
	}
	stream, err := c.openProfileStream()
	if err != nil {
		return ProfileStatus{}, nil, nil, err
	}
	payload, nextRev, _, err := readStreamHead(ctx, stream)
	if err != nil {
		return ProfileStatus{}, nil, nil, err
	}
	if payload == nil {
		st := ProfileStatus{State: "inactive", Active: false}
		_ = writePointerStatus(c.owner.Dir, st)
		return st, nil, nil, nil
	}
	st := statusFromPayload(*payload, nextRev-1)
	if !payload.Active {
		_ = writePointerStatus(c.owner.Dir, st)
		return st, nil, payload, nil
	}
	raw, err := readImmutablePackBytes(c.owner.Dir, payload.PackRevision)
	if err != nil {
		return st, nil, payload, err
	}
	pack, err := profiles.Load(bytes.NewReader(raw))
	if err != nil {
		return st, nil, payload, err
	}
	if pack.Revision() != payload.PackRevision {
		return st, nil, payload, fmt.Errorf("pack revision mismatch")
	}
	binding, err := pack.ResolveBinding(payload.Profile)
	if err != nil {
		return st, nil, payload, err
	}
	if binding.CompiledPolicyDigest != payload.CompiledPolicyDigest {
		return st, nil, payload, fmt.Errorf("compiled policy binding drifted from activated revision")
	}
	_ = writePointerStatus(c.owner.Dir, st)
	return st, pack, payload, nil
}

// EvaluateProposals evaluates the currently active profile and caches the result
// for GET /v1/proposals. If no profile is active, proposals remain in state
// "not_evaluated". If the active profile body is missing, corrupt, drifted, or
// evaluation is incomplete, proposals transition to "unavailable" or "partial"
// — never an empty successful "evaluated" list.
func (c *Coordinator) EvaluateProposals(ctx context.Context) ProposalsResponse {
	st, pack, payload, err := c.loadAuthoritativeProfile(ctx)
	if err != nil {
		resp := ProposalsResponse{
			Protocol:   ProtocolVersion,
			State:      "unavailable",
			Items:      []ProposalItem{},
			NextCursor: nil,
		}
		if payload != nil && validateActivationPayloadFormat(*payload) == nil {
			resp.Profile = payload.Profile
			resp.PackRevision = payload.PackRevision
			resp.CompiledPolicyDigest = payload.CompiledPolicyDigest
		}
		c.proposals.Store(&resp)
		return cloneProposals(resp)
	}
	if !st.Active || pack == nil || payload == nil {
		resp := ProposalsResponse{
			Protocol:   ProtocolVersion,
			State:      "not_evaluated",
			Items:      []ProposalItem{},
			NextCursor: nil,
		}
		c.proposals.Store(&resp)
		return cloneProposals(resp)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	statusSnap := c.Status()
	samplePath, _ := c.lastSamplePath.Load().(string)
	sampler := c.sampler
	capacityRecordID := ""
	if statusSnap.State == "observing" && statusSnap.Sample != nil && payload.VolumeObjectivePath != "" &&
		samplePath != "" && filepath.Clean(samplePath) == filepath.Clean(payload.VolumeObjectivePath) {
		sampler = &cachedOrLiveSampler{
			cachedPath: samplePath,
			sample:     *statusSnap.Sample,
			fallback:   c.sampler,
		}
		capacityRecordID = statusSnap.RecordID
	}
	preview, err := pack.PreviewWithOptions(ctx, payload.Profile, profiles.PreviewOptions{
		Sampler: sampler,
		Now:     now,
	})
	if err != nil {
		resp := ProposalsResponse{
			Protocol:             ProtocolVersion,
			State:                "unavailable",
			Profile:              payload.Profile,
			PackRevision:         payload.PackRevision,
			CompiledPolicyDigest: payload.CompiledPolicyDigest,
			EvaluatedAt:          now.Format("2006-01-02T15:04:05.000000Z"),
			Items:                []ProposalItem{},
			NextCursor:           nil,
		}
		c.proposals.Store(&resp)
		return cloneProposals(resp)
	}
	state := "evaluated"
	if preview.State != "complete" {
		state = "partial"
	}
	if preview.VolumeObjective != nil && preview.VolumeObjective.Outcome == profiles.ObjectiveOutcomeUnknown {
		state = "partial"
	}
	items := make([]ProposalItem, 0, len(preview.Recipes))
	for _, r := range preview.Recipes {
		if r.Budget != nil && r.Budget.Status == profiles.BudgetStatusUnknown {
			state = "partial"
		}
		roots := append([]string{}, r.Roots...)
		cands := append([]profiles.Candidate{}, r.Candidates...)
		warns := append([]string{}, r.Warnings...)
		items = append(items, ProposalItem{
			RecipeID:             r.ID,
			State:                r.State,
			Roots:                roots,
			ProposedItemsDigest:  r.ProposedItemsDigest,
			ProposedItemsCount:   r.ProposedItemsCount,
			ProposedReclaimBytes: r.ProposedReclaimBytes,
			Budget:               r.Budget,
			Candidates:           cands,
			Warnings:             warns,
		})
	}
	resp := ProposalsResponse{
		Protocol:             ProtocolVersion,
		State:                state,
		Profile:              payload.Profile,
		PackRevision:         payload.PackRevision,
		CompiledPolicyDigest: payload.CompiledPolicyDigest,
		CapacityRecordID:     capacityRecordID,
		EvaluatedAt:          now.Format("2006-01-02T15:04:05.000000Z"),
		ExecutionAuthorized:  false,
		VolumeObjective:      preview.VolumeObjective,
		Items:                items,
		NextCursor:           nil,
	}
	if raw, err := json.Marshal(resp); err != nil || len(raw) > MaxResponseBytes {
		unavail := ProposalsResponse{
			Protocol:             ProtocolVersion,
			State:                "unavailable",
			Profile:              payload.Profile,
			PackRevision:         payload.PackRevision,
			CompiledPolicyDigest: payload.CompiledPolicyDigest,
			EvaluatedAt:          now.Format("2006-01-02T15:04:05.000000Z"),
			Items:                []ProposalItem{},
			NextCursor:           nil,
		}
		c.proposals.Store(&unavail)
		return cloneProposals(unavail)
	}
	c.proposals.Store(&resp)
	return cloneProposals(resp)
}

func (c *Coordinator) Proposals() ProposalsResponse {
	p := c.proposals.Load()
	if p == nil {
		return ProposalsResponse{
			Protocol:   ProtocolVersion,
			State:      "not_evaluated",
			Items:      []ProposalItem{},
			NextCursor: nil,
		}
	}
	if journalStopped(c.Status().State) && p.State != "not_evaluated" {
		return ProposalsResponse{
			Protocol:             ProtocolVersion,
			State:                "unavailable",
			Profile:              p.Profile,
			PackRevision:         p.PackRevision,
			CompiledPolicyDigest: p.CompiledPolicyDigest,
			EvaluatedAt:          p.EvaluatedAt,
			Items:                []ProposalItem{},
			NextCursor:           nil,
		}
	}
	return cloneProposals(*p)
}

func cloneProposals(in ProposalsResponse) ProposalsResponse {
	out := in
	if in.VolumeObjective != nil {
		vo := *in.VolumeObjective
		out.VolumeObjective = &vo
	}
	out.Items = make([]ProposalItem, len(in.Items))
	for i, item := range in.Items {
		cp := item
		cp.Roots = append([]string{}, item.Roots...)
		cp.Candidates = append([]profiles.Candidate{}, item.Candidates...)
		cp.Warnings = append([]string{}, item.Warnings...)
		if item.Budget != nil {
			b := *item.Budget
			cp.Budget = &b
		}
		out.Items[i] = cp
	}
	return out
}

type cachedOrLiveSampler struct {
	cachedPath string
	sample     observe.Sample
	fallback   observe.Sampler
}

func (s *cachedOrLiveSampler) Sample(ctx context.Context, path string) (observe.Sample, error) {
	if filepath.Clean(path) == filepath.Clean(s.cachedPath) {
		return s.sample, nil
	}
	if s.fallback != nil {
		return s.fallback.Sample(ctx, path)
	}
	return (observe.CapacitySampler{}).Sample(ctx, path)
}

func packDigestHex(revision string) (string, error) {
	if !strings.HasPrefix(revision, "sha256:") || len(revision) != len("sha256:")+64 {
		return "", fmt.Errorf("invalid pack revision %q", revision)
	}
	hexPart := strings.TrimPrefix(revision, "sha256:")
	for _, r := range hexPart {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return "", fmt.Errorf("invalid pack revision %q", revision)
		}
	}
	return hexPart, nil
}

func persistImmutablePackBytes(stateDir, revision string, raw []byte) error {
	hexPart, err := packDigestHex(revision)
	if err != nil {
		return err
	}
	if fmt.Sprintf("sha256:%x", sha256.Sum256(raw)) != revision {
		return fmt.Errorf("pack content does not match revision %s", revision)
	}
	packsDir := filepath.Join(stateDir, "packs")
	dirFD, _, err := OpenVerifiedDir(packsDir, true, 0700)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(dirFD) }()
	fileName := hexPart + ".json"
	if existing, err := readRegularFileAt(dirFD, fileName, profiles.MaxDocumentBytes); err == nil {
		if bytes.Equal(existing, raw) {
			return nil
		}
		return fmt.Errorf("existing pack body for %s failed integrity check", revision)
	} else if !errors.Is(err, unix.ENOENT) {
		return fmt.Errorf("inspect existing pack body for %s: %w", revision, err)
	}
	return writeAtomicFileAt(dirFD, fileName, raw)
}

func readImmutablePackBytes(stateDir, revision string) ([]byte, error) {
	hexPart, err := packDigestHex(revision)
	if err != nil {
		return nil, err
	}
	packsDir := filepath.Join(stateDir, "packs")
	dirFD, _, err := OpenVerifiedDir(packsDir, false, 0700)
	if err != nil {
		return nil, err
	}
	defer func() { _ = unix.Close(dirFD) }()
	raw, err := readRegularFileAt(dirFD, hexPart+".json", profiles.MaxDocumentBytes)
	if err != nil {
		return nil, err
	}
	if fmt.Sprintf("sha256:%x", sha256.Sum256(raw)) != revision {
		return nil, fmt.Errorf("corrupt pack body for %s", revision)
	}
	return raw, nil
}

func writePointerStatus(stateDir string, st ProfileStatus) error {
	dirFD, _, err := OpenVerifiedDir(stateDir, false, 0700)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(dirFD) }()
	raw, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return writeAtomicFileAt(dirFD, profilePointerFile, raw)
}

func readPointerStatus(stateDir string) (ProfileStatus, error) {
	dirFD, _, err := OpenVerifiedDir(stateDir, false, 0700)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return ProfileStatus{State: "inactive", Active: false}, nil
		}
		return ProfileStatus{}, err
	}
	defer func() { _ = unix.Close(dirFD) }()
	raw, err := readRegularFileAt(dirFD, profilePointerFile, 65536)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return ProfileStatus{State: "inactive", Active: false}, nil
		}
		return ProfileStatus{}, err
	}
	var st ProfileStatus
	if err := json.Unmarshal(raw, &st); err != nil {
		return ProfileStatus{}, err
	}
	return st, nil
}

func readRegularFileAt(dirFD int, name string, maxBytes int64) ([]byte, error) {
	var preSt unix.Stat_t
	if err := unix.Fstatat(dirFD, name, &preSt, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return nil, err
	}
	if int64(preSt.Uid) != int64(os.Getuid()) || preSt.Mode&unix.S_IFMT != unix.S_IFREG || preSt.Mode&0077 != 0 || preSt.Nlink != 1 {
		return nil, fmt.Errorf("unsafe state file %s", name)
	}
	if preSt.Size < 0 || preSt.Size > maxBytes {
		return nil, fmt.Errorf("state file %s size out of bounds", name)
	}
	fd, err := unix.Openat(dirFD, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), name)
	defer func() { _ = f.Close() }()
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return nil, err
	}
	if st.Dev != preSt.Dev || st.Ino != preSt.Ino ||
		int64(st.Uid) != int64(os.Getuid()) || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&0077 != 0 || st.Nlink != 1 {
		return nil, fmt.Errorf("unsafe state file %s", name)
	}
	if st.Size < 0 || st.Size > maxBytes {
		return nil, fmt.Errorf("state file %s size out of bounds", name)
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) != st.Size {
		return nil, fmt.Errorf("state file %s changed during read", name)
	}
	return raw, nil
}

func writeAtomicFileAt(dirFD int, name string, data []byte) error {
	tmpName := fmt.Sprintf(".%s.tmp.%d", name, time.Now().UnixNano())
	_ = unix.Unlinkat(dirFD, tmpName, 0)
	fd, err := unix.Openat(dirFD, tmpName, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), tmpName)
	cleanup := true
	defer func() {
		_ = f.Close()
		if cleanup {
			_ = unix.Unlinkat(dirFD, tmpName, 0)
		}
	}()
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := unix.Renameat(dirFD, tmpName, dirFD, name); err != nil {
		return err
	}
	cleanup = false
	_ = unix.Fsync(dirFD)
	return nil
}
