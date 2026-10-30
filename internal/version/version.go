// Package version holds the build version, stamped in by the release build.
package version

// Version is "dev" for a local build; releases set it with
// -ldflags "-X github.com/WentTheFox/FoxCloud/internal/version.Version=v1.2.3".
var Version = "dev"
