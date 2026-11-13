package queryrun

import (
	"context"

	"github.com/lodthe/clickhouse-playground/internal/dbsettings"
	"github.com/lodthe/clickhouse-playground/internal/dbsettings/runsettings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/pkg/errors"
)

var ErrNotFound = errors.New("not found")

type Repository interface {
	Create(run *Run) error
	Get(id string) (*Run, error)
}

type Repo struct {
	ctx    context.Context
	client *dynamodb.Client

	tableName *string
}

func NewRepository(ctx context.Context, client *dynamodb.Client, tableName string) *Repo {
	return &Repo{
		ctx:       ctx,
		client:    client,
		tableName: aws.String(tableName),
	}
}

// Create stores the run with its output gzipped. When even the compressed item exceeds
// the DynamoDB size limit, the run is stored without the output and run.OutputDropped
// is set, so the caller can tell the user that the stored run has no output.
func (r *Repo) Create(run *Run) error {
	stored := prepareForStorage(run)

	marshaled, err := attributevalue.MarshalMap(stored)
	if err != nil {
		return errors.Wrap(err, "marshal failed")
	}

	if itemSize(marshaled) > maxItemSize {
		marshaled, err = attributevalue.MarshalMap(dropOutput(stored))
		if err != nil {
			return errors.Wrap(err, "marshal failed")
		}

		run.OutputDropped = true
	}

	_, err = r.client.PutItem(r.ctx, &dynamodb.PutItemInput{
		TableName: r.tableName,
		Item:      marshaled,
	})
	if err != nil {
		return errors.Wrap(err, "put failed")
	}

	return nil
}

func (r *Repo) Get(id string) (*Run, error) {
	out, err := r.client.GetItem(r.ctx, &dynamodb.GetItemInput{
		TableName: r.tableName,
		Key: map[string]types.AttributeValue{
			"Id": &types.AttributeValueMemberS{Value: id},
		},
	})
	if err != nil {
		return nil, errors.Wrap(err, "get failed")
	}

	run := new(Run)

	// Done because UnmarshalMap can't unmarshal in interface{}
	var databaseType dbsettings.Type
	_ = attributevalue.Unmarshal(out.Item["Database"], &databaseType)
	// TODO: add switch-case in the future
	if databaseType == dbsettings.TypeClickHouse {
		run.Settings = &runsettings.ClickHouseSettings{}
	}

	err = attributevalue.UnmarshalMap(out.Item, run)
	if err != nil {
		return nil, errors.Wrap(err, "unmarshal failed")
	}

	if run.ID == "" {
		return nil, ErrNotFound
	}

	err = restoreFromStorage(run)
	if err != nil {
		return nil, err
	}

	return run, nil
}
