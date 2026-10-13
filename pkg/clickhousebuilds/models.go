package clickhousebuilds

import (
	"regexp"
	"strings"
)

// Report is a partial view of a ClickHouse CI (praktika) result JSON document,
// e.g. result_releasebranchci.json. Only the fields the playground needs are decoded.
type Report struct {
	Name    string   `json:"name"`
	Status  string   `json:"status"`
	Results []Result `json:"results"`
}

// Result is a single CI check. Build jobs (e.g. "Build (amd_asan)") expose their
// produced package URLs in Links. Results may be nested.
type Result struct {
	Name    string   `json:"name"`
	Status  string   `json:"status"`
	Links   []string `json:"links"`
	Results []Result `json:"results"`
}

// successStatuses are the status strings praktika uses for a passed check. The value has
// varied across report versions ("success" on older reports, "OK" on newer ones).
var successStatuses = map[string]bool{"success": true, "ok": true}

// IsSuccessStatus reports whether a praktika check status means the check passed.
func IsSuccessStatus(status string) bool {
	return successStatuses[strings.ToLower(strings.TrimSpace(status))]
}

// FindResult performs a depth-first search for a result with the exact given name.
// It returns the first match and whether one was found.
func (r *Report) FindResult(name string) (Result, bool) {
	return findResult(r.Results, name)
}

func findResult(results []Result, name string) (Result, bool) {
	for _, res := range results {
		if res.Name == name {
			return res, true
		}
		if nested, ok := findResult(res.Results, name); ok {
			return nested, true
		}
	}

	return Result{}, false
}

// amdBuildRe matches an amd64 package build job, capturing the underscore-separated variant
// tokens. The CI matrix sometimes combines variants into one job, e.g. "Build (amd_asan_ubsan)".
var amdBuildRe = regexp.MustCompile(`^Build \(amd_(.+)\)$`)

// AMDBuildJob returns the first successful amd64 build job that produces the given variant
// (debug/asan/tsan/msan/ubsan). It matches by token, so a combined job such as
// "Build (amd_asan_ubsan)" satisfies both "asan" and "ubsan".
func (r *Report) AMDBuildJob(variant string) (Result, bool) {
	return findAMDBuild(r.Results, variant)
}

func findAMDBuild(results []Result, variant string) (Result, bool) {
	for _, res := range results {
		match := amdBuildRe.FindStringSubmatch(res.Name)
		if match != nil && IsSuccessStatus(res.Status) {
			for token := range strings.SplitSeq(match[1], "_") {
				if strings.EqualFold(token, variant) {
					return res, true
				}
			}
		}
		if nested, ok := findAMDBuild(res.Results, variant); ok {
			return nested, true
		}
	}

	return Result{}, false
}
