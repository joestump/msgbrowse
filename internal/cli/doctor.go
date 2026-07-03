package cli

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/joestump/msgbrowse/internal/archivepath"
	"github.com/joestump/msgbrowse/internal/config"
	"github.com/joestump/msgbrowse/internal/imageconv"
	"github.com/joestump/msgbrowse/internal/ingest"
	"github.com/joestump/msgbrowse/internal/source"
	"github.com/joestump/msgbrowse/internal/store"
	"github.com/joestump/msgbrowse/internal/whatsapp"
	"github.com/spf13/cobra"
)

// Doctor is a read-only environment/setup diagnostic. It walks a series of
// checks over the resolved config, the data dir, the read-only archives, and the
// imported attachment rows, printing one human-readable line per check and a
// one-line summary. It is intentionally side-effect free: it opens the store
// read-only (Open does create the data dir and apply migrations, which is the
// only write, and only to msgbrowse's own data dir), reads attachment metadata,
// stats files in the archive, and — only behind --check-llm — does a bare TCP
// connect to the configured llm.base_url host:port (no bytes sent).

// glyphs prefixing each check line. Plain text so output stays grep-friendly.
const (
	glyphPass = "✓"
	glyphWarn = "⚠"
	glyphFail = "✗"
)

// checkStatus is the outcome of a single doctor check.
type checkStatus int

const (
	statusPass checkStatus = iota
	statusWarn
	statusFail
)

func (s checkStatus) glyph() string {
	switch s {
	case statusFail:
		return glyphFail
	case statusWarn:
		return glyphWarn
	default:
		return glyphPass
	}
}

// report accumulates check results and writes them to a single Writer. Keeping
// the writer here means every line — including the summary — goes to one stream
// (stdout), never the slog logger (which is reserved for stderr).
type report struct {
	w        io.Writer
	warnings int
	fails    int
}

// add prints one check line: "<glyph> <title>" plus an optional indented hint on
// the next line when the check did not pass.
func (r *report) add(status checkStatus, title, hint string) {
	fmt.Fprintf(r.w, "%s %s\n", status.glyph(), title)
	if status != statusPass && hint != "" {
		fmt.Fprintf(r.w, "    %s\n", hint)
	}
	switch status {
	case statusWarn:
		r.warnings++
	case statusFail:
		r.fails++
	}
}

// summary writes the trailing one-liner and reports whether any check failed.
func (r *report) summary() bool {
	fmt.Fprintf(r.w, "doctor: %s, %s\n", plural(r.warnings, "warning"), plural(r.fails, "problem"))
	return r.fails > 0
}

func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

func newDoctorCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Diagnose msgbrowse setup and archive/attachment health (read-only)",
		Long: "doctor runs read-only checks over your configuration, data directory, and\n" +
			"imported archives, then prints a report with a status glyph per check\n" +
			"(✓ pass, ⚠ warn, ✗ problem) and a one-line summary. It exits non-zero only\n" +
			"if a check fails (✗), so it is safe to use in scripts.\n" +
			"\n" +
			"The headline check inspects imported attachment paths: an iMessage export\n" +
			"done WITHOUT copy-mode records absolute ~/Library paths that point outside\n" +
			"the archive, so no media is browsable. doctor flags that and tells you how\n" +
			"to re-export.\n" +
			"\n" +
			"doctor makes NO network calls except an OPTIONAL TCP-connect reachability\n" +
			"probe (no data sent) to the single configured llm.base_url, behind --check-llm.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := resolveConfig()
			if err != nil {
				return err
			}
			checkLLM, err := cmd.Flags().GetBool("check-llm")
			if err != nil {
				return err
			}
			failed := runDoctor(cmd.Context(), cmd.OutOrStdout(), cfg, checkLLM)
			if failed {
				// Non-zero exit for scripts. Suppress usage/error rendering: the
				// report is the user-facing output, not an error message.
				cmd.SilenceUsage = true
				cmd.SilenceErrors = true
				return errDoctorFailed
			}
			return nil
		},
	}
	cmd.Flags().Bool("check-llm", false, "additionally TCP-probe the configured llm.base_url for reachability (the single configured egress; no data is sent)")
	return cmd
}

// errDoctorFailed signals at least one ✗ check so Execute exits non-zero. It is
// rendered specially (no logger line) since the report already explained things.
var errDoctorFailed = doctorFailedError{}

type doctorFailedError struct{}

func (doctorFailedError) Error() string { return "doctor found one or more problems" }

// runDoctor executes every check in order, writing to w, and returns true if any
// check failed (✗). It never returns an error: a failed check is reported as a
// line, not a Go error, so the report is always complete.
func runDoctor(ctx context.Context, w io.Writer, cfg *config.Config, checkLLM bool) bool {
	r := &report{w: w}

	st := checkDataDir(ctx, r, cfg)
	if st != nil {
		defer st.Close()
	}
	checkSignalArchive(r, cfg)
	checkIMessageArchive(r, cfg)
	checkWhatsAppArchive(r, cfg)
	checkAttachments(ctx, r, cfg, st)
	checkConverter(ctx, r, cfg, st)
	checkEmbeddings(ctx, r, cfg, st)
	checkExporters(r)
	checkLLMEndpoint(r, cfg, checkLLM)

	return r.summary()
}

// checkDataDir verifies the data dir is writable and reports DB presence, schema
// version, and corpus totals. It returns an open *store.Store (caller closes) or
// nil if the DB couldn't be opened — later checks degrade gracefully on nil.
func checkDataDir(ctx context.Context, r *report, cfg *config.Config) *store.Store {
	if cfg.DataDir == "" {
		r.add(statusFail, "data_dir is not set", "set data_dir (config), --data-dir, or MSGBROWSE_DATA_DIR to a writable directory")
		return nil
	}

	// doctor is a read-only diagnostic: it must NOT create the data dir or the
	// database (a typo'd --data-dir should be reported, not silently created).
	info, err := os.Stat(cfg.DataDir)
	switch {
	case os.IsNotExist(err):
		r.add(statusWarn, fmt.Sprintf("data_dir %q does not exist yet", cfg.DataDir),
			"it's created on first import; run `msgbrowse import` once your archives are configured")
		return nil // nothing to open; don't create anything
	case err != nil:
		r.add(statusFail, fmt.Sprintf("data_dir %q: %v", cfg.DataDir, err), "check the path and permissions")
		return nil
	case !info.IsDir():
		r.add(statusFail, fmt.Sprintf("data_dir %q is not a directory", cfg.DataDir), "point data_dir at a directory")
		return nil
	}
	if err := writable(cfg.DataDir); err != nil {
		r.add(statusFail, fmt.Sprintf("data_dir %q is not writable: %v", cfg.DataDir, err),
			"the database and caches live here; grant write access or choose another data_dir")
		return nil
	}
	r.add(statusPass, fmt.Sprintf("data_dir %q exists and is writable", cfg.DataDir), "")

	if !fileExists(dbPath(cfg)) {
		r.add(statusWarn, "no database yet (no import has run)",
			"run `msgbrowse import` after configuring your archive roots")
		return nil
	}

	// Open read-only and WITHOUT migrating, so we report the true on-disk schema
	// version (drift is meaningful) and never write to the user's DB.
	st, err := store.OpenReadOnly(dbPath(cfg))
	if err != nil {
		r.add(statusFail, fmt.Sprintf("cannot open database (read-only): %v", err), "check data_dir permissions")
		return nil
	}

	if v, err := st.UserVersion(ctx); err != nil {
		r.add(statusWarn, fmt.Sprintf("could not read schema version: %v", err), "")
	} else if v == store.SchemaVersion() {
		r.add(statusPass, fmt.Sprintf("database schema is current (version %d)", v), "")
	} else {
		r.add(statusWarn, fmt.Sprintf("database schema version %d, binary expects %d", v, store.SchemaVersion()),
			"run any msgbrowse command (e.g. `import`) to migrate it forward")
	}

	convs, cerr := st.ListConversations(ctx)
	msgs, merr := st.CountMessages(ctx)
	if cerr != nil || merr != nil {
		r.add(statusWarn, "could not count conversations/messages", firstErr(cerr, merr).Error())
		return st
	}
	if len(convs) == 0 || msgs == 0 {
		r.add(statusWarn, fmt.Sprintf("%d conversations, %d messages", len(convs), msgs),
			"nothing imported yet — run `msgbrowse import`")
	} else {
		r.add(statusPass, fmt.Sprintf("%d conversations, %d messages imported", len(convs), msgs), "")
	}
	return st
}

// checkSignalArchive validates the signal archive_root. The classic mistake it
// catches is pointing archive_root AT the export/ subdir instead of its parent.
func checkSignalArchive(r *report, cfg *config.Config) {
	if cfg.ArchiveRoot == "" {
		r.add(statusWarn, "archive_root (Signal) is not set",
			"set it to the folder that CONTAINS export/ if you want to import Signal; ignore if you only use iMessage")
		return
	}
	info, err := os.Stat(cfg.ArchiveRoot)
	if err != nil {
		r.add(statusFail, fmt.Sprintf("archive_root %q: %v", cfg.ArchiveRoot, err),
			"check the path; it must be the read-only signal-export archive root")
		return
	}
	if !info.IsDir() {
		r.add(statusFail, fmt.Sprintf("archive_root %q is not a directory", cfg.ArchiveRoot), "")
		return
	}
	switch classifyArchiveRoot(cfg.ArchiveRoot) {
	case archiveRootOK:
		r.add(statusPass, fmt.Sprintf("Signal archive_root %q contains export/", cfg.ArchiveRoot), "")
	case archiveRootPointsAtExport:
		r.add(statusFail, fmt.Sprintf("archive_root %q points AT export/ (or its contents), not the archive root", cfg.ArchiveRoot),
			"set archive_root to the PARENT folder — the one that CONTAINS export/, e.g. .../Signal-Archive not .../Signal-Archive/export")
	default: // archiveRootNoExport
		r.add(statusWarn, fmt.Sprintf("archive_root %q has no export/ subdirectory", cfg.ArchiveRoot),
			"archive_root must contain an export/ folder of per-conversation directories; check you exported with signal-export")
	}
}

// checkIMessageArchive validates the imessage_archive_root: it should be the
// flat directory of <ChatName>.txt files.
func checkIMessageArchive(r *report, cfg *config.Config) {
	if cfg.IMessageArchiveRoot == "" {
		r.add(statusWarn, "imessage_archive_root is not set",
			"set it to the imessage-exporter output directory if you want to import iMessage; ignore if you only use Signal")
		return
	}
	info, err := os.Stat(cfg.IMessageArchiveRoot)
	if err != nil {
		r.add(statusFail, fmt.Sprintf("imessage_archive_root %q: %v", cfg.IMessageArchiveRoot, err),
			"check the path; it must be the imessage-exporter output directory")
		return
	}
	if !info.IsDir() {
		r.add(statusFail, fmt.Sprintf("imessage_archive_root %q is not a directory", cfg.IMessageArchiveRoot), "")
		return
	}
	n, err := countTxtFiles(cfg.IMessageArchiveRoot)
	if err != nil {
		r.add(statusWarn, fmt.Sprintf("could not scan imessage_archive_root %q: %v", cfg.IMessageArchiveRoot, err), "")
		return
	}
	if n == 0 {
		r.add(statusWarn, fmt.Sprintf("imessage_archive_root %q has no *.txt files", cfg.IMessageArchiveRoot),
			"this should be the imessage-exporter output (a folder of <ChatName>.txt files); re-run with `-f txt` and point here")
		return
	}
	r.add(statusPass, fmt.Sprintf("imessage_archive_root %q has %d *.txt file(s)", cfg.IMessageArchiveRoot, n), "")
}

// checkWhatsAppArchive validates the whatsapp_archive_root per SPEC-0009
// REQ-0009-009: the directory must exist and contain the exporter's
// result.json, no chat may reference media through an absolute media_base
// outside the root, and a sample of media references must resolve to files
// inside the root. Remediation hints are platform-aware (the export records
// the device type): iOS needs a local Finder/iTunes backup, Android the
// backup plus its 64-digit key.
func checkWhatsAppArchive(r *report, cfg *config.Config) {
	root := cfg.WhatsAppArchiveRoot
	if root == "" {
		r.add(statusWarn, "whatsapp_archive_root is not set",
			"set it to the WhatsApp-Chat-Exporter output directory (the folder containing result.json) if you want to import WhatsApp; ignore otherwise")
		return
	}
	info, err := os.Stat(root)
	if err != nil {
		r.add(statusFail, fmt.Sprintf("whatsapp_archive_root %q: %v", root, err),
			"check the path; it must be the wtsexporter output directory (the folder containing result.json)")
		return
	}
	if !info.IsDir() {
		r.add(statusFail, fmt.Sprintf("whatsapp_archive_root %q is not a directory", root), "")
		return
	}

	path := filepath.Join(root, whatsapp.ResultFile)
	f, err := os.Open(path)
	if err != nil {
		r.add(statusWarn, fmt.Sprintf("whatsapp_archive_root %q has no %s", root, whatsapp.ResultFile),
			fmt.Sprintf("run `msgbrowse export` (wtsexporter with --json) into this directory; %s", whatsappBackupHint("")))
		return
	}
	defer f.Close()
	sum, err := whatsapp.ScanExport(f, whatsappMediaSampleLimit)
	if err != nil {
		r.add(statusFail, fmt.Sprintf("could not parse %s: %v", path, err),
			fmt.Sprintf("this should be the wtsexporter JSON export; re-run `msgbrowse export`; %s", whatsappBackupHint(sum.Device)))
		return
	}
	r.add(statusPass, fmt.Sprintf("whatsapp_archive_root %q has %s (%d chats, %d messages)",
		root, whatsapp.ResultFile, sum.Chats, sum.Messages), "")

	// Headline WhatsApp check: a chat-level media_base that is absolute and
	// points outside the root means the exporter referenced the media folder
	// in place instead of copying it under the root — none of that media is
	// browsable from the archive.
	if n, example := absMediaBasesOutside(root, sum.MediaBaseChats); n > 0 {
		r.add(statusFail, fmt.Sprintf("%d WhatsApp chat(s) reference media through an absolute media_base outside the archive (e.g. %q)", n, example),
			fmt.Sprintf("media was not copied into the export; re-run the exporter so the media folder lands under whatsapp_archive_root (wtsexporter copies -m into -o by default), then `msgbrowse import --full`; %s", whatsappBackupHint(sum.Device)))
	}

	if len(sum.MediaRefs) == 0 {
		r.add(statusPass, "no WhatsApp media references to check", "")
		return
	}
	var s whatsappMediaStats
	for _, ref := range sum.MediaRefs {
		s.add(classifyWhatsAppMedia(root, ref, os.Stat))
	}
	status, hint := whatsappMediaVerdict(sum.Device, &s)
	r.add(status, fmt.Sprintf("WhatsApp media references: %d ok, %d outside the root, %d missing (of %d sampled)",
		s.present, s.outside, s.missing, s.total()), hint)
}

// checkAttachments is the headline check: sample image attachments and classify
// each by how its stored rel_path resolves on disk. A large absolute-path or
// missing-file share for iMessage means the export was not copy-mode.
func checkAttachments(ctx context.Context, r *report, cfg *config.Config, st *store.Store) {
	if st == nil {
		return // no DB; nothing imported to inspect
	}
	items, err := st.ListImageAttachments(ctx)
	if err != nil {
		r.add(statusWarn, "could not list image attachments", err.Error())
		return
	}
	if len(items) == 0 {
		r.add(statusPass, "no image attachments to check", "")
		return
	}

	sample := items
	if len(sample) > attachmentSampleLimit {
		sample = sample[:attachmentSampleLimit]
	}

	bySource := map[string]*attachmentStats{}
	for _, it := range sample {
		s := bySource[it.Source]
		if s == nil {
			s = &attachmentStats{}
			bySource[it.Source] = s
		}
		s.add(classifyAttachment(it.Source, cfg.ArchiveRoot, cfg.IMessageArchiveRoot, cfg.WhatsAppArchiveRoot, it.ConversationName, it.RelPath, os.Stat))
	}

	for _, src := range sortedSources(bySource) {
		s := bySource[src]
		label := source.Label(src)
		status, hint := attachmentVerdict(src, s)
		title := fmt.Sprintf("%s attachments: %d ok, %d absolute, %d missing (of %d sampled)",
			label, s.present, s.absolute, s.missing, s.total())
		if len(items) > len(sample) {
			title = fmt.Sprintf("%s attachments: %d ok, %d absolute, %d missing (of %d sampled, %d total images)",
				label, s.present, s.absolute, s.missing, s.total(), len(items))
		}
		r.add(status, title, hint)
	}
}

// checkConverter reports the image converter and how many convertible (HEIC/
// TIFF) attachments lack a cached derivative.
func checkConverter(ctx context.Context, r *report, cfg *config.Config, st *store.Store) {
	conv, ok := imageconv.Detect()
	if ok {
		r.add(statusPass, fmt.Sprintf("image converter found: %s", conv.Name), "")
	} else {
		r.add(statusWarn, "no image converter found (sips / magick / convert / heif-convert)",
			"HEIC/TIFF attachments will show placeholders; install one (e.g. ImageMagick or libheif) and run `msgbrowse media`")
	}

	if st == nil {
		return
	}
	items, err := st.ListImageAttachments(ctx)
	if err != nil {
		return // already surfaced in checkAttachments
	}
	derivedDir := imageconv.DerivedDir(cfg.DataDir)
	var needDeriv int
	for _, it := range items {
		if !imageconv.Convertible(it.RelPath) {
			continue
		}
		abs, resolved := archivepath.Resolve(it.Source, cfg.ArchiveRoot, cfg.IMessageArchiveRoot, it.ConversationName, it.RelPath)
		if !resolved {
			continue // unresolvable (e.g. absolute path) — not a transcode candidate
		}
		if _, serr := os.Stat(abs); serr != nil {
			continue // source missing — transcode can't help
		}
		if _, derr := os.Stat(imageconv.DerivedPath(derivedDir, abs)); derr != nil {
			needDeriv++
		}
	}
	if needDeriv > 0 {
		status := statusWarn
		hint := fmt.Sprintf("run `msgbrowse media` to transcode %d HEIC/TIFF image(s) for the gallery", needDeriv)
		if !ok {
			hint = "install an image converter first, then run `msgbrowse media`"
		}
		r.add(status, fmt.Sprintf("%d convertible image(s) lack a cached derivative", needDeriv), hint)
	}
}

// checkEmbeddings reports how many messages still need embedding.
func checkEmbeddings(ctx context.Context, r *report, cfg *config.Config, st *store.Store) {
	if st == nil {
		return
	}
	n, err := st.CountMissingEmbeddings(ctx, cfg.LLM.EmbedModel)
	if err != nil {
		r.add(statusWarn, "could not count missing embeddings", err.Error())
		return
	}
	if n == 0 {
		r.add(statusPass, "all messages are embedded", "")
		return
	}
	r.add(statusWarn, fmt.Sprintf("%d message(s) not embedded for model %q", n, cfg.LLM.EmbedModel),
		"run `msgbrowse embed` (needs the configured LLM endpoint) to enable semantic search")
}

// checkExporters looks for the upstream export tools msgbrowse may shell out to
// for the planned export feature. Missing tools are informational warnings.
func checkExporters(r *report) {
	for _, e := range []struct{ bin, hint string }{
		// The Signal exporter's console command is `sigexport` (the pip *package*
		// is signal-export); `msgbrowse export` looks up this same binary.
		{"sigexport", "needed only if you want msgbrowse to run Signal exports; install via pipx: `pipx install signal-export` (the command is `sigexport`)"},
		{"imessage-exporter", "needed only if you want msgbrowse to run iMessage exports; install via Homebrew: `brew install imessage-exporter`"},
		// Same pip-package-vs-command confusion as sigexport: the package is
		// whatsapp-chat-exporter, the console command `wtsexporter`.
		{"wtsexporter", "needed only if you want msgbrowse to run WhatsApp exports; install via pipx: `pipx install whatsapp-chat-exporter` (the command is `wtsexporter`); " + whatsappBackupHint("")},
	} {
		if _, err := exec.LookPath(e.bin); err == nil {
			r.add(statusPass, fmt.Sprintf("exporter %q found on PATH", e.bin), "")
		} else {
			r.add(statusWarn, fmt.Sprintf("exporter %q not found on PATH", e.bin), e.hint)
		}
	}
}

// checkLLMEndpoint optionally TCP-probes the configured llm.base_url. It is the
// only network operation doctor performs, and only with --check-llm. No request
// body is sent — it opens and closes a connection to confirm reachability.
func checkLLMEndpoint(r *report, cfg *config.Config, checkLLM bool) {
	if !checkLLM {
		return
	}
	host, err := hostPort(cfg.LLM.BaseURL)
	if err != nil {
		r.add(statusWarn, fmt.Sprintf("could not parse llm.base_url %q: %v", cfg.LLM.BaseURL, err), "")
		return
	}
	conn, err := net.DialTimeout("tcp", host, llmProbeTimeout)
	if err != nil {
		r.add(statusWarn, fmt.Sprintf("llm endpoint %s not reachable: %v", host, err),
			"embed/facts/journal need this endpoint; this is the single configured egress (llm.base_url)")
		return
	}
	_ = conn.Close()
	r.add(statusPass, fmt.Sprintf("llm endpoint %s reachable (TCP connect only; no data sent)", host), "")
}

// --- testable decision logic -------------------------------------------------

// attachmentSampleLimit caps how many image attachments checkAttachments
// inspects; sampling keeps doctor fast on large archives while still being
// representative of a misconfigured export.
const attachmentSampleLimit = 300

// llmProbeTimeout bounds the optional TCP reachability probe.
const llmProbeTimeout = 2 * time.Second

// attachmentClass is how a stored attachment rel_path resolves on disk.
type attachmentClass int

const (
	// attachPresent: rel_path resolves inside the archive and the file exists.
	attachPresent attachmentClass = iota
	// attachAbsolute: rel_path is an absolute path (e.g. ~/Library expanded) —
	// a reference-only export pointing outside the archive. No media is copied.
	attachAbsolute
	// attachMissing: rel_path resolves inside the archive but the file is gone.
	attachMissing
)

// classifyAttachment decides how one attachment's rel_path resolves. statFn is
// injected so tests don't touch the filesystem (pass os.Stat in production).
//
// An absolute rel_path is the signature of a non-copy-mode iMessage export: the
// exporter wrote the original ~/Library/.../Attachments path rather than copying
// the file into the archive. The IsAbs short-circuit below is what catches these
// — archivepath.Contain does NOT reject an absolute path, it neutralizes the
// leading "/" and folds it UNDER the archive root, which would mis-classify it as
// present/missing rather than flagging the real problem. So the explicit IsAbs
// check must come first.
//
// WhatsApp rows resolve flat under whatsappRoot here (the same containment
// archivepath.Contain provides); their media-resolution branch in
// archivepath.Resolve is the surface story's scope (REQ-0009-006).
func classifyAttachment(src, archiveRoot, imessageRoot, whatsappRoot, convName, rel string, statFn func(string) (os.FileInfo, error)) attachmentClass {
	if filepath.IsAbs(rel) {
		return attachAbsolute
	}
	var abs string
	var ok bool
	if src == source.WhatsApp {
		abs, ok = archivepath.Contain(whatsappRoot, rel)
	} else {
		abs, ok = archivepath.Resolve(src, archiveRoot, imessageRoot, convName, rel)
	}
	if !ok {
		// Unresolvable for a non-absolute path means the relevant archive root is
		// unset/misconfigured; treat as missing so it still counts against health.
		return attachMissing
	}
	if _, err := statFn(abs); err != nil {
		return attachMissing
	}
	return attachPresent
}

// attachmentStats tallies classifications for one source.
type attachmentStats struct {
	present  int
	absolute int
	missing  int
}

func (s *attachmentStats) add(c attachmentClass) {
	switch c {
	case attachAbsolute:
		s.absolute++
	case attachMissing:
		s.missing++
	default:
		s.present++
	}
}

func (s *attachmentStats) total() int { return s.present + s.absolute + s.missing }

// attachmentVerdict turns one source's stats into a status + hint. The headline
// case: a meaningful share of absolute iMessage paths means the export was not
// copy-mode. Missing files (resolvable but absent) are a softer warning.
func attachmentVerdict(src string, s *attachmentStats) (checkStatus, string) {
	total := s.total()
	if total == 0 {
		return statusPass, ""
	}
	// A clear majority of attachments are absolute (or absent): the export almost
	// certainly skipped copy-mode. This is the high-value diagnosis.
	if s.absolute > 0 && fraction(s.absolute, total) >= attachAbsoluteFailFraction {
		if src == source.IMessage {
			return statusFail, fmt.Sprintf(
				"%d iMessage attachments use absolute ~/Library paths — your imessage-exporter run wasn't copy-mode; "+
					"re-run with -c/--copy-method (e.g. `-c clone`), then `msgbrowse import --full`.", s.absolute)
		}
		return statusFail, fmt.Sprintf(
			"%d attachments store absolute paths pointing outside the archive; re-export with media copied into the archive, then `msgbrowse import --full`.", s.absolute)
	}
	// Some absolute paths, or a meaningful share of missing files: warn.
	if s.absolute > 0 {
		if src == source.IMessage {
			return statusWarn, fmt.Sprintf(
				"%d iMessage attachments use absolute ~/Library paths (non-copy-mode export); "+
					"re-run imessage-exporter with -c/--copy-method (e.g. `-c clone`) then `msgbrowse import --full` to browse them.", s.absolute)
		}
		return statusWarn, fmt.Sprintf("%d attachments store absolute paths outside the archive; re-export with media copied in.", s.absolute)
	}
	if s.missing > 0 && fraction(s.missing, total) >= attachMissingWarnFraction {
		return statusWarn, fmt.Sprintf(
			"%d of %d sampled attachments resolve inside the archive but the file is missing; the archive may be incomplete or moved.", s.missing, total)
	}
	if s.missing > 0 {
		return statusWarn, fmt.Sprintf("%d of %d sampled attachment file(s) are missing on disk.", s.missing, total)
	}
	return statusPass, ""
}

// Thresholds for attachmentVerdict. Kept as named constants so the policy is
// visible and testable.
const (
	// attachAbsoluteFailFraction: at/above this share of absolute paths, the
	// export is treated as definitively non-copy-mode (✗).
	attachAbsoluteFailFraction = 0.5
	// attachMissingWarnFraction: at/above this share of missing-but-resolvable
	// files, escalate the wording (still ⚠).
	attachMissingWarnFraction = 0.25
)

func fraction(part, total int) float64 {
	if total == 0 {
		return 0
	}
	return float64(part) / float64(total)
}

// --- WhatsApp export health (SPEC-0009 REQ-0009-009) -------------------------

// whatsappMediaSampleLimit caps how many media references checkWhatsAppArchive
// samples from result.json, mirroring attachmentSampleLimit's rationale.
const whatsappMediaSampleLimit = 300

// whatsappMediaClass is how one raw media reference (media_base + data)
// resolves against the WhatsApp archive root.
type whatsappMediaClass int

const (
	// whatsappMediaPresent: the reference resolves inside the root and the
	// file exists.
	whatsappMediaPresent whatsappMediaClass = iota
	// whatsappMediaOutside: the reference is an absolute path outside the
	// root — media was referenced in place, not copied into the export.
	whatsappMediaOutside
	// whatsappMediaMissing: the reference resolves inside the root but the
	// file is not there (e.g. the media directory was not copied).
	whatsappMediaMissing
)

// classifyWhatsAppMedia decides how one media reference resolves. The full
// path is media_base + data (the exporter's own <base href> semantics); an
// absolute full path under the root is relativized (the parser stores it that
// way), an absolute path elsewhere is the reference-only signature. statFn is
// injected so tests don't touch the filesystem (pass os.Stat in production).
func classifyWhatsAppMedia(root string, ref whatsapp.MediaRef, statFn func(string) (os.FileInfo, error)) whatsappMediaClass {
	full := ref.MediaBase + ref.Data
	if filepath.IsAbs(full) {
		rel, err := filepath.Rel(root, full)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return whatsappMediaOutside
		}
		full = rel
	}
	abs, ok := archivepath.Contain(root, full)
	if !ok {
		return whatsappMediaMissing
	}
	if _, err := statFn(abs); err != nil {
		return whatsappMediaMissing
	}
	return whatsappMediaPresent
}

// whatsappMediaStats tallies media-reference classifications.
type whatsappMediaStats struct {
	present int
	outside int
	missing int
}

func (s *whatsappMediaStats) add(c whatsappMediaClass) {
	switch c {
	case whatsappMediaOutside:
		s.outside++
	case whatsappMediaMissing:
		s.missing++
	default:
		s.present++
	}
}

func (s *whatsappMediaStats) total() int { return s.present + s.outside + s.missing }

// whatsappMediaVerdict turns the sampled stats into a status + remediation.
// Majority-outside and majority-missing are both the fail-level "the export
// has no usable media" diagnosis (REQ-0009-009's missing-media scenario);
// smaller shares warn. The hint is platform-aware via device.
func whatsappMediaVerdict(device string, s *whatsappMediaStats) (checkStatus, string) {
	total := s.total()
	if total == 0 {
		return statusPass, ""
	}
	rerun := fmt.Sprintf("re-run the exporter with the media folder present so it is copied under whatsapp_archive_root, then `msgbrowse import --full`; %s", whatsappBackupHint(device))
	if s.outside > 0 && fraction(s.outside, total) >= attachAbsoluteFailFraction {
		return statusFail, fmt.Sprintf("%d media reference(s) point outside the archive root (absolute media_base) — media was not copied into the export; %s", s.outside, rerun)
	}
	if s.missing > 0 && fraction(s.missing, total) >= attachAbsoluteFailFraction {
		return statusFail, fmt.Sprintf("%d of %d sampled media reference(s) are not present under the root — the media directory was not copied; %s", s.missing, total, rerun)
	}
	if s.outside > 0 {
		return statusWarn, fmt.Sprintf("%d media reference(s) point outside the archive root; %s", s.outside, rerun)
	}
	if s.missing > 0 {
		return statusWarn, fmt.Sprintf("%d of %d sampled media reference file(s) are missing under the root; the export may be partial. %s", s.missing, total, rerun)
	}
	return statusPass, ""
}

// absMediaBasesOutside counts chats whose media_base is an absolute prefix
// outside root, returning the count and one example value for the report.
func absMediaBasesOutside(root string, mediaBaseChats map[string]int) (int, string) {
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

// whatsappBackupHint is the platform-aware backup prerequisite for producing
// a WhatsApp export (SPEC-0009: the platform changes the prerequisite, not
// the parsing). An unknown/empty device prints both paths.
func whatsappBackupHint(device string) string {
	switch device {
	case whatsapp.DeviceIOS:
		return "iOS exports need a local Finder/iTunes backup of the device (not iCloud)"
	case whatsapp.DeviceAndroid:
		return "Android exports need the WhatsApp backup file plus its 64-digit end-to-end encryption key"
	default:
		return "iOS needs a local Finder/iTunes backup; Android needs the WhatsApp backup plus its 64-digit key"
	}
}

// archiveRootKind classifies a signal archive_root path.
type archiveRootKind int

const (
	// archiveRootOK: <root>/export exists and is a directory (correct).
	archiveRootOK archiveRootKind = iota
	// archiveRootPointsAtExport: the user pointed at export/ itself (or its
	// contents) — the classic mistake. Detected when <root>/export/export exists
	// OR <root> has no export/ subdir but is itself named "export".
	archiveRootPointsAtExport
	// archiveRootNoExport: a directory with no export/ subdir and not named
	// export — wrong directory entirely.
	archiveRootNoExport
)

// classifyArchiveRoot decides whether archive_root is correct, points at export/
// itself, or simply lacks export/. It is pure filesystem inspection so it can be
// unit-tested against a temp tree.
func classifyArchiveRoot(root string) archiveRootKind {
	exportSub := filepath.Join(root, ingest.ExportDir)
	if info, err := os.Stat(exportSub); err == nil && info.IsDir() {
		// <root>/export exists. If <root>/export/export also exists, the user
		// passed one level too deep (…/Archive/export as the root).
		if info2, err2 := os.Stat(filepath.Join(exportSub, ingest.ExportDir)); err2 == nil && info2.IsDir() {
			return archiveRootPointsAtExport
		}
		return archiveRootOK
	}
	// No export/ subdir. If the root itself is named "export", the user almost
	// certainly pointed at the export folder instead of its parent.
	if filepath.Base(filepath.Clean(root)) == ingest.ExportDir {
		return archiveRootPointsAtExport
	}
	return archiveRootNoExport
}

// hostPort extracts a dialable host:port from an llm.base_url, defaulting the
// port from the scheme when absent. Returns an error for an unparseable URL.
func hostPort(base string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	if u.Host == "" {
		return "", fmt.Errorf("no host in URL")
	}
	if u.Port() != "" {
		return u.Host, nil
	}
	port := "80"
	if u.Scheme == "https" {
		port = "443"
	}
	return net.JoinHostPort(u.Hostname(), port), nil
}

// --- small filesystem helpers ------------------------------------------------

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// writable confirms dir accepts a write by creating and removing a temp file.
func writable(dir string) error {
	f, err := os.CreateTemp(dir, ".doctor-write-*")
	if err != nil {
		return err
	}
	name := f.Name()
	_ = f.Close()
	return os.Remove(name)
}

// countTxtFiles counts *.txt files directly under dir (non-recursive — the
// imessage-exporter txt output is a flat directory of <ChatName>.txt files).
func countTxtFiles(dir string) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, err
	}
	var n int
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if filepath.Ext(e.Name()) == ".txt" {
			n++
		}
	}
	return n, nil
}

func sortedSources(m map[string]*attachmentStats) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func firstErr(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}
