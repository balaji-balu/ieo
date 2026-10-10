// Package archive validates and extracts Margo Compose Archives on the host (SPEC §5.2, §9.2). It
// owns the archive rules and the safety of extraction; pulling the archive and running what it
// holds are other packages' work.
package archive

import (
	"context"
	"errors"
	"io"
)

// ComposeFile is the only name accepted for an archive's Compose file (SPEC §5.2).
const ComposeFile = "compose.yaml"

// ErrInvalid is returned, wrapped with the rule broken, for an archive that breaks SPEC §5.2 or
// is over one of its limits. The EN reports it as IEO-ARCHIVE-INVALID (SPEC §8.9).
var ErrInvalid = errors.New("invalid compose archive")

// Limits bound what one archive may hold (SPEC §5.2, §6.3). A zero limit admits nothing.
type Limits struct {
	// MaxEntries is the most entries the archive may hold (en.archive.max_entries).
	MaxEntries int
	// MaxExtractedBytes is the most bytes its regular files may add up to once decompressed
	// (en.archive.max_extracted_bytes).
	MaxExtractedBytes int64
}

// Extract validates the gzip tar read from r against SPEC §5.2 and extracts it to dir, the
// component's directory (SPEC §9.1). It returns the name of the archive's top-level directory:
// the Compose file is then <dir>/<top>/compose.yaml.
//
// dir is replaced: what it held before is deleted first, and its parent is created if missing.
// When Extract fails, dir does not exist and nothing extracted is left behind. An archive that
// breaks a rule or a limit fails with an error wrapping ErrInvalid; no file is written past
// l.MaxExtractedBytes. Other errors are failures to read r or to write to disk, or ctx ending.
//
// Extraction never writes outside dir and never follows a link (SPEC §9.2). Files and directories
// keep their permission bits, without setuid, setgid and sticky; owners and times are not kept.
func Extract(ctx context.Context, r io.Reader, dir string, l Limits) (top string, err error) {
	return "", errors.New("archive: Extract is not implemented")
}
