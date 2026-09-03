// Package migrate holds the one-shot cutovers that adopt state the
// PowerShell app left behind. Today that is exactly one thing: the managed
// crontab block, whose lines must stop invoking `pwsh -File scriptorium.ps1`
// and start invoking this binary.
//
// (The systemd unit's ExecStart is the other half of the cutover and is
// deliberately NOT here — rewriting a live MCP service to point at a stub
// would take the service down. It lands with the real MCP server.)
package migrate

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/yshah-aromatech/scriptorium/internal/cron"
)

// psSpelling is what a not-yet-adopted managed line looks like: the
// PowerShell app writes `'<pwsh>' -NoProfile -File scriptorium.ps1 --run ...`.
const psSpelling = " -NoProfile -File "

// ErrReadFailed is the wipe-guard error: the crontab could not be read at
// all, so its true contents are unknown and nothing may be written. Crontab
// and Preview both return this exact value — --migrate prints it verbatim
// on either path, so there is only one string to keep in sync.
var ErrReadFailed = errors.New("crontab read failed — refusing to migrate (unmanaged entries would be destroyed)")

// Crontab adopts the managed block: same script names, same expressions,
// rewritten in this binary's spelling under the current markers. It is a
// one-shot — a block that is already adopted is left alone, so calling this
// at every startup is free.
//
// Nothing is written unless a full backup of the pre-image crontab landed in
// dataDir first, and nothing is written at all when the read failed: an
// unreadable crontab means unknown contents, and rewriting one would destroy
// every unmanaged entry the user owns.
func Crontab(ct *cron.Crontab, dataDir string) (bool, error) {
	lines, ok := ct.Read()
	if !ok {
		return false, ErrReadFailed
	}

	schedules := ct.Schedules()
	if len(schedules) == 0 {
		return false, nil // no block, or a block with nothing parsable in it
	}
	if !needsAdoption(lines) {
		return false, nil
	}

	if err := backup(lines, dataDir); err != nil {
		return false, err
	}
	if err := ct.Save(schedules); err != nil {
		return false, err
	}
	return true, nil
}

// needsAdoption reports whether the block still carries pre-cutover bytes:
// a legacy marker pair, or any line inside the block written in PS spelling.
func needsAdoption(lines []string) bool {
	inBlock := false
	for _, line := range lines {
		switch {
		case line == cron.LegacyBlockStart, line == cron.LegacyBlockEnd:
			return true
		case line == cron.BlockStart:
			inBlock = true
		case line == cron.BlockEnd:
			inBlock = false
		case inBlock && strings.Contains(line, psSpelling):
			return true
		}
	}
	return false
}

// backup writes the whole pre-image crontab — managed block, unmanaged
// entries, comments, everything — to <dataDir>/crontab.bak.<RFC3339>.
func backup(lines []string, dataDir string) error {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dataDir, "crontab.bak."+time.Now().Format(time.RFC3339))
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600)
}

// Preview reports the managed block exactly as it stands today and the
// block Crontab would write if it ran right now — everything --migrate
// prints before it touches anything. Read-only (one read, the same
// wipe-guard Crontab itself applies) and side-effect-free: calling this
// commits to nothing, so a caller may print it and then decide not to
// proceed at all.
func Preview(ct *cron.Crontab) (current, planned []string, err error) {
	lines, ok := ct.Read()
	if !ok {
		return nil, nil, ErrReadFailed
	}
	current = cron.BlockLines(lines)
	planned = ct.RenderBlock(cron.SchedulesFromLines(lines))
	return current, planned, nil
}

// LatestBackup returns the most recently written crontab backup under
// dataDir (crontab.bak.<RFC3339>, sorted — RFC3339 timestamps order
// chronologically as plain strings), or "" if there is none. --migrate
// calls this right after a successful Crontab to report exactly which file
// it just wrote, rather than predicting backup's own filename ahead of the
// timestamp it picks internally.
func LatestBackup(dataDir string) string {
	matches, err := filepath.Glob(filepath.Join(dataDir, "crontab.bak.*"))
	if err != nil || len(matches) == 0 {
		return ""
	}
	sort.Strings(matches)
	return matches[len(matches)-1]
}
