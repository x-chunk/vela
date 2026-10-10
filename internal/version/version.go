// Package version exposes build metadata for both binaries.
//
// Release builds inject Version, Commit and Date through -ldflags -X. Builds
// made with plain "go install module@vX.Y.Z" carry no ldflags, so Get falls
// back to the module and VCS information the Go toolchain embeds in every
// binary (debug.ReadBuildInfo).
package version

import (
	"fmt"
	"runtime"
	"runtime/debug"
)

// Values injected at link time. They are variables, not constants, because
// -ldflags -X can only override package-level string variables.
var (
	Version = ""
	Commit  = ""
	Date    = ""
)

// Info is the resolved build metadata.
type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	Date      string `json:"date"`
	GoVersion string `json:"go_version"`
	Platform  string `json:"platform"`
}

// Get returns the build metadata, preferring link-time values and falling
// back to the information embedded by the Go toolchain.
func Get() Info {
	info := Info{
		Version:   Version,
		Commit:    Commit,
		Date:      Date,
		GoVersion: runtime.Version(),
		Platform:  runtime.GOOS + "/" + runtime.GOARCH,
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		if info.Version == "" && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
			info.Version = bi.Main.Version
		}
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				if info.Commit == "" {
					info.Commit = s.Value
				}
			case "vcs.time":
				if info.Date == "" {
					info.Date = s.Value
				}
			}
		}
	}
	if info.Version == "" {
		info.Version = "dev"
	}
	if info.Commit == "" {
		info.Commit = "none"
	}
	if info.Date == "" {
		info.Date = "unknown"
	}
	return info
}

// String renders a one-line human readable description.
func (i Info) String() string {
	return fmt.Sprintf("%s (commit %s, built %s, %s, %s)", i.Version, shortCommit(i.Commit), i.Date, i.GoVersion, i.Platform)
}

func shortCommit(c string) string {
	if len(c) > 12 {
		return c[:12]
	}
	return c
}
