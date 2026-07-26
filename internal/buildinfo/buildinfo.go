// Package buildinfo exposes the image/source provenance of the running binary
// so a deployed gateway can be tied back to the exact source it was built from.
package buildinfo

import (
	"runtime"
	"runtime/debug"
	"sync"
)

// Stamped at link time by the Dockerfile:
//
//	-ldflags "-X github.com/teslashibe/open-agent-api/internal/buildinfo.Version=..."
//
// When they are empty (plain `go build`, `go test`) the VCS stamps recorded by
// the toolchain in the binary are used instead.
var (
	Version   = ""
	Commit    = ""
	BuildDate = ""
)

// Info is the JSON-serializable provenance record served by /health and by the
// structured inference contract.
type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildDate string `json:"build_date"`
	GoVersion string `json:"go_version"`
	Modified  bool   `json:"modified"`
}

var (
	once     sync.Once
	resolved Info
)

// Get returns the provenance of this binary. The value is resolved once and is
// safe for concurrent use.
func Get() Info {
	once.Do(func() {
		resolved = resolve(debug.ReadBuildInfo)
	})
	return resolved
}

func resolve(read func() (*debug.BuildInfo, bool)) Info {
	info := Info{
		Version:   Version,
		Commit:    Commit,
		BuildDate: BuildDate,
		GoVersion: runtime.Version(),
	}

	build, ok := readBuildInfo(read)
	if ok {
		for _, setting := range build.Settings {
			switch setting.Key {
			case "vcs.revision":
				if info.Commit == "" {
					info.Commit = setting.Value
				}
			case "vcs.time":
				if info.BuildDate == "" {
					info.BuildDate = setting.Value
				}
			case "vcs.modified":
				info.Modified = setting.Value == "true"
			}
		}
		if info.Version == "" && build.Main.Version != "" && build.Main.Version != "(devel)" {
			info.Version = build.Main.Version
		}
		if info.GoVersion == "" {
			info.GoVersion = build.GoVersion
		}
	}

	if info.Version == "" {
		info.Version = "devel"
	}
	if info.Commit == "" {
		info.Commit = "unknown"
	}
	if info.BuildDate == "" {
		info.BuildDate = "unknown"
	}
	return info
}

func readBuildInfo(read func() (*debug.BuildInfo, bool)) (*debug.BuildInfo, bool) {
	if read == nil {
		return nil, false
	}
	build, ok := read()
	if !ok || build == nil {
		return nil, false
	}
	return build, true
}
