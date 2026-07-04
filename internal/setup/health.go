// Governing: ADR-0020 (detection is a reusable package the CLI doctor and the
// desktop UI both call, not duplicated logic), SPEC-0013 REQ "Source detection"
// (the detection currently in internal/cli/doctor.go MUST be refactored into a
// shared package rather than duplicated).
//
// This file holds the pure archive- and attachment-health decision logic that
// used to live in internal/cli/doctor.go: how a stored attachment's rel_path
// resolves against its archive, how a Signal archive_root is classified, and
// how WhatsApp media references resolve. It is filesystem-injectable (statFn)
// and free of any presentation concern, so both the CLI report and the desktop
// Setup surface classify identically. doctor.go keeps thin, unexported wrappers
// over these so its report wording and exit behavior are byte-for-byte
// unchanged.
package setup

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/joestump/msgbrowse/internal/archivepath"
	"github.com/joestump/msgbrowse/internal/source"
	"github.com/joestump/msgbrowse/internal/whatsapp"
)

// Health is a source-agnostic health verdict for one archive/attachment check.
// It is the shared vocabulary the CLI doctor maps onto its ✓/⚠/✗ glyphs and the
// desktop UI maps onto its card states — the decision, not the presentation.
type Health int

const (
	// HealthOK: the check passed.
	HealthOK Health = iota
	// HealthWarn: a soft, non-fatal warning (some absolute/missing media, a
	// misnamed root) the user should know about but that does not fail the run.
	HealthWarn
	// HealthProblem: a hard problem (a non-copy-mode export, media not copied)
	// that the doctor reports as ✗ and that fails the run.
	HealthProblem
)

// String renders a stable token for logs and tests.
func (h Health) String() string {
	switch h {
	case HealthWarn:
		return "warn"
	case HealthProblem:
		return "problem"
	default:
		return "ok"
	}
}

// sprintf is fmt.Sprintf under a short name so the verdict strings, copied
// verbatim from the CLI doctor, stay compact and diff-clean.
func sprintf(format string, a ...any) string { return fmt.Sprintf(format, a...) }

// AttachmentSampleLimit caps how many image attachments a health check
// inspects; sampling keeps the scan fast on large archives while staying
// representative of a misconfigured export.
const AttachmentSampleLimit = 300

// WhatsAppMediaSampleLimit caps how many WhatsApp media references a health
// check samples from result.json, mirroring AttachmentSampleLimit's rationale.
const WhatsAppMediaSampleLimit = 300

// --- attachment classification ----------------------------------------------

// AttachmentClass is how a stored attachment rel_path resolves on disk.
type AttachmentClass int

const (
	// AttachPresent: rel_path resolves inside the archive and the file exists.
	AttachPresent AttachmentClass = iota
	// AttachAbsolute: rel_path is an absolute path (e.g. ~/Library expanded) —
	// a reference-only export pointing outside the archive. No media is copied.
	AttachAbsolute
	// AttachMissing: rel_path resolves inside the archive but the file is gone.
	AttachMissing
)

// ClassifyAttachment decides how one attachment's rel_path resolves. statFn is
// injected so callers/tests don't touch the filesystem (pass os.Stat in
// production).
//
// An absolute rel_path is the signature of a non-copy-mode iMessage export: the
// exporter wrote the original ~/Library/.../Attachments path rather than copying
// the file into the archive. The IsAbs short-circuit below is what catches these
// — archivepath.Contain does NOT reject an absolute path, it neutralizes the
// leading "/" and folds it UNDER the archive root, which would mis-classify it as
// present/missing rather than flagging the real problem. So the explicit IsAbs
// check must come first.
func ClassifyAttachment(src string, roots archivepath.Roots, convName, rel string, statFn func(string) (os.FileInfo, error)) AttachmentClass {
	if filepath.IsAbs(rel) {
		return AttachAbsolute
	}
	abs, ok := archivepath.Resolve(src, roots, convName, rel)
	if !ok {
		// Unresolvable for a non-absolute path means the relevant archive root is
		// unset/misconfigured; treat as missing so it still counts against health.
		return AttachMissing
	}
	if _, err := statFn(abs); err != nil {
		return AttachMissing
	}
	return AttachPresent
}

// AttachmentStats tallies classifications for one source.
type AttachmentStats struct {
	Present  int
	Absolute int
	Missing  int
}

// Add records one classification.
func (s *AttachmentStats) Add(c AttachmentClass) {
	switch c {
	case AttachAbsolute:
		s.Absolute++
	case AttachMissing:
		s.Missing++
	default:
		s.Present++
	}
}

// Total is the number of classified attachments.
func (s *AttachmentStats) Total() int { return s.Present + s.Absolute + s.Missing }

// Thresholds for AttachmentVerdict / WhatsAppMediaVerdict. Kept as named
// constants so the policy is visible and testable.
const (
	// AttachAbsoluteFailFraction: at/above this share of absolute paths, the
	// export is treated as definitively non-copy-mode (a hard problem).
	AttachAbsoluteFailFraction = 0.5
	// AttachMissingWarnFraction: at/above this share of missing-but-resolvable
	// files, escalate the wording (still a warning).
	AttachMissingWarnFraction = 0.25
)

// AttachmentVerdict turns one source's stats into a health verdict + hint. The
// headline case: a meaningful share of absolute iMessage paths means the export
// was not copy-mode. Missing files (resolvable but absent) are a softer warning.
// The hint wording is preserved verbatim from the CLI doctor so its report is
// unchanged.
func AttachmentVerdict(src string, s *AttachmentStats) (Health, string) {
	total := s.Total()
	if total == 0 {
		return HealthOK, ""
	}
	// A clear majority of attachments are absolute (or absent): the export almost
	// certainly skipped copy-mode. This is the high-value diagnosis.
	if s.Absolute > 0 && Fraction(s.Absolute, total) >= AttachAbsoluteFailFraction {
		if src == source.IMessage {
			return HealthProblem, sprintf(
				"%d iMessage attachments use absolute ~/Library paths — your imessage-exporter run wasn't copy-mode; "+
					"re-run with -c/--copy-method (e.g. `-c clone`), then `msgbrowse import --full`.", s.Absolute)
		}
		return HealthProblem, sprintf(
			"%d attachments store absolute paths pointing outside the archive; re-export with media copied into the archive, then `msgbrowse import --full`.", s.Absolute)
	}
	// Some absolute paths, or a meaningful share of missing files: warn.
	if s.Absolute > 0 {
		if src == source.IMessage {
			return HealthWarn, sprintf(
				"%d iMessage attachments use absolute ~/Library paths (non-copy-mode export); "+
					"re-run imessage-exporter with -c/--copy-method (e.g. `-c clone`) then `msgbrowse import --full` to browse them.", s.Absolute)
		}
		return HealthWarn, sprintf("%d attachments store absolute paths outside the archive; re-export with media copied in.", s.Absolute)
	}
	if s.Missing > 0 && Fraction(s.Missing, total) >= AttachMissingWarnFraction {
		return HealthWarn, sprintf(
			"%d of %d sampled attachments resolve inside the archive but the file is missing; the archive may be incomplete or moved.", s.Missing, total)
	}
	if s.Missing > 0 {
		return HealthWarn, sprintf("%d of %d sampled attachment file(s) are missing on disk.", s.Missing, total)
	}
	return HealthOK, ""
}

// --- Signal archive_root classification -------------------------------------

// exportDir mirrors ingest.ExportDir — the signal-export subdirectory holding
// per-conversation folders. Hardcoded here to keep this a dependency-light leaf
// package; it must stay in sync with ingest.ExportDir (as archivepath does).
const exportDir = "export"

// ArchiveRootKind classifies a Signal archive_root path.
type ArchiveRootKind int

const (
	// ArchiveRootOK: <root>/export exists and is a directory (correct).
	ArchiveRootOK ArchiveRootKind = iota
	// ArchiveRootPointsAtExport: the user pointed at export/ itself (or its
	// contents) — the classic mistake. Detected when <root>/export/export exists
	// OR <root> has no export/ subdir but is itself named "export".
	ArchiveRootPointsAtExport
	// ArchiveRootNoExport: a directory with no export/ subdir and not named
	// export — wrong directory entirely.
	ArchiveRootNoExport
)

// ClassifyArchiveRoot decides whether archive_root is correct, points at export/
// itself, or simply lacks export/. It is pure filesystem inspection so it can be
// unit-tested against a temp tree.
func ClassifyArchiveRoot(root string) ArchiveRootKind {
	exportSub := filepath.Join(root, exportDir)
	if info, err := os.Stat(exportSub); err == nil && info.IsDir() {
		// <root>/export exists. If <root>/export/export also exists, the user
		// passed one level too deep (…/Archive/export as the root).
		if info2, err2 := os.Stat(filepath.Join(exportSub, exportDir)); err2 == nil && info2.IsDir() {
			return ArchiveRootPointsAtExport
		}
		return ArchiveRootOK
	}
	// No export/ subdir. If the root itself is named "export", the user almost
	// certainly pointed at the export folder instead of its parent.
	if filepath.Base(filepath.Clean(root)) == exportDir {
		return ArchiveRootPointsAtExport
	}
	return ArchiveRootNoExport
}

// --- WhatsApp media classification ------------------------------------------

// WhatsAppMediaClass is how one raw media reference (media_base + data)
// resolves against the WhatsApp archive root.
type WhatsAppMediaClass int

const (
	// WhatsAppMediaPresent: the reference resolves inside the root and the
	// file exists.
	WhatsAppMediaPresent WhatsAppMediaClass = iota
	// WhatsAppMediaOutside: the reference is an absolute path outside the
	// root — media was referenced in place, not copied into the export.
	WhatsAppMediaOutside
	// WhatsAppMediaMissing: the reference resolves inside the root but the
	// file is not there (e.g. the media directory was not copied).
	WhatsAppMediaMissing
)

// ClassifyWhatsAppMedia decides how one media reference resolves. The full
// path is media_base + data (the exporter's own <base href> semantics); an
// absolute full path under the root is relativized (the parser stores it that
// way), an absolute path elsewhere is the reference-only signature. statFn is
// injected so callers/tests don't touch the filesystem (pass os.Stat in
// production).
func ClassifyWhatsAppMedia(root string, ref whatsapp.MediaRef, statFn func(string) (os.FileInfo, error)) WhatsAppMediaClass {
	full := ref.MediaBase + ref.Data
	if filepath.IsAbs(full) {
		rel, err := filepath.Rel(root, full)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return WhatsAppMediaOutside
		}
		full = rel
	}
	abs, ok := archivepath.Contain(root, full)
	if !ok {
		return WhatsAppMediaMissing
	}
	if _, err := statFn(abs); err != nil {
		return WhatsAppMediaMissing
	}
	return WhatsAppMediaPresent
}

// WhatsAppMediaStats tallies media-reference classifications.
type WhatsAppMediaStats struct {
	Present int
	Outside int
	Missing int
}

// Add records one classification.
func (s *WhatsAppMediaStats) Add(c WhatsAppMediaClass) {
	switch c {
	case WhatsAppMediaOutside:
		s.Outside++
	case WhatsAppMediaMissing:
		s.Missing++
	default:
		s.Present++
	}
}

// Total is the number of classified references.
func (s *WhatsAppMediaStats) Total() int { return s.Present + s.Outside + s.Missing }

// WhatsAppMediaVerdict turns the sampled stats into a health verdict +
// remediation. Majority-outside and majority-missing are both the problem-level
// "the export has no usable media" diagnosis (REQ-0009-009's missing-media
// scenario); smaller shares warn. The hint is platform-aware via device. Wording
// is preserved verbatim from the CLI doctor.
func WhatsAppMediaVerdict(device string, s *WhatsAppMediaStats) (Health, string) {
	total := s.Total()
	if total == 0 {
		return HealthOK, ""
	}
	rerun := sprintf("re-run the exporter with the media folder present so it is copied under whatsapp_archive_root, then `msgbrowse import --full`; %s", WhatsAppBackupHint(device))
	if s.Outside > 0 && Fraction(s.Outside, total) >= AttachAbsoluteFailFraction {
		return HealthProblem, sprintf("%d media reference(s) point outside the archive root (absolute media_base) — media was not copied into the export; %s", s.Outside, rerun)
	}
	if s.Missing > 0 && Fraction(s.Missing, total) >= AttachAbsoluteFailFraction {
		return HealthProblem, sprintf("%d of %d sampled media reference(s) are not present under the root — the media directory was not copied; %s", s.Missing, total, rerun)
	}
	if s.Outside > 0 {
		return HealthWarn, sprintf("%d media reference(s) point outside the archive root; %s", s.Outside, rerun)
	}
	if s.Missing > 0 {
		return HealthWarn, sprintf("%d of %d sampled media reference file(s) are missing under the root; the export may be partial. %s", s.Missing, total, rerun)
	}
	return HealthOK, ""
}

// AbsMediaBasesOutside counts chats whose media_base is an absolute prefix
// outside root, returning the count and one example value for the report.
func AbsMediaBasesOutside(root string, mediaBaseChats map[string]int) (int, string) {
	bases := make([]string, 0, len(mediaBaseChats))
	for b := range mediaBaseChats {
		bases = append(bases, b)
	}
	sort.Strings(bases)
	var n int
	var example string
	for _, base := range bases {
		if !filepath.IsAbs(base) {
			continue
		}
		rel, err := filepath.Rel(root, base)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue // absolute but under the root: the parser relativizes it
		}
		n += mediaBaseChats[base]
		if example == "" {
			example = base
		}
	}
	return n, example
}

// WhatsAppBackupHint is the platform-aware backup prerequisite for producing
// a WhatsApp export (SPEC-0009: the platform changes the prerequisite, not
// the parsing). An unknown/empty device prints both paths.
func WhatsAppBackupHint(device string) string {
	switch device {
	case whatsapp.DeviceIOS:
		return "iOS exports need a local Finder/iTunes backup of the device (not iCloud)"
	case whatsapp.DeviceAndroid:
		return "Android exports need the WhatsApp backup file plus its 64-digit end-to-end encryption key"
	default:
		return "iOS needs a local Finder/iTunes backup; Android needs the WhatsApp backup plus its 64-digit key"
	}
}

// Fraction is part/total guarding total==0.
func Fraction(part, total int) float64 {
	if total == 0 {
		return 0
	}
	return float64(part) / float64(total)
}
