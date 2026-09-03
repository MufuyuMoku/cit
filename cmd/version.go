package cmd

import "runtime/debug"

// Build metadata. Overridden at link time; see the ldflags in the Makefile.
var (
	// Version is the human-readable release version.
	Version = "0.0.0-dev"
	// Commit is the git revision the binary was built from.
	Commit = ""
	// BuildDate is the RFC 3339 timestamp of the build.
	BuildDate = ""
)

// BuildInfo is the build metadata handed to the frontend.
type BuildInfo struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildDate string `json:"buildDate"`
}

// buildInfo resolves the link-time values, falling back to whatever the Go
// toolchain stamped into the binary when they were not supplied.
func buildInfo() BuildInfo {
	info := BuildInfo{
		Version:   Version,
		Commit:    Commit,
		BuildDate: BuildDate,
	}

	if info.Commit != "" && info.BuildDate != "" {
		return info
	}

	stamped, ok := debug.ReadBuildInfo()
	if !ok {
		return info
	}
	for _, setting := range stamped.Settings {
		switch setting.Key {
		case "vcs.revision":
			if info.Commit == "" {
				info.Commit = setting.Value
			}
		case "vcs.time":
			if info.BuildDate == "" {
				info.BuildDate = setting.Value
			}
		}
	}
	return info
}
