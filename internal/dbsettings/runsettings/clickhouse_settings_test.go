package runsettings

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFormatArgs(t *testing.T) {
	cases := []struct {
		version      string
		outputFormat string
		expected     []string
	}{
		{"20.3", "", nil},
		{"21", "", []string{"--output_format_pretty_color", "0", "--format", "TabSeparated"}},
		{"24.8", "JSON", []string{"--output_format_pretty_color", "0", "--format", "JSON"}},
		{"26.7.3.1", "", []string{"--output_format_pretty_color", "0", "--format", "TabSeparated"}},
		{"26.8", "", []string{"--output_format_pretty_color", "0", "--default_format", "TabSeparated"}},
		{"26.8.2.7", "PrettyCompact", []string{"--output_format_pretty_color", "0", "--default_format", "PrettyCompact"}},
		{"26.8-alpine", "", []string{"--output_format_pretty_color", "0", "--default_format", "TabSeparated"}},
		{"27.1", "CSV", []string{"--output_format_pretty_color", "0", "--default_format", "CSV"}},
		{"head", "", []string{"--output_format_pretty_color", "0", "--default_format", "TabSeparated"}},
		{"latest", "", []string{"--output_format_pretty_color", "0", "--default_format", "TabSeparated"}},
	}

	for _, tc := range cases {
		t.Run(tc.version+"/"+tc.outputFormat, func(t *testing.T) {
			cs := &ClickHouseSettings{OutputFormat: tc.outputFormat}
			assert.Equal(t, tc.expected, cs.FormatArgs(tc.version, "TabSeparated"))
		})
	}
}
