package queryrun

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/lodthe/clickhouse-playground/internal/dbsettings/runsettings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// TestRepositoryRoundTrip verifies Create/Get against a real local DynamoDB, which
// enforces the 400 KB item limit: a large compressible output is stored in full, an
// output that does not fit even compressed is dropped with a flag, and a small run
// round-trips with its exact bytes.
func TestRepositoryRoundTrip(t *testing.T) {
	if os.Getenv("RUN_DOCKER_TESTS") == "" {
		t.Skip("Skipping a docker test. Set RUN_DOCKER_TESTS=true to enable.")
	}

	ctx := context.Background()

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "amazon/dynamodb-local:2.6.1",
			ExposedPorts: []string{"8000/tcp"},
			WaitingFor:   wait.ForListeningPort("8000/tcp"),
		},
		Started: true,
	})
	testcontainers.CleanupContainer(t, container)
	require.NoError(t, err)

	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "8000")
	require.NoError(t, err)
	endpoint := fmt.Sprintf("http://%s:%s", host, port.Port())

	cfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion("us-east-2"),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("dummy", "dummy", "")),
	)
	require.NoError(t, err)

	client := dynamodb.NewFromConfig(cfg, func(o *dynamodb.Options) {
		o.BaseEndpoint = aws.String(endpoint)
	})

	const tableName = "QueryRunsTest"
	_, err = client.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName:            aws.String(tableName),
		AttributeDefinitions: []types.AttributeDefinition{{AttributeName: aws.String("Id"), AttributeType: types.ScalarAttributeTypeS}},
		KeySchema:            []types.KeySchemaElement{{AttributeName: aws.String("Id"), KeyType: types.KeyTypeHash}},
		BillingMode:          types.BillingModePayPerRequest,
	})
	require.NoError(t, err)

	repo := NewRepository(ctx, client, tableName)

	// The system.privileges incident shape: ~370 KB of padded Pretty output, far beyond
	// the item limit as plain text, a few KB gzipped.
	padded := strings.Repeat("│ ALTER UPDATE   │ ['UPDATE']   │ COLUMN   │ ALTER TABLE │\n", 6200)
	run := New("SELECT * FROM system.privileges;", "clickhouse", "latest", "release",
		&runsettings.ClickHouseSettings{OutputFormat: "PrettyMonoBlock"})
	run.Output = padded
	run.RawOutput = []byte(padded)

	require.NoError(t, repo.Create(run))
	require.False(t, run.OutputDropped)

	got, err := repo.Get(run.ID)
	require.NoError(t, err)
	require.Equal(t, padded, got.Output)
	require.Equal(t, []byte(padded), got.RawOutput)
	require.False(t, got.OutputDropped)
	require.Empty(t, got.OutputGz)

	// Output that does not compress (random bytes) and does not fit the item: the run is
	// stored without it, and both the saved run and the read one carry the flag.
	noise := make([]byte, 600<<10)
	_, err = rand.Read(noise)
	require.NoError(t, err)

	large := New("SELECT randomString(600000)", "clickhouse", "latest", "release", &runsettings.ClickHouseSettings{})
	large.Output = string(noise)
	large.RawOutput = noise
	large.OutputTruncated = true

	require.NoError(t, repo.Create(large))
	require.True(t, large.OutputDropped)
	// The caller's copy of the output is untouched: the response still carries it.
	require.Equal(t, noise, large.RawOutput)

	gotLarge, err := repo.Get(large.ID)
	require.NoError(t, err)
	require.True(t, gotLarge.OutputDropped)
	require.True(t, gotLarge.OutputTruncated)
	require.Empty(t, gotLarge.Output)
	require.Empty(t, gotLarge.RawOutput)
	require.Equal(t, "SELECT randomString(600000)", gotLarge.Input)

	// A small run with bytes that are not valid UTF-8 comes back exactly.
	small := New("SELECT unhex('FF00')", "clickhouse", "latest", "release", &runsettings.ClickHouseSettings{})
	small.Output = "\xff\\0\n"
	small.RawOutput = []byte(small.Output)

	require.NoError(t, repo.Create(small))

	gotSmall, err := repo.Get(small.ID)
	require.NoError(t, err)
	require.Equal(t, []byte{0xff, '\\', '0', '\n'}, gotSmall.RawOutput)
	require.Equal(t, "\xff\\0\n", gotSmall.Output)
}
