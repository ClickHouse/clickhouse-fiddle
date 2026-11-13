package queryrun

import (
	"time"

	"github.com/lodthe/clickhouse-playground/internal/dbsettings/runsettings"

	"github.com/google/uuid"
)

type Run struct {
	ID string `dynamodbav:"Id"`

	Version string `dynamodbav:"Version"`
	// BuildType is the ClickHouse build kind (release/debug/asan/...). Empty means release.
	BuildType string `dynamodbav:"BuildType"`
	Input     string `dynamodbav:"Input"`
	// Output is the query output as text; RawOutput is the same output as the exact
	// bytes, for clients that inspect non-UTF-8 output. In memory both are always set.
	// In storage only OutputGz is written; the plain attributes are read for records
	// written before compression (see restoreFromStorage).
	Output    string `dynamodbav:"Output,omitempty"`
	RawOutput []byte `dynamodbav:"RawOutput,omitempty"`

	// OutputGz is the gzipped exact output bytes, the only form written to storage.
	// RawOutputGz exists in a few records written by an intermediate version.
	OutputGz    []byte `dynamodbav:"OutputGz,omitempty"`
	RawOutputGz []byte `dynamodbav:"RawOutputGz,omitempty"`

	// OutputTruncated is set when the runner stopped reading the output at its cap.
	OutputTruncated bool `dynamodbav:"OutputTruncated,omitempty"`
	// OutputDropped is set when the output was not stored because the item would exceed
	// the DynamoDB size limit. Such a run has to be executed again to see its output.
	OutputDropped bool `dynamodbav:"OutputDropped,omitempty"`

	Database string                  `dynamodbav:"Database"`
	Settings runsettings.RunSettings `dynamodbav:"Settings"`

	CreatedAt     time.Time     `dynamodbav:"CreatedAt"`
	ExecutionTime time.Duration `dynamodbav:"ExecutionTime"`
}

func New(input string, database string, version string, buildType string, settings runsettings.RunSettings) *Run {
	return &Run{
		ID:        uuid.New().String(),
		CreatedAt: time.Now(),
		Input:     input,
		Database:  database,
		Version:   version,
		BuildType: buildType,
		Settings:  settings,
	}
}
