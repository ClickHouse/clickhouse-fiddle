package dockerengine

import (
	"strings"
	"testing"
)

func TestExtractSanitizerReport(t *testing.T) {
	const asan = `Some normal server startup line
==42==ERROR: AddressSanitizer: heap-use-after-free on address 0x602000000010
    #0 0x55 in DB::doThing()
SUMMARY: AddressSanitizer: heap-use-after-free src/X.cpp:10 in DB::doThing()
==42==ABORTING`

	report := extractSanitizerReport(asan)
	if !strings.HasPrefix(report, sanitizerReportHeader) {
		t.Fatalf("missing header: %q", report)
	}
	if !strings.Contains(report, "AddressSanitizer: heap-use-after-free") {
		t.Errorf("report missing the error line: %q", report)
	}
	// The normal startup line precedes the first marker and must be excluded.
	if strings.Contains(report, "normal server startup line") {
		t.Errorf("report should start at the first sanitizer marker: %q", report)
	}
}

func TestExtractSanitizerReportVariants(t *testing.T) {
	cases := map[string]string{
		"tsan":  "WARNING: ThreadSanitizer: data race (pid=1)\nSUMMARY: ThreadSanitizer: data race src/Y.cpp:3",
		"msan":  "==7==WARNING: MemorySanitizer: use-of-uninitialized-value\nSUMMARY: MemorySanitizer: use-of-uninitialized-value",
		"ubsan": "src/Z.cpp:5:9: runtime error: signed integer overflow",
	}
	for name, stderr := range cases {
		if got := extractSanitizerReport(stderr); got == "" {
			t.Errorf("%s: expected a report, got empty", name)
		}
	}
}

func TestExtractSanitizerReportNoMarker(t *testing.T) {
	clean := "Processing configuration file.\nLogging to /var/log/clickhouse-server.\nReady for connections."
	if got := extractSanitizerReport(clean); got != "" {
		t.Errorf("expected no report for clean stderr, got %q", got)
	}
	if got := extractSanitizerReport(""); got != "" {
		t.Errorf("expected empty for empty stderr, got %q", got)
	}
}

func TestExtractSanitizerReportCap(t *testing.T) {
	var b strings.Builder
	b.WriteString("ERROR: AddressSanitizer: heap-buffer-overflow\n")
	for range maxSanitizerReportLines * 2 {
		b.WriteString("    #frame line\n")
	}

	report := extractSanitizerReport(b.String())
	// header + at most maxSanitizerReportLines report lines.
	if lines := strings.Count(report, "\n"); lines > maxSanitizerReportLines+1 {
		t.Errorf("report not capped: %d lines", lines)
	}
}
