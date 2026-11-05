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
	// Output remains for API and stored-run compatibility. RawOutput preserves the
	// exact byte sequence for clients that need to inspect non-UTF-8 output.
	Output    string `dynamodbav:"Output"`
	RawOutput []byte `dynamodbav:"RawOutput,omitempty"`

	// Gzipped replacements of Output/RawOutput, set when the plain values would not
	// fit the DynamoDB item size limit. See prepareForStorage.
	OutputGz    []byte `dynamodbav:"OutputGz,omitempty"`
	RawOutputGz []byte `dynamodbav:"RawOutputGz,omitempty"`

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
