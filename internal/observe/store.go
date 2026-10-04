package observe

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

// StateDocument is the versioned on-disk observe state.
type StateDocument struct {
	Schema  string                    `json:"$schema"`
	Version int                       `json:"version"`
	Volumes map[string]*VolumeRuntime `json:"volumes"` // key = VolumeID string
}

// Store persists observe state. Implementations must fail closed on corrupt data.
type Store interface {
	Load() (*StateDocument, error)
	Save(*StateDocument) error
}

// FileStore is a simple JSON file store (0600).
type FileStore struct {
	Path string
	mu   sync.Mutex
}

func (s *FileStore) Load() (*StateDocument, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := os.Open(s.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return emptyState(), nil
		}
		return nil, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() > MaxStateFileBytes {
		return nil, fmt.Errorf("observe state exceeds max size %d bytes", MaxStateFileBytes)
	}
	raw := make([]byte, info.Size())
	if _, err := io.ReadFull(f, raw); err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("observe state empty (incomparable baseline)")
	}
	var doc StateDocument
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("observe state corrupt: %w", err)
	}
	if doc.Version != StateFileVersion {
		return nil, fmt.Errorf("observe state version %d incompatible (want %d)", doc.Version, StateFileVersion)
	}
	if doc.Schema != "" && doc.Schema != StateSchemaID {
		return nil, fmt.Errorf("observe state schema %q incompatible", doc.Schema)
	}
	if doc.Volumes == nil {
		doc.Volumes = map[string]*VolumeRuntime{}
	}
	if err := validateStateSemantics(&doc); err != nil {
		return nil, fmt.Errorf("observe state corrupt: %w", err)
	}
	return &doc, nil
}

// validateStateSemantics rejects syntactically valid JSON that cannot safely drive
// growth/proposals (wrong map keys, empty volume ids, impossible sample pairs,
// over-cap registrations, overlong operator-controlled strings).
func validateStateSemantics(doc *StateDocument) error {
	if len(doc.Volumes) > MaxRegisteredVolumes {
		return fmt.Errorf("volume count %d exceeds max %d", len(doc.Volumes), MaxRegisteredVolumes)
	}
	for key, v := range doc.Volumes {
		if v == nil {
			return fmt.Errorf("nil volume entry for key %q", key)
		}
		if v.Volume.ID == "" {
			return fmt.Errorf("volume key %q has empty id", key)
		}
		if len(key) > MaxVolumeIDLen || len(v.Volume.ID) > MaxVolumeIDLen {
			return fmt.Errorf("volume id exceeds max length %d", MaxVolumeIDLen)
		}
		if string(v.Volume.ID) != key {
			return fmt.Errorf("volume map key %q != volume.id %q", key, v.Volume.ID)
		}
		if len(v.Volume.Label) > MaxLabelLen {
			return fmt.Errorf("volume %s label exceeds max length %d", key, MaxLabelLen)
		}
		if len(v.Volume.RegisterPath) > MaxRegisterPathLen {
			return fmt.Errorf("volume %s register_path exceeds max length %d", key, MaxRegisterPathLen)
		}
		if !validLevel(v.Level, true) || !validLevel(v.LastNotifiedLevel, true) {
			return fmt.Errorf("volume %s has invalid pressure level", key)
		}
		if !validCoverage(v.Coverage, true) {
			return fmt.Errorf("volume %s has invalid coverage %q", key, v.Coverage)
		}
		if len(v.LossReason) > MaxStateStringLen {
			return fmt.Errorf("volume %s loss_reason exceeds max length %d", key, MaxStateStringLen)
		}
		if !validLossReason(v.LossReason) {
			return fmt.Errorf("volume %s has invalid loss_reason %q", key, v.LossReason)
		}
		if v.NextSeq == 0 {
			return fmt.Errorf("volume %s has zero next_seq", key)
		}
		if v.ScanInFlight {
			return fmt.Errorf("volume %s persists runtime-only scan_in_flight", key)
		}
		if err := validateSamplePtr("last_success", v.LastSuccess, v.Volume.ID); err != nil {
			return err
		}
		if err := validateSamplePtr("last_attempt", v.LastAttempt, v.Volume.ID); err != nil {
			return err
		}
		if v.LastSuccess != nil && v.LastSuccess.Seq >= v.NextSeq {
			return fmt.Errorf("volume %s last_success.seq %d not less than next_seq %d", key, v.LastSuccess.Seq, v.NextSeq)
		}
	}
	return nil
}

func validateSamplePtr(field string, s *Sample, want VolumeID) error {
	if s == nil {
		return nil
	}
	if s.VolumeID == "" {
		return fmt.Errorf("%s has empty volume_id", field)
	}
	if len(s.VolumeID) > MaxVolumeIDLen {
		return fmt.Errorf("%s volume_id exceeds max length %d", field, MaxVolumeIDLen)
	}
	if len(s.MountPath) > MaxRegisterPathLen {
		return fmt.Errorf("%s mount_path exceeds max length %d", field, MaxRegisterPathLen)
	}
	// last_attempt may record a foreign identity after mismatch; last_success must match.
	if field == "last_success" && s.VolumeID != want {
		return fmt.Errorf("last_success volume_id %q != registered %q", s.VolumeID, want)
	}
	if s.Basis == "" {
		return fmt.Errorf("%s missing measurement basis", field)
	}
	if s.Basis != BasisStatfs {
		return fmt.Errorf("%s has invalid measurement basis %q", field, s.Basis)
	}
	if !validLevel(s.Level, false) {
		return fmt.Errorf("%s has invalid pressure level %q", field, s.Level)
	}
	if !validCoverage(s.Coverage, field != "last_success") {
		return fmt.Errorf("%s has invalid coverage %q", field, s.Coverage)
	}
	if len(s.Source) > MaxStateStringLen {
		return fmt.Errorf("%s source exceeds max length %d", field, MaxStateStringLen)
	}
	if !validSampleSource(s.Source) {
		return fmt.Errorf("%s has invalid source %q", field, s.Source)
	}
	if field == "last_success" {
		if s.Coverage == "" {
			return fmt.Errorf("%s missing coverage", field)
		}
		if s.CapturedAt.IsZero() {
			return fmt.Errorf("%s missing capture time", field)
		}
	}
	return nil
}

func validLevel(value string, allowEmpty bool) bool {
	return (allowEmpty && value == "") || value == LevelOK || value == LevelWarn || value == LevelCritical
}

func validCoverage(value string, allowEmpty bool) bool {
	if allowEmpty && value == "" {
		return true
	}
	switch value {
	case CoverageComplete, CoveragePartial, CoverageDegraded, CoverageIncomparable, CoverageUnavailable, CoverageIndeterminate:
		return true
	default:
		return false
	}
}

func validLossReason(value string) bool {
	switch value {
	case "", LossOverflow, LossSequenceGap, LossWatcherError, LossDroppedWork,
		LossCorruptState, LossIdentityShift, "alias_path_change", "sample_error", "incomplete_sample":
		return true
	default:
		return false
	}
}

func validSampleSource(value string) bool {
	switch value {
	case "", "capacity.SampleFilesystem", "sample_error", "identity_mismatch":
		return true
	default:
		return false
	}
}

func (s *FileStore) Save(doc *StateDocument) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if doc == nil {
		return fmt.Errorf("nil state document")
	}
	doc.Schema = StateSchemaID
	doc.Version = StateFileVersion
	if doc.Volumes == nil {
		doc.Volumes = map[string]*VolumeRuntime{}
	}
	if err := validateStateSemantics(doc); err != nil {
		return fmt.Errorf("refusing invalid observe state: %w", err)
	}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return err
	}
	tmp := s.Path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.Path)
}

// MemoryStore is an in-memory store for tests.
type MemoryStore struct {
	Doc *StateDocument
	Err error
}

func (m *MemoryStore) Load() (*StateDocument, error) {
	if m.Err != nil {
		return nil, m.Err
	}
	if m.Doc == nil {
		return emptyState(), nil
	}
	// shallow copy map pointers ok for tests
	cp := *m.Doc
	return &cp, nil
}

func (m *MemoryStore) Save(doc *StateDocument) error {
	if m.Err != nil {
		return m.Err
	}
	cp := *doc
	m.Doc = &cp
	return nil
}

func emptyState() *StateDocument {
	return &StateDocument{
		Schema:  StateSchemaID,
		Version: StateFileVersion,
		Volumes: map[string]*VolumeRuntime{},
	}
}
