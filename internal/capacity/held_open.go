package capacity

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

const (
	lsofPath              = "/usr/sbin/lsof"
	heldOpenSource        = "lsof_plus_L1"
	heldOpenEntryLimit    = 200
	heldOpenHolderLimit   = 16
	defaultHeldOpenOutput = 16 << 20
)

type heldOpenProcess struct {
	pid        int
	uid        *int
	executable string
}

type heldOpenFile struct {
	device string
	inode  string
	size   int64
	path   string
	holder heldOpenProcess
}

type heldOpenParseResult struct {
	files             []heldOpenFile
	processesExamined int
	parseOmissions    int
}

func collectHeldOpen(
	ctx context.Context,
	runner CommandRunner,
	disclosure string,
	targetTree string,
) HeldOpenPlane {
	if disclosure == "" {
		disclosure = "none"
	}
	privilege := "unprivileged"
	if os.Geteuid() == 0 {
		privilege = "elevated"
	}
	base := HeldOpenPlane{
		CollectionStatus: StatusUnavailable,
		Source:           heldOpenSource,
		Coverage:         emptyCoverage(),
		Disclosure:       disclosure,
		Privilege:        privilege,
	}
	raw, runErr := runner.Run(ctx, lsofPath, "-nP", "+L1", "-F0pcuDiksn")
	if runErr != nil && len(raw) == 0 {
		base.Coverage.Gaps = append(base.Coverage.Gaps, heldOpenRunGap(runErr))
		return base
	}

	parsed := parseHeldOpenLsof(raw)
	plane := buildHeldOpenPlane(parsed, disclosure, targetTree, privilege)
	if runErr != nil {
		plane.CollectionStatus = StatusPartial
		plane.Coverage.Gaps = append(plane.Coverage.Gaps, heldOpenRunGap(runErr))
		var typed *RunError
		if errors.As(runErr, &typed) && typed.Code == "permission_denied" {
			// The runner can classify a permission warning, but it cannot
			// determine whether the denied entity was a process, path, or
			// object. Leave the process quantity unclaimed.
			plane.ProcessesDenied = nil
		}
	}
	return plane
}

func heldOpenRunGap(err error) CoverageGap {
	gap := gapForRunError(err, "held_open")
	gap.Detail = map[string]string{
		"timeout":           "held-open collection did not complete before its deadline",
		"command_missing":   "the required held-open collector is not installed",
		"permission_denied": "held-open collection was denied by current privileges",
		"command_failed":    "held-open collection failed",
		"truncated":         "held-open command output exceeded its bounded capture; at least one object was omitted",
	}[gap.Code]
	if gap.Code == "truncated" {
		one := 1
		gap.OmittedObjects = &one
	}
	return gap
}

func parseHeldOpenLsof(raw []byte) heldOpenParseResult {
	result := heldOpenParseResult{}
	var process heldOpenProcess
	seenProcesses := make(map[int]struct{})
	var fileFields map[byte]string
	flushFile := func() {
		if fileFields == nil {
			return
		}
		size, sizeErr := strconv.ParseInt(fileFields['s'], 10, 64)
		if process.executable == "" || fileFields['D'] == "" || fileFields['i'] == "" ||
			sizeErr != nil || size < 0 {
			result.parseOmissions++
		} else {
			result.files = append(result.files, heldOpenFile{
				device: fileFields['D'],
				inode:  fileFields['i'],
				size:   size,
				path:   fileFields['n'],
				holder: process,
			})
		}
		fileFields = nil
	}

	for _, field := range bytes.Split(raw, []byte{0}) {
		for len(field) > 0 && field[0] == '\n' {
			field = field[1:]
		}
		if len(field) == 0 {
			continue
		}
		identifier := field[0]
		value := string(field[1:])
		switch identifier {
		case 'p':
			flushFile()
			pid, err := strconv.Atoi(value)
			if err != nil || pid < 0 {
				process = heldOpenProcess{}
				result.parseOmissions++
				continue
			}
			process = heldOpenProcess{pid: pid}
			seenProcesses[pid] = struct{}{}
		case 'c':
			process.executable = normalizeExecutable(value)
		case 'u':
			if uid, err := strconv.Atoi(value); err == nil && uid >= 0 {
				process.uid = &uid
			}
		case 'f':
			flushFile()
			fileFields = make(map[byte]string)
		default:
			if fileFields != nil {
				fileFields[identifier] = value
			}
		}
	}
	flushFile()
	result.processesExamined = len(seenProcesses)
	return result
}

func buildHeldOpenPlane(
	parsed heldOpenParseResult,
	disclosure string,
	targetTree string,
	privilege string,
) HeldOpenPlane {
	type aggregate struct {
		device  string
		inode   string
		size    int64
		path    string
		holders map[int]HeldOpenHolder
	}
	objects := make(map[string]*aggregate)
	holderPIDs := make(map[int]struct{})
	relationships := 0
	for _, file := range parsed.files {
		key := file.device + "\x00" + file.inode
		object := objects[key]
		if object == nil {
			object = &aggregate{
				device:  file.device,
				inode:   file.inode,
				size:    file.size,
				path:    file.path,
				holders: make(map[int]HeldOpenHolder),
			}
			objects[key] = object
		} else {
			if file.size > object.size {
				object.size = file.size
			}
			if object.path == "" {
				object.path = file.path
			}
		}
		relationships++
		holderPIDs[file.holder.pid] = struct{}{}
		if _, exists := object.holders[file.holder.pid]; !exists {
			object.holders[file.holder.pid] = HeldOpenHolder{
				PID:            file.holder.pid,
				UID:            file.holder.uid,
				Executable:     file.holder.executable,
				InterfaceGuess: interfaceGuess(file.holder.executable),
			}
		}
	}

	keys := make([]string, 0, len(objects))
	for key := range objects {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		left, right := objects[keys[i]], objects[keys[j]]
		if left.size != right.size {
			return left.size > right.size
		}
		return keys[i] < keys[j]
	})

	entries := make([]HeldOpenObject, 0, min(len(keys), heldOpenEntryLimit))
	var total int64
	omittedObjects := 0
	omittedHolders := 0
	for index, key := range keys {
		object := objects[key]
		total += object.size
		if index >= heldOpenEntryLimit {
			omittedObjects++
			continue
		}
		pids := make([]int, 0, len(object.holders))
		for pid := range object.holders {
			pids = append(pids, pid)
		}
		sort.Ints(pids)
		holders := make([]HeldOpenHolder, 0, min(len(pids), heldOpenHolderLimit))
		for holderIndex, pid := range pids {
			if holderIndex >= heldOpenHolderLimit {
				omittedHolders++
				continue
			}
			holders = append(holders, object.holders[pid])
		}
		entry := HeldOpenObject{
			Device:       object.device,
			Inode:        object.inode,
			PathPresent:  object.path != "",
			RootClass:    classifyHeldOpenRoot(object.path, targetTree),
			LogicalBytes: heldOpenClaim(object.size, StatusMeasured),
			Holders:      holders,
		}
		if disclosure == "full" && object.path != "" {
			entry.Path = discloseHeldOpenPath(object.path)
		}
		entries = append(entries, entry)
	}

	// Assign each object to one deterministic executable/interface group.
	// Group totals therefore partition objects instead of double-counting an
	// object held by processes from several interfaces.
	groupObjects := make(map[string]map[string]int64)
	for _, key := range keys {
		object := objects[key]
		candidates := make([]string, 0, len(object.holders))
		for _, holder := range object.holders {
			groupKey := holder.Executable
			if holder.InterfaceGuess != "" {
				groupKey = holder.InterfaceGuess + ":" + holder.Executable
			}
			candidates = append(candidates, groupKey)
		}
		sort.Strings(candidates)
		if len(candidates) == 0 {
			continue
		}
		groupKey := candidates[0]
		if groupObjects[groupKey] == nil {
			groupObjects[groupKey] = make(map[string]int64)
		}
		groupObjects[groupKey][key] = object.size
	}

	groupKeys := make([]string, 0, len(groupObjects))
	for key := range groupObjects {
		groupKeys = append(groupKeys, key)
	}
	sort.Strings(groupKeys)
	groups := make([]HeldOpenGroup, 0, min(len(groupKeys), heldOpenEntryLimit))
	explicitGroupLimit := len(groupKeys)
	if explicitGroupLimit > heldOpenEntryLimit {
		explicitGroupLimit = heldOpenEntryLimit - 1
	}
	for _, key := range groupKeys[:explicitGroupLimit] {
		groupTotal := int64(0)
		for _, size := range groupObjects[key] {
			groupTotal += size
		}
		claim := heldOpenClaim(groupTotal, StatusMeasured)
		groups = append(groups, HeldOpenGroup{
			Key:           key,
			UniqueObjects: len(groupObjects[key]),
			LogicalBytes:  &claim,
		})
	}
	if explicitGroupLimit < len(groupKeys) {
		otherKey := boundedOtherGroupKey(groupObjects)
		otherObjects := 0
		var otherTotal int64
		for _, key := range groupKeys[explicitGroupLimit:] {
			otherObjects += len(groupObjects[key])
			for _, size := range groupObjects[key] {
				otherTotal += size
			}
		}
		claim := heldOpenClaim(otherTotal, StatusMeasured)
		groups = append(groups, HeldOpenGroup{
			Key:           otherKey,
			UniqueObjects: otherObjects,
			LogicalBytes:  &claim,
		})
	}

	status := StatusMeasured
	gaps := make([]CoverageGap, 0, 2)
	if parsed.parseOmissions > 0 {
		status = StatusPartial
		count := parsed.parseOmissions
		gaps = append(gaps, CoverageGap{
			Code:           "parse_failed",
			Scope:          "held_open",
			Detail:         "held-open rows missing required typed identity or size fields were omitted",
			OmittedObjects: &count,
		})
	}
	if omittedObjects > 0 || omittedHolders > 0 {
		status = StatusPartial
		gap := CoverageGap{
			Code:   "truncated",
			Scope:  "held_open",
			Detail: "held-open report entry caps omitted detail; aggregate totals still cover parsed objects",
		}
		if omittedObjects > 0 {
			gap.OmittedObjects = &omittedObjects
		}
		if omittedHolders > 0 {
			gap.OmittedHolders = &omittedHolders
		}
		gaps = append(gaps, gap)
	}
	zero := 0
	uniqueObjects := len(objects)
	holderProcesses := len(holderPIDs)
	logical := heldOpenClaim(total, status)
	return HeldOpenPlane{
		CollectionStatus:  status,
		Source:            heldOpenSource,
		Coverage:          Coverage{Gaps: gaps},
		Disclosure:        disclosure,
		Privilege:         privilege,
		ProcessesExamined: &parsed.processesExamined,
		ProcessesDenied:   &zero,
		UniqueObjects:     &uniqueObjects,
		HolderProcesses:   &holderProcesses,
		Relationships:     &relationships,
		LogicalBytes:      &logical,
		Entries:           entries,
		Groups:            groups,
	}
}

func boundedOtherGroupKey(groups map[string]map[string]int64) string {
	const base = "other:group-overflow"
	if _, exists := groups[base]; !exists {
		return base
	}
	for suffix := 2; ; suffix++ {
		candidate := fmt.Sprintf("%s:%d", base, suffix)
		if _, exists := groups[candidate]; !exists {
			return candidate
		}
	}
}

func heldOpenClaim(bytes int64, status string) ByteClaim {
	value := bytes
	return ByteClaim{
		Status: status,
		Bytes:  &value,
		Human:  HumanBytes(bytes),
		Basis:  "lsof_file_size_dedup_device_inode",
		Bound:  "lower",
		Source: heldOpenSource,
	}
}

func normalizeExecutable(value string) string {
	if index := strings.LastIndexAny(value, `/\`); index >= 0 {
		value = value[index+1:]
	}
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == '/' || r == '\\' {
			return -1
		}
		return r
	}, value)
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}
	return value
}

func interfaceGuess(executable string) string {
	lower := strings.ToLower(executable)
	switch {
	case strings.Contains(lower, "codex"):
		return "codex"
	case strings.Contains(lower, "claude"):
		return "claude"
	case strings.Contains(lower, "cursor"):
		return "cursor"
	case lower == "code" || strings.Contains(lower, "visual studio code"):
		return "vscode"
	default:
		return ""
	}
}

func classifyHeldOpenRoot(path, targetTree string) string {
	path = cleanDeletedPath(path)
	if withinRoot(path, targetTree) {
		return "target_tree"
	}
	if home, err := os.UserHomeDir(); err == nil && withinRoot(path, home) {
		return "user_home"
	}
	for _, root := range []string{"/tmp", "/private/tmp", "/private/var/folders"} {
		if withinRoot(path, root) {
			return "system_temp"
		}
	}
	for _, root := range []string{
		"/System", "/Library", "/Applications", "/usr", "/bin", "/sbin", "/private/var",
	} {
		if withinRoot(path, root) {
			return "system"
		}
	}
	return "other"
}

func withinRoot(path, root string) bool {
	if path == "" || root == "" || !filepath.IsAbs(path) || !filepath.IsAbs(root) {
		return false
	}
	path = filepath.Clean(path)
	root = filepath.Clean(root)
	if path == root {
		return true
	}
	return strings.HasPrefix(path, root+string(filepath.Separator))
}

func cleanDeletedPath(path string) string {
	return strings.TrimSuffix(path, " (deleted)")
}

func discloseHeldOpenPath(path string) string {
	path = cleanDeletedPath(path)
	home, err := os.UserHomeDir()
	if err == nil && withinRoot(path, home) {
		relative, relErr := filepath.Rel(home, path)
		if relErr == nil {
			if relative == "." {
				return "~"
			}
			return filepath.Join("~", relative)
		}
	}
	return path
}

func validateHeldOpenDisclosure(value string) error {
	switch value {
	case "", "none", "full":
		return nil
	default:
		return fmt.Errorf("unsupported held-open disclosure %q", value)
	}
}
