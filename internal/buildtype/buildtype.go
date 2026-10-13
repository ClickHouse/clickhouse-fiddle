// Package buildtype enumerates the kinds of ClickHouse builds the playground can run.
//
// The default build type is Release: images are pulled from the public Docker Hub
// repositories. The other build types (debug and sanitizers) are not published to
// Docker Hub; their images are built locally from the .deb packages produced by
// ClickHouse CI on release branches (see the internal/cibuild package).
package buildtype

import (
	"fmt"
	"slices"
	"strings"
)

// BuildType identifies the kind of ClickHouse build to run.
type BuildType string

const (
	Release BuildType = "release"
	Debug   BuildType = "debug"
	ASAN    BuildType = "asan"
	TSAN    BuildType = "tsan"
	MSAN    BuildType = "msan"
	UBSAN   BuildType = "ubsan"
)

// Default is the build type assumed when a request does not specify one.
const Default = Release

// all lists every supported build type in display order.
var all = []BuildType{Release, Debug, ASAN, TSAN, MSAN, UBSAN}

// All returns every supported build type.
func All() []BuildType {
	out := make([]BuildType, len(all))
	copy(out, all)

	return out
}

// Parse validates and normalizes a raw build type string.
// An empty string is treated as the default (Release) for backward compatibility.
func Parse(raw string) (BuildType, error) {
	if raw == "" {
		return Default, nil
	}

	bt := BuildType(strings.ToLower(strings.TrimSpace(raw)))
	if slices.Contains(all, bt) {
		return bt, nil
	}

	return "", fmt.Errorf("unknown build type %q", raw)
}

// IsRelease reports whether the build type is the default Docker Hub release build.
func (b BuildType) IsRelease() bool {
	return b == Release || b == ""
}

// String returns the build type as a string. It also doubles as the variant token used to
// match a CI build job (e.g. "asan" matches a "Build (amd_asan)" or "Build (amd_asan_ubsan)"
// job).
func (b BuildType) String() string {
	return string(b)
}
