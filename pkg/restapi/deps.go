package restapi

import (
	"context"

	"github.com/lodthe/clickhouse-playground/internal/buildtype"
	"github.com/lodthe/clickhouse-playground/internal/dockertag"
	"github.com/lodthe/clickhouse-playground/internal/qrunner"
	"github.com/lodthe/clickhouse-playground/internal/queryrun"
)

type TagStorage interface {
	GetAll() []dockertag.Image
	Exists(tag string) bool
}

type QueryRunner interface {
	RunQuery(ctx context.Context, run *queryrun.Run) (qrunner.QueryOutput, error)
}

// ImagePreparer triggers and reports on local image builds for non-release build types.
type ImagePreparer interface {
	EnsureImage(ctx context.Context, version string, bt buildtype.BuildType) (qrunner.ImageStatus, error)
}
