package buildinfo

import (
	"runtime"
	"runtime/debug"
	"testing"
)

func TestResolvePrefersLinkerStamps(t *testing.T) {
	Version, Commit, BuildDate = "v1.2.3", "abc123", "2026-07-26T00:00:00Z"
	t.Cleanup(func() { Version, Commit, BuildDate = "", "", "" })

	info := resolve(func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "from-vcs"},
			{Key: "vcs.time", Value: "1999-01-01T00:00:00Z"},
			{Key: "vcs.modified", Value: "true"},
		}}, true
	})

	if info.Version != "v1.2.3" || info.Commit != "abc123" || info.BuildDate != "2026-07-26T00:00:00Z" {
		t.Fatalf("info = %#v, want the linker stamps", info)
	}
	if !info.Modified {
		t.Fatal("modified = false, want the VCS dirty flag to survive")
	}
	if info.GoVersion != runtime.Version() {
		t.Fatalf("go_version = %q, want %q", info.GoVersion, runtime.Version())
	}
}

func TestResolveFallsBackToVCSStamps(t *testing.T) {
	info := resolve(func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{
			Main: debug.Module{Version: "v0.9.0"},
			Settings: []debug.BuildSetting{
				{Key: "vcs.revision", Value: "from-vcs"},
				{Key: "vcs.time", Value: "1999-01-01T00:00:00Z"},
			},
		}, true
	})

	if info.Version != "v0.9.0" || info.Commit != "from-vcs" || info.BuildDate != "1999-01-01T00:00:00Z" {
		t.Fatalf("info = %#v, want the VCS stamps", info)
	}
	if info.Modified {
		t.Fatal("modified = true without a vcs.modified stamp")
	}
}

func TestResolveNeverReturnsEmptyFields(t *testing.T) {
	for name, read := range map[string]func() (*debug.BuildInfo, bool){
		"nil reader":     nil,
		"unavailable":    func() (*debug.BuildInfo, bool) { return nil, false },
		"empty info":     func() (*debug.BuildInfo, bool) { return &debug.BuildInfo{}, true },
		"devel main mod": func() (*debug.BuildInfo, bool) { return &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}, true },
	} {
		t.Run(name, func(t *testing.T) {
			info := resolve(read)
			if info.Version == "" || info.Commit == "" || info.BuildDate == "" || info.GoVersion == "" {
				t.Fatalf("info = %#v, want placeholder values instead of empty strings", info)
			}
		})
	}
}

func TestGetIsStable(t *testing.T) {
	first := Get()
	if first != Get() {
		t.Fatal("Get() is not stable")
	}
	if first.GoVersion == "" {
		t.Fatalf("info = %#v", first)
	}
}
