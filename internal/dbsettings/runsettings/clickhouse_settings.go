package runsettings

import (
	"github.com/lodthe/clickhouse-playground/internal/dbsettings"
	"github.com/lodthe/clickhouse-playground/pkg/chspec"
)

// ClickHouseSettings contains settings for clickhouse client
type ClickHouseSettings struct {
	OutputFormat string `dynamodbav:"OutputFormat"`
}

func (cs *ClickHouseSettings) Type() dbsettings.Type {
	return dbsettings.TypeClickHouse
}

// FormatArgs gets args for custom output formatting
//
// Returns empty args if database version doesn't support --format flag
func (cs *ClickHouseSettings) FormatArgs(version string, defaultOutputFormat string) []string {
	var result []string

	// Check if database version supports --format flag
	if chspec.IsAtLeastMajor(version, "21") {
		outputFormat := defaultOutputFormat
		if cs.OutputFormat != "" {
			outputFormat = cs.OutputFormat
		}

		result = append(result,
			"--output_format_pretty_color", "0",
			formatFlag(version), outputFormat)
	}

	return result
}

// formatFlag returns the clickhouse client flag that sets the output format.
//
// Since 26.8 --format maps to the output_format setting, which overrides the FORMAT clause
// of a query (https://github.com/ClickHouse/ClickHouse/pull/105249). --default_format is
// only a fallback, so the FORMAT clause keeps working. Older versions don't know this flag.
func formatFlag(version string) string {
	if chspec.IsAtLeastMajor(version, "26.8") {
		return "--default_format"
	}

	return "--format"
}
