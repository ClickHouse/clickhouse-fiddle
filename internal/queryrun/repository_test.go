package queryrun

import (
	"context"
	"fmt"
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

// TestRepositoryRoundTrip verifies that runs whose plain representation exceeds the
// DynamoDB 400 KB item limit survive Create/Get against a real local DynamoDB,
// which enforces the limit.
func TestRepositoryRoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping a test that starts a DynamoDB container.")
	}

	ctx := context.Background()

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "amazon/dynamodb-local:latest",
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

	// The system.privileges incident shape: ~370 KB of padded Pretty output,
	// stored both as Output and RawOutput (~740 KB plain, far beyond the item limit).
	padded := strings.Repeat("│ ALTER UPDATE   │ ['UPDATE']   │ COLUMN   │ ALTER TABLE │\n", 6200)
	run := New("SELECT * FROM system.privileges;", "clickhouse", "latest", "release",
		&runsettings.ClickHouseSettings{OutputFormat: "PrettyMonoBlock"})
	run.Output = padded
	run.RawOutput = []byte(padded)

	require.NoError(t, repo.Create(run))

	got, err := repo.Get(run.ID)
	require.NoError(t, err)
	require.Equal(t, padded, got.Output)
	require.Equal(t, []byte(padded), got.RawOutput)
	require.Empty(t, got.OutputGz)
	require.Empty(t, got.RawOutputGz)

	// A small run keeps the plain, console-readable representation.
	small := New("SELECT 1", "clickhouse", "latest", "release", &runsettings.ClickHouseSettings{})
	small.Output = "1\n"
	small.RawOutput = []byte("1\n")

	require.NoError(t, repo.Create(small))

	gotSmall, err := repo.Get(small.ID)
	require.NoError(t, err)
	require.Equal(t, "1\n", gotSmall.Output)
}
