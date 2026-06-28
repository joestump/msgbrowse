// Package archivepath resolves a stored attachment's relative path to an
// absolute file path inside the correct read-only archive, with path-traversal
// containment. It is shared by the web media handler and the image-transcode
// pipeline so both agree on (and equally protect) the mapping.
package archivepath

import (
	"path/filepath"
	"strings"

	"github.com/joestump/msgbrowse/internal/source"
)

// exportDir mirrors ingest.ExportDir — the signal-export subdirectory holding
// per-conversation folders. Hardcoded here to keep this a dependency-light leaf
// package; it must stay in sync with ingest.ExportDir.
const exportDir = "export"

// Resolve maps an attachment's (source, conversation, rel) to an absolute path
// under the correct archive, returning ok=false on traversal or misconfig:
//
//   - signal:   <archiveRoot>/export/<conversation>/<rel>
//   - imessage: <imessageRoot>/<rel>  (flat export; rel like "attachments/…")
func Resolve(src, archiveRoot, imessageRoot, convName, rel string) (string, bool) {
	switch src {
	case source.IMessage:
		return Contain(imessageRoot, rel)
	default: // signal (and legacy rows with empty source)
		if archiveRoot == "" {
			return "", false
		}
		return Contain(filepath.Join(archiveRoot, exportDir, convName), rel)
	}
}

// Contain resolves rel against base, rejecting empty inputs and any path that
// escapes base. The containment check is lexical against base, so a symlinked
// media dir inside base is still served (its target is not checked).
func Contain(base, rel string) (string, bool) {
	if base == "" || rel == "" {
		return "", false
	}
	cleanRel := filepath.Clean("/" + strings.TrimPrefix(rel, "/"))
	full := filepath.Join(base, cleanRel)
	relCheck, err := filepath.Rel(base, full)
	if err != nil || relCheck == ".." || strings.HasPrefix(relCheck, ".."+string(filepath.Separator)) {
		return "", false
	}
	return full, true
}
