// SPDX-License-Identifier: MIT

package buildinfo

import (
	"runtime"
	"runtime/debug"
	"strings"
	"testing"
)

const sha = "1802d0f9afe8642d1e278120b76fcdfac967cb9e"

var goPlatform = runtime.Version() + ", " + runtime.GOOS + "/" + runtime.GOARCH

func TestStringAllFields(t *testing.T) {
	i := Resolve("mbcapctl", "0.2.0", "v0.2.0", sha, "2026-11-05T15:41:49Z", nil)
	want := "mbcapctl 0.2.0 (tag v0.2.0, commit " + sha + ", built 2026-11-05T15:41:49Z, " + goPlatform + ")"
	if got := i.String(); got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestStringWithoutTag(t *testing.T) {
	i := Resolve("mbcapctl", "v0.2.0", "", sha, "2026-11-05T15:41:49Z", nil)
	want := "mbcapctl 0.2.0 (commit " + sha + ", built 2026-11-05T15:41:49Z, " + goPlatform + ")"
	if got := i.String(); got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestDevFallback(t *testing.T) {
	i := Resolve("mbcapctl", "", "", "", "", nil)
	want := "mbcapctl dev (commit unknown, built unknown, " + goPlatform + ")"
	if got := i.String(); got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	if got := i.JSON(); !strings.Contains(got, `"version":"dev","tag":"","commit":"unknown","built":"unknown","libraries":[]`) {
		t.Errorf("json %s", got)
	}
}

func TestFromBuildInfo(t *testing.T) {
	bi := &debug.BuildInfo{
		Main: debug.Module{Path: "github.com/otfabric/go-modbus-capture", Version: "v0.2.0"},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: sha},
			{Key: "vcs.modified", Value: "true"},
		},
	}
	i := Resolve("mbcapctl", "", "", "", "", bi)
	if i.Version != "0.2.0" || i.Commit != sha+"-dirty" {
		t.Errorf("%+v", i)
	}
	bi.Settings[1].Value = "false"
	if i := Resolve("mbcapctl", "", "", "", "", bi); i.Commit != sha {
		t.Errorf("clean commit %q", i.Commit)
	}
	// Injected values win over the build information.
	if i := Resolve("mbcapctl", "0.3.0", "", "abc", "", bi); i.Version != "0.3.0" || i.Commit != "abc" {
		t.Errorf("injected %+v", i)
	}
}

func TestNotReleaseVersions(t *testing.T) {
	for _, v := range []string{
		"", "(devel)",
		"v0.1.1-0.20261105154149-1802d0f9afe8",
		"v0.0.0-20261105154149-1802d0f9afe8",
		"v0.2.0-rc.1.0.20261105154149-1802d0f9afe8",
		"v0.1.0+dirty",
		"v0.1.1-0.20261105154149-1802d0f9afe8+dirty",
	} {
		bi := &debug.BuildInfo{Main: debug.Module{Version: v}}
		if got := Resolve("mbcapctl", "", "", "", "", bi).Version; got != "dev" {
			t.Errorf("%q: version %q, want dev", v, got)
		}
	}
	for v, want := range map[string]string{"v0.2.0": "0.2.0", "v1.0.0-rc.1": "1.0.0-rc.1"} {
		bi := &debug.BuildInfo{Main: debug.Module{Version: v}}
		if got := Resolve("mbcapctl", "", "", "", "", bi).Version; got != want {
			t.Errorf("%q: version %q, want %q", v, got, want)
		}
	}
}

func TestLibrariesFromBuildInfo(t *testing.T) {
	bi := &debug.BuildInfo{
		Main: debug.Module{Path: "github.com/otfabric/go-modbus-capture", Version: "v0.2.0"},
		Deps: []*debug.Module{
			{Path: "github.com/otfabric/go-modbus", Version: "v1.7.0"},
			{Path: "github.com/otfabric/go-modbus-capture", Version: "v0.2.0"},
			{Path: "github.com/otfabric/go-canbus", Version: "v0.8.0", Replace: &debug.Module{Path: "../go-canbus"}},
			{Path: "github.com/otfabric/go-knx", Version: "v0.1.0", Replace: &debug.Module{Path: "github.com/fork/go-knx", Version: "v0.1.1-0.20261105154149-1802d0f9afe8"}},
			{Path: "github.com/otfabric/docsync", Version: "v0.3.1"},
			{Path: "golang.org/x/sys", Version: "v0.30.0"},
		},
	}
	got := Resolve("mbcapctl", "", "", "", "", bi).Libraries
	want := []Library{{"go-canbus", "dev"}, {"go-knx", "0.1.1-0.20261105154149-1802d0f9afe8"}, {"go-modbus", "1.7.0"}}
	if len(got) != len(want) {
		t.Fatalf("libraries %+v", got)
	}
	for k := range want {
		if got[k] != want[k] {
			t.Errorf("library %d: %+v, want %+v", k, got[k], want[k])
		}
	}
}

func TestLibrariesFormatting(t *testing.T) {
	base := Info{Name: "mbcapctl", Version: "0.2.0", Tag: "v0.2.0", Commit: sha, Built: "2026-11-05T15:41:49Z", Go: "go1.26.10", Platform: "darwin/arm64"}
	cases := []struct {
		libs     []Library
		text     string
		jsonLibs string
	}{
		{nil, "", `[]`},
		{[]Library{{"go-canbus", "0.8.0"}}, "go-canbus 0.8.0, ", `[{"name":"go-canbus","version":"0.8.0"}]`},
		{[]Library{{"go-canbus", "0.8.0"}, {"go-canopen", "0.5.1"}}, "go-canbus 0.8.0, go-canopen 0.5.1, ",
			`[{"name":"go-canbus","version":"0.8.0"},{"name":"go-canopen","version":"0.5.1"}]`},
	}
	for _, c := range cases {
		i := base
		i.Libraries = c.libs
		wantText := "mbcapctl 0.2.0 (tag v0.2.0, commit " + sha + ", built 2026-11-05T15:41:49Z, " + c.text + "go1.26.10, darwin/arm64)"
		if got := i.String(); got != wantText {
			t.Errorf("text\ngot  %s\nwant %s", got, wantText)
		}
		wantJSON := `{"name":"mbcapctl","version":"0.2.0","tag":"v0.2.0","commit":"` + sha + `","built":"2026-11-05T15:41:49Z","libraries":` +
			c.jsonLibs + `,"go":"go1.26.10","platform":"darwin/arm64"}`
		if got := i.JSON(); got != wantJSON {
			t.Errorf("json\ngot  %s\nwant %s", got, wantJSON)
		}
	}
}

func TestGet(t *testing.T) {
	i := Get("mbcapctl", "", "", "", "")
	if i.Name != "mbcapctl" || i.Version == "" || i.Commit == "" || i.Built != "unknown" || i.Libraries == nil {
		t.Errorf("%+v", i)
	}
}
