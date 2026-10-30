// Package classify turns rclone's output into a failure a human can act on.
package classify

import "regexp"

type Kind string

const (
	KindPermissions   Kind = "permissions"
	KindAuth          Kind = "auth"
	KindNetwork       Kind = "network"
	KindCaseClash     Kind = "case-clash"
	KindTooManyDel    Kind = "too-many-deletes"
	KindNameTooLong   Kind = "name-too-long"
	KindNeedsResync   Kind = "needs-resync"
	KindFilterChange  Kind = "filters-changed"
	KindNoMountDriver Kind = "no-mount-driver"
	KindMountBusy     Kind = "mount-busy"
	KindUnknown       Kind = "unknown"
)

// Failure is a classified rclone failure.
type Failure struct {
	Kind    Kind
	Message string
	// Pause means syncing again blindly would make things worse, so a human
	// has to look first.
	Pause bool
}

type rule struct {
	re *regexp.Regexp
	f  Failure
}

// Order matters: the first matching rule wins, so specific causes go first.
var rules = []rule{
	{regexp.MustCompile(`(?i)cannot find winfsp|winfsp.*not (?:found|installed)|fusermount[^\n]*(?:not found|no such file)|fuse device not found|/dev/fuse`),
		Failure{KindNoMountDriver, "No file system driver for mounting was found. Install WinFsp (Windows, https://winfsp.dev) or FUSE (Linux: your distribution's fuse3 package), then restart the daemon.", false}},
	{regexp.MustCompile(`(?i)mountpoint path already exists|already mounted|directory not empty|mount point is busy`),
		Failure{KindMountBusy, "The mount point is already in use or not empty. Pick an unused drive letter or an empty folder as mount_point.", false}},
	{regexp.MustCompile(`(?i)too many deletes|max-delete|Safety abort`),
		Failure{KindTooManyDel, "The sync would delete more files than max_delete allows. Check that the source folder is intact, then resume.", true}},
	{regexp.MustCompile(`(?i)differ(?:s|ing)? only (?:in|by) case|case[- ]insensitive`),
		Failure{KindCaseClash, "Two files or folders differ only by letter case, which Windows cannot hold side by side. Rename one, then resume.", true}},
	{regexp.MustCompile(`(?i)access is denied|permission denied|operation not permitted`),
		Failure{KindPermissions, "A file could not be read or written because of its permissions. Check the access rights of the file named in the log.", false}},
	{regexp.MustCompile(`(?i)unable to authenticate|handshake failed|no supported methods remain|host key|invalid access token`),
		Failure{KindAuth, "Authentication with the server failed. Check the SSH key, the pinned host key and the crypt password.", false}},
	{regexp.MustCompile(`(?i)connection refused|i/o timeout|no such host|network is unreachable|connection reset|broken pipe`),
		Failure{KindNetwork, "The server could not be reached. This usually clears on its own.", false}},
	// The server answers an over-long (encrypted) file name with a bare SFTP
	// "Bad message"; this is how names of 144 bytes or more showed up in practice.
	{regexp.MustCompile(`(?i)SSH_FX_BAD_MESSAGE|"Bad message"`),
		Failure{KindNameTooLong, "Some file names are too long once encrypted (the limit is 143 bytes) and could not be uploaded. Run `foxcloud check-names` for the list, then shorten them or exclude them in the filter file.", false}},
	// Must precede the generic resync rule: it prints the same "must run --resync".
	{regexp.MustCompile(`(?i)filters file has changed`),
		Failure{KindFilterChange, "The filter file changed since the last bisync. Bisync has to rebuild its baseline with a full resync (newer file wins), which can move files that the new filters include or exclude. Check the filter change is intended, then resume to resync.", true}},
	{regexp.MustCompile(`(?i)must run --resync|bisync aborted|critical error`),
		Failure{KindNeedsResync, "Bisync lost track of its previous state and needs a fresh --resync.", false}},
}

// Classify returns the first matching failure for the given rclone output.
func Classify(output string) Failure {
	for _, r := range rules {
		if r.re.MatchString(output) {
			return r.f
		}
	}
	return Failure{Kind: KindUnknown, Message: "The sync failed for a reason FoxCloud does not recognise. See the log for details."}
}
