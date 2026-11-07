// SPDX-License-Identifier: MIT

// Package buildinfo formats the version line of mbcapctl: the values the
// linker injected, completed from the build information Go embeds in every
// binary.
package buildinfo

import (
	"encoding/json"
	"regexp"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"
)

// Library is an OT Fabric Go module the binary is built with.
type Library struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Info is what "version" prints. The field order is the JSON key order.
type Info struct {
	Name      string    `json:"name"`
	Version   string    `json:"version"`
	Tag       string    `json:"tag"`
	Commit    string    `json:"commit"`
	Built     string    `json:"built"`
	Libraries []Library `json:"libraries"`
	Go        string    `json:"go"`
	Platform  string    `json:"platform"`
}

// Get resolves the version of the running binary. version, tag, commit and
// built are the values injected with -ldflags -X; empty means not injected.
func Get(name, version, tag, commit, built string) Info {
	bi, _ := debug.ReadBuildInfo()
	return Resolve(name, version, tag, commit, built, bi)
}

// Resolve is Get with the build information passed in; bi may be nil.
func Resolve(name, version, tag, commit, built string, bi *debug.BuildInfo) Info {
	i := Info{
		Name:      name,
		Version:   strings.TrimPrefix(version, "v"),
		Tag:       tag,
		Commit:    commit,
		Built:     built,
		Libraries: []Library{},
		Go:        runtime.Version(),
		Platform:  runtime.GOOS + "/" + runtime.GOARCH,
	}
	if bi != nil {
		if i.Version == "" && isRelease(bi.Main.Version) {
			i.Version = strings.TrimPrefix(bi.Main.Version, "v")
		}
		if i.Commit == "" {
			i.Commit = vcsCommit(bi.Settings)
		}
		i.Libraries = libraries(bi)
	}
	if i.Version == "" {
		i.Version = "dev"
	}
	if i.Commit == "" {
		i.Commit = "unknown"
	}
	if i.Built == "" {
		i.Built = "unknown"
	}
	return i
}

// pseudo matches the tail of a Go pseudo-version: a 14-digit UTC time and a
// 12-character commit prefix.
var pseudo = regexp.MustCompile(`[.-]\d{14}-[0-9a-f]{12}$`)

// isRelease reports whether a main-module version from the build
// information is a released version: not "(devel)", not a pseudo-version
// and not a "+dirty" build of a checkout.
func isRelease(v string) bool {
	if v == "" || v == "(devel)" || strings.Contains(v, "+") {
		return false
	}
	return !pseudo.MatchString(v)
}

func vcsCommit(settings []debug.BuildSetting) string {
	var rev string
	var modified bool
	for _, s := range settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			modified = s.Value == "true"
		}
	}
	if rev != "" && modified {
		rev += "-dirty"
	}
	return rev
}

func libraries(bi *debug.BuildInfo) []Library {
	libs := []Library{}
	for _, d := range bi.Deps {
		if !strings.HasPrefix(d.Path, "github.com/otfabric/go-") || d.Path == bi.Main.Path {
			continue
		}
		m := d
		if d.Replace != nil {
			m = d.Replace
		}
		v := strings.TrimPrefix(m.Version, "v")
		if v == "" {
			v = "dev"
		}
		libs = append(libs, Library{Name: d.Path[strings.LastIndex(d.Path, "/")+1:], Version: v})
	}
	sort.Slice(libs, func(a, b int) bool { return libs[a].Name < libs[b].Name })
	return libs
}

// String is the one-line text form, without a newline.
func (i Info) String() string {
	parts := make([]string, 0, 5+len(i.Libraries))
	if i.Tag != "" {
		parts = append(parts, "tag "+i.Tag)
	}
	parts = append(parts, "commit "+i.Commit, "built "+i.Built)
	for _, l := range i.Libraries {
		parts = append(parts, l.Name+" "+l.Version)
	}
	parts = append(parts, i.Go, i.Platform)
	return i.Name + " " + i.Version + " (" + strings.Join(parts, ", ") + ")"
}

// JSON is the one-line JSON form, without a newline.
func (i Info) JSON() string {
	if i.Libraries == nil {
		i.Libraries = []Library{}
	}
	b, _ := json.Marshal(i) // cannot fail: only strings
	return string(b)
}
