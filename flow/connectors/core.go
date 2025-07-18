package connectors

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"google.golang.org/protobuf/proto"

	connbigquery "github.com/PeerDB-io/peerdb/flow/connectors/bigquery"
	connclickhouse "github.com/PeerDB-io/peerdb/flow/connectors/clickhouse"
	connelasticsearch "github.com/PeerDB-io/peerdb/flow/connectors/elasticsearch"
	conneventhub "github.com/PeerDB-io/peerdb/flow/connectors/eventhub"
	connkafka "github.com/PeerDB-io/peerdb/flow/connectors/kafka"
	connmongo "github.com/PeerDB-io/peerdb/flow/connectors/mongo"
	connmysql "github.com/PeerDB-io/peerdb/flow/connectors/mysql"
	connpostgres "github.com/PeerDB-io/peerdb/flow/connectors/postgres"
	connpubsub "github.com/PeerDB-io/peerdb/flow/connectors/pubsub"
	conns3 "github.com/PeerDB-io/peerdb/flow/connectors/s3"
	connsnowflake "github.com/PeerDB-io/peerdb/flow/connectors/snowflake"
	"github.com/PeerDB-io/peerdb/flow/generated/protos"
	"github.com/PeerDB-io/peerdb/flow/internal"
	"github.com/PeerDB-io/peerdb/flow/model"
	"github.com/PeerDB-io/peerdb/flow/otel_metrics"
	"github.com/PeerDB-io/peerdb/flow/shared"
	"github.com/PeerDB-io/peerdb/flow/shared/exceptions"
)

type Connector interface {
	Close() error
	ConnectionActive(context.Context) error
}

type ValidationConnector interface {
	Connector

	// ValidationCheck performs validation for the connectors,
	// usually includes permissions to create and use objects (tables, schema etc).
	ValidateCheck(context.Context) error
}

type MirrorSourceValidationConnector interface {
	GetTableSchemaConnector

	ValidateMirrorSource(context.Context, *protos.FlowConnectionConfigs) error
}

type MirrorDestinationValidationConnector interface {
	Connector

	ValidateMirrorDestination(context.Context, *protos.FlowConnectionConfigs, map[string]*protos.TableSchema) error
}

type StatActivityConnector interface {
	Connector

	StatActivity(context.Context, *protos.PostgresPeerActivityInfoRequest) (*protos.PeerStatResponse, error)
}

type GetTableSchemaConnector interface {
	Connector

	// GetTableSchema returns the schema of a table in terms of type system.
	GetTableSchema(
		ctx context.Context,
		env map[string]string,
		version uint32,
		system protos.TypeSystem,
		tableMappings []*protos.TableMapping,
	) (map[string]*protos.TableSchema, error)
}

type GetSchemaConnector interface {
	Connector

	GetAllTables(context.Context) (*protos.AllTablesResponse, error)
	GetColumns(ctx context.Context, version uint32, schema string, table string) (*protos.TableColumnsResponse, error)
	GetSchemas(ctx context.Context) (*protos.PeerSchemasResponse, error)
	GetTablesInSchema(ctx context.Context, schema string, cdcEnabled bool) (*protos.SchemaTablesResponse, error)
}

type CDCPullConnectorCore interface {
	GetTableSchemaConnector

	// EnsurePullability ensures that the connector is pullable.
	EnsurePullability(ctx context.Context, req *protos.EnsurePullabilityBatchInput) (
		*protos.EnsurePullabilityBatchOutput, error)

	// For InitialSnapshotOnly correctness without replication slot
	// `any` is for returning transaction if necessary
	ExportTxSnapshot(context.Context, map[string]string) (*protos.ExportTxSnapshotOutput, any, error)

	// `any` from ExportSnapshot passed here when done, allowing transaction to commit
	FinishExport(any) error

	// Setup replication in prep for initial copy
	SetupReplication(context.Context, *protos.SetupReplicationInput) (model.SetupReplicationResult, error)

	// Methods related to retrieving and pushing records for this connector as a source and destination.
	SetupReplConn(context.Context) error

	// Ping source to keep connection alive. Can be called concurrently with pulling records; skips ping in that case.
	ReplPing(context.Context) error

	// Called when offset has been confirmed to destination
	UpdateReplStateLastOffset(ctx context.Context, lastOffset model.CdcCheckpoint) error

	// PullFlowCleanup drops both the Postgres publication and replication slot, as a part of DROP MIRROR
	PullFlowCleanup(ctx context.Context, jobName string) error
}

type CDCPullConnector interface {
	CDCPullConnectorCore

	// This method should be idempotent, and should be able to be called multiple times with the same request.
	PullRecords(
		ctx context.Context,
		catalogPool shared.CatalogPool,
		otelManager *otel_metrics.OtelManager,
		req *model.PullRecordsRequest[model.RecordItems],
	) error
}

type CDCPullPgConnector interface {
	CDCPullConnectorCore

	// This method should be idempotent, and should be able to be called multiple times with the same request.
	// It's signature, aside from type parameter, should match CDCPullConnector.PullRecords.
	PullPg(
		ctx context.Context,
		catalogPool shared.CatalogPool,
		otelManager *otel_metrics.OtelManager,
		req *model.PullRecordsRequest[model.PgItems],
	) error
}

type NormalizedTablesConnector interface {
	Connector

	// StartSetupNormalizedTables may be used to have SetupNormalizedTable calls run in a transaction.
	StartSetupNormalizedTables(ctx context.Context) (any, error)

	// CleanupSetupNormalizedTables may be used to rollback transaction started by StartSetupNormalizedTables.
	// Calling CleanupSetupNormalizedTables after FinishSetupNormalizedTables must be a nop.
	CleanupSetupNormalizedTables(ctx context.Context, tx any)

	// FinishSetupNormalizedTables may be used to finish transaction started by StartSetupNormalizedTables.
	FinishSetupNormalizedTables(ctx context.Context, tx any) error

	// SetupNormalizedTable sets up the normalized table on the connector.
	SetupNormalizedTable(
		ctx context.Context,
		tx any,
		config *protos.SetupNormalizedTableBatchInput,
		destinationTableIdentifier string,
		sourceTableSchema *protos.TableSchema,
	) (bool, error)
}

type CDCSyncConnectorCore interface {
	Connector

	// NeedsSetupMetadataTables checks if the metadata table [PEERDB_MIRROR_JOBS] needs to be created.
	NeedsSetupMetadataTables(ctx context.Context) (bool, error)

	// SetupMetadataTables creates the metadata table [PEERDB_MIRROR_JOBS] if necessary.
	SetupMetadataTables(ctx context.Context) error

	// GetLastOffset gets the last offset from the metadata table on the destination
	GetLastOffset(ctx context.Context, jobName string) (model.CdcCheckpoint, error)

	// SetLastOffset updates the last offset on the metadata table on the destination
	SetLastOffset(ctx context.Context, jobName string, lastOffset model.CdcCheckpoint) error

	// GetLastSyncBatchID gets the last batch synced to the destination from the metadata table
	GetLastSyncBatchID(ctx context.Context, jobName string) (int64, error)

	// CreateRawTable creates a raw table for the connector with a given name and a fixed schema.
	CreateRawTable(ctx context.Context, req *protos.CreateRawTableInput) (*protos.CreateRawTableOutput, error)

	// SyncFlowCleanup drops metadata tables on the destination, as a part of DROP MIRROR.
	SyncFlowCleanup(ctx context.Context, jobName string) error

	// ReplayTableSchemaDelta changes a destination table to match the schema at source
	// This could involve adding multiple columns.
	// Connectors which are non-normalizing should implement this as a nop.
	ReplayTableSchemaDeltas(ctx context.Context, env map[string]string, flowJobName string,
		tableMappings []*protos.TableMapping, schemaDeltas []*protos.TableSchemaDelta) error
}

type CDCSyncConnector interface {
	CDCSyncConnectorCore

	// SyncRecords pushes RecordItems to the destination peer and stores it in PeerDB specific tables.
	// This method should be idempotent, and should be able to be called multiple times with the same request.
	SyncRecords(ctx context.Context, req *model.SyncRecordsRequest[model.RecordItems]) (*model.SyncResponse, error)
}

type CDCSyncPgConnector interface {
	CDCSyncConnectorCore

	// SyncPg pushes PgItems to the destination peer and stores it in PeerDB specific tables.
	// This method should be idempotent, and should be able to be called multiple times with the same request.
	// It's signature, aside from type parameter, should match CDCSyncConnector.SyncRecords.
	SyncPg(ctx context.Context, req *model.SyncRecordsRequest[model.PgItems]) (*model.SyncResponse, error)
}

type CDCNormalizeConnector interface {
	Connector

	// NormalizeRecords merges records pushed earlier into the destination table.
	// This method should be idempotent, and should be able to be called multiple times with the same request.
	NormalizeRecords(ctx context.Context, req *model.NormalizeRecordsRequest) (model.NormalizeResponse, error)
}

type CreateTablesFromExistingConnector interface {
	Connector

	CreateTablesFromExisting(context.Context, *protos.CreateTablesFromExistingInput) (*protos.CreateTablesFromExistingOutput, error)
}

type QRepPullConnectorCore interface {
	Connector

	// GetQRepPartitions returns the partitions for a given table that haven't been synced yet.
	GetQRepPartitions(ctx context.Context, config *protos.QRepConfig, last *protos.QRepPartition) ([]*protos.QRepPartition, error)
}

type QRepPullConnector interface {
	QRepPullConnectorCore

	// PullQRepRecords returns the records for a given partition.
	PullQRepRecords(
		context.Context, *otel_metrics.OtelManager, *protos.QRepConfig, *protos.QRepPartition, *model.QRecordStream,
	) (int64, int64, error)
}

type QRepPullPgConnector interface {
	QRepPullConnectorCore

	// PullPgQRepRecords returns the records for a given partition.
	PullPgQRepRecords(
		context.Context, *otel_metrics.OtelManager, *protos.QRepConfig, *protos.QRepPartition, connpostgres.PgCopyWriter,
	) (int64, int64, error)
}

type QRepSyncConnectorCore interface {
	Connector

	// IsQRepPartitionSynced returns true if a partition has already been synced
	IsQRepPartitionSynced(ctx context.Context, req *protos.IsQRepPartitionSyncedInput) (bool, error)

	// SetupQRepMetadataTables sets up the metadata tables for QRep.
	SetupQRepMetadataTables(ctx context.Context, config *protos.QRepConfig) error
}

type QRepSyncConnector interface {
	QRepSyncConnectorCore

	// SyncQRepRecords syncs the records for a given partition.
	// returns the number of records synced and a slice of warnings to report to the user.
	SyncQRepRecords(ctx context.Context, config *protos.QRepConfig, partition *protos.QRepPartition,
		stream *model.QRecordStream) (int64, shared.QRepWarnings, error)
}

type QRepSyncPgConnector interface {
	QRepSyncConnectorCore

	// SyncPgQRepRecords syncs the records for a given partition.
	// returns the number of records synced and a slice of warnings to report to the user.
	SyncPgQRepRecords(ctx context.Context, config *protos.QRepConfig, partition *protos.QRepPartition,
		stream connpostgres.PgCopyReader) (int64, shared.QRepWarnings, error)
}

type QRepConsolidateConnector interface {
	Connector

	// ConsolidateQRepPartitions consolidates the partitions for a given table.
	ConsolidateQRepPartitions(ctx context.Context, config *protos.QRepConfig) error

	// CleanupQRepFlow cleans up the QRep flow for a given table.
	CleanupQRepFlow(ctx context.Context, config *protos.QRepConfig) error
}

type RawTableConnector interface {
	Connector

	RemoveTableEntriesFromRawTable(context.Context, *protos.RemoveTablesFromRawTableInput) error
}

type RenameTablesConnector interface {
	Connector

	RenameTables(context.Context, *protos.RenameTablesInput, map[string]*protos.TableSchema) (*protos.RenameTablesOutput, error)
}

type GetVersionConnector interface {
	Connector

	GetVersion(context.Context) (string, error)
}

func LoadPeerType(ctx context.Context, catalogPool shared.CatalogPool, peerName string) (protos.DBType, error) {
	row := catalogPool.QueryRow(ctx, "SELECT type FROM peers WHERE name = $1", peerName)
	var dbtype protos.DBType
	err := row.Scan(&dbtype)
	return dbtype, err
}

func LoadPeerTypes(ctx context.Context, catalogPool shared.CatalogPool, peerNames []string) (map[string]protos.DBType, error) {
	if len(peerNames) == 0 {
		return nil, nil
	}

	rows, err := catalogPool.Query(ctx, "SELECT name, type FROM peers WHERE name = ANY($1)", peerNames)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	peerTypes := make(map[string]protos.DBType)
	for rows.Next() {
		var peerName string
		var dbtype protos.DBType
		if err := rows.Scan(&peerName, &dbtype); err != nil {
			return nil, err
		}
		peerTypes[peerName] = dbtype
	}
	return peerTypes, nil
}

func LoadPeer(ctx context.Context, catalogPool shared.CatalogPool, peerName string) (*protos.Peer, error) {
	row := catalogPool.QueryRow(ctx, `
		SELECT type, options, enc_key_id
		FROM peers
		WHERE name = $1`, peerName)

	peer := &protos.Peer{Name: peerName}
	var encPeerOptions []byte
	var encKeyID string
	if err := row.Scan(&peer.Type, &encPeerOptions, &encKeyID); err != nil {
		return nil, fmt.Errorf("failed to load peer: %w", err)
	}

	peerOptions, err := internal.Decrypt(ctx, encKeyID, encPeerOptions)
	if err != nil {
		return nil, fmt.Errorf("failed to load peer: %w", err)
	}

	switch peer.Type {
	case protos.DBType_BIGQUERY:
		var config protos.BigqueryConfig
		if err := proto.Unmarshal(peerOptions, &config); err != nil {
			return nil, fmt.Errorf("failed to unmarshal BigQuery config: %w", err)
		}
		peer.Config = &protos.Peer_BigqueryConfig{BigqueryConfig: &config}
	case protos.DBType_SNOWFLAKE:
		var config protos.SnowflakeConfig
		if err := proto.Unmarshal(peerOptions, &config); err != nil {
			return nil, fmt.Errorf("failed to unmarshal Snowflake config: %w", err)
		}
		peer.Config = &protos.Peer_SnowflakeConfig{SnowflakeConfig: &config}
	case protos.DBType_POSTGRES:
		var config protos.PostgresConfig
		if err := proto.Unmarshal(peerOptions, &config); err != nil {
			return nil, fmt.Errorf("failed to unmarshal Postgres config: %w", err)
		}
		peer.Config = &protos.Peer_PostgresConfig{PostgresConfig: &config}
	case protos.DBType_S3:
		var config protos.S3Config
		if err := proto.Unmarshal(peerOptions, &config); err != nil {
			return nil, fmt.Errorf("failed to unmarshal S3 config: %w", err)
		}
		peer.Config = &protos.Peer_S3Config{S3Config: &config}
	case protos.DBType_SQLSERVER:
		var config protos.SqlServerConfig
		if err := proto.Unmarshal(peerOptions, &config); err != nil {
			return nil, fmt.Errorf("failed to unmarshal SQL Server config: %w", err)
		}
		peer.Config = &protos.Peer_SqlserverConfig{SqlserverConfig: &config}
	case protos.DBType_MONGO:
		var config protos.MongoConfig
		if err := proto.Unmarshal(peerOptions, &config); err != nil {
			return nil, fmt.Errorf("failed to unmarshal MongoDB config: %w", err)
		}
		peer.Config = &protos.Peer_MongoConfig{MongoConfig: &config}
	case protos.DBType_MYSQL:
		var config protos.MySqlConfig
		if err := proto.Unmarshal(peerOptions, &config); err != nil {
			return nil, fmt.Errorf("failed to unmarshal MySQL config: %w", err)
		}
		peer.Config = &protos.Peer_MysqlConfig{MysqlConfig: &config}
	case protos.DBType_CLICKHOUSE:
		var config protos.ClickhouseConfig
		if err := proto.Unmarshal(peerOptions, &config); err != nil {
			return nil, fmt.Errorf("failed to unmarshal ClickHouse config: %w", err)
		}
		peer.Config = &protos.Peer_ClickhouseConfig{ClickhouseConfig: &config}
	case protos.DBType_KAFKA:
		var config protos.KafkaConfig
		if err := proto.Unmarshal(peerOptions, &config); err != nil {
			return nil, fmt.Errorf("failed to unmarshal Kafka config: %w", err)
		}
		peer.Config = &protos.Peer_KafkaConfig{KafkaConfig: &config}
	case protos.DBType_PUBSUB:
		var config protos.PubSubConfig
		if err := proto.Unmarshal(peerOptions, &config); err != nil {
			return nil, fmt.Errorf("failed to unmarshal Pub/Sub config: %w", err)
		}
		peer.Config = &protos.Peer_PubsubConfig{PubsubConfig: &config}
	case protos.DBType_EVENTHUBS:
		var config protos.EventHubGroupConfig
		if err := proto.Unmarshal(peerOptions, &config); err != nil {
			return nil, fmt.Errorf("failed to unmarshal Event Hubs config: %w", err)
		}
		peer.Config = &protos.Peer_EventhubGroupConfig{EventhubGroupConfig: &config}
	case protos.DBType_ELASTICSEARCH:
		var config protos.ElasticsearchConfig
		if err := proto.Unmarshal(peerOptions, &config); err != nil {
			return nil, fmt.Errorf("failed to unmarshal Elasticsearch config: %w", err)
		}
		peer.Config = &protos.Peer_ElasticsearchConfig{ElasticsearchConfig: &config}
	default:
		return nil, fmt.Errorf("unsupported peer type: %s", peer.Type)
	}

	return peer, nil
}

func GetConnector(ctx context.Context, env map[string]string, config *protos.Peer) (Connector, error) {
	switch inner := config.Config.(type) {
	case *protos.Peer_PostgresConfig:
		return connpostgres.NewPostgresConnector(ctx, env, inner.PostgresConfig)
	case *protos.Peer_BigqueryConfig:
		return connbigquery.NewBigQueryConnector(ctx, inner.BigqueryConfig)
	case *protos.Peer_SnowflakeConfig:
		return connsnowflake.NewSnowflakeConnector(ctx, inner.SnowflakeConfig)
	case *protos.Peer_EventhubGroupConfig:
		return conneventhub.NewEventHubConnector(ctx, inner.EventhubGroupConfig)
	case *protos.Peer_S3Config:
		return conns3.NewS3Connector(ctx, inner.S3Config)
	case *protos.Peer_MongoConfig:
		return connmongo.NewMongoConnector(ctx, inner.MongoConfig)
	case *protos.Peer_MysqlConfig:
		return connmysql.NewMySqlConnector(ctx, inner.MysqlConfig)
	case *protos.Peer_ClickhouseConfig:
		return connclickhouse.NewClickHouseConnector(ctx, env, inner.ClickhouseConfig)
	case *protos.Peer_KafkaConfig:
		return connkafka.NewKafkaConnector(ctx, env, inner.KafkaConfig)
	case *protos.Peer_PubsubConfig:
		return connpubsub.NewPubSubConnector(ctx, env, inner.PubsubConfig)
	case *protos.Peer_ElasticsearchConfig:
		return connelasticsearch.NewElasticsearchConnector(ctx, inner.ElasticsearchConfig)
	default:
		return nil, errors.ErrUnsupported
	}
}

func GetAs[T Connector](ctx context.Context, env map[string]string, config *protos.Peer) (T, error) {
	var none T
	conn, err := GetConnector(ctx, env, config)
	if err != nil {
		return none, exceptions.NewPeerCreateError(err)
	}

	if tconn, ok := conn.(T); ok {
		return tconn, nil
	} else {
		conn.Close()
		return none, errors.ErrUnsupported
	}
}

func GetByNameAs[T Connector](ctx context.Context, env map[string]string, catalogPool shared.CatalogPool, name string) (T, error) {
	peer, err := LoadPeer(ctx, catalogPool, name)
	if err != nil {
		var none T
		return none, err
	}
	return GetAs[T](ctx, env, peer)
}

func CloseConnector(ctx context.Context, conn Connector) {
	if err := conn.Close(); err != nil {
		internal.LoggerFromCtx(ctx).Error("error closing connector", slog.Any("error", err))
	}
}

// create type assertions to cause compile time error if connector interface not implemented
var (
	_ CDCPullConnector = &connpostgres.PostgresConnector{}
	_ CDCPullConnector = &connmysql.MySqlConnector{}
	_ CDCPullConnector = &connmongo.MongoConnector{}

	_ CDCPullPgConnector = &connpostgres.PostgresConnector{}

	_ CDCSyncConnector = &connpostgres.PostgresConnector{}
	_ CDCSyncConnector = &connbigquery.BigQueryConnector{}
	_ CDCSyncConnector = &connsnowflake.SnowflakeConnector{}
	_ CDCSyncConnector = &conneventhub.EventHubConnector{}
	_ CDCSyncConnector = &connkafka.KafkaConnector{}
	_ CDCSyncConnector = &connpubsub.PubSubConnector{}
	_ CDCSyncConnector = &conns3.S3Connector{}
	_ CDCSyncConnector = &connclickhouse.ClickHouseConnector{}
	_ CDCSyncConnector = &connelasticsearch.ElasticsearchConnector{}

	_ CDCSyncPgConnector = &connpostgres.PostgresConnector{}

	_ CDCNormalizeConnector = &connpostgres.PostgresConnector{}
	_ CDCNormalizeConnector = &connbigquery.BigQueryConnector{}
	_ CDCNormalizeConnector = &connsnowflake.SnowflakeConnector{}
	_ CDCNormalizeConnector = &connclickhouse.ClickHouseConnector{}

	_ StatActivityConnector = &connpostgres.PostgresConnector{}
	_ StatActivityConnector = &connmysql.MySqlConnector{}

	_ GetTableSchemaConnector = &connpostgres.PostgresConnector{}
	_ GetTableSchemaConnector = &connmysql.MySqlConnector{}
	_ GetTableSchemaConnector = &connsnowflake.SnowflakeConnector{}
	_ GetTableSchemaConnector = &connclickhouse.ClickHouseConnector{}

	_ GetSchemaConnector = &connpostgres.PostgresConnector{}
	_ GetSchemaConnector = &connmysql.MySqlConnector{}
	_ GetSchemaConnector = &connmongo.MongoConnector{}

	_ NormalizedTablesConnector = &connpostgres.PostgresConnector{}
	_ NormalizedTablesConnector = &connbigquery.BigQueryConnector{}
	_ NormalizedTablesConnector = &connsnowflake.SnowflakeConnector{}
	_ NormalizedTablesConnector = &connclickhouse.ClickHouseConnector{}

	_ CreateTablesFromExistingConnector = &connbigquery.BigQueryConnector{}
	_ CreateTablesFromExistingConnector = &connsnowflake.SnowflakeConnector{}

	_ QRepPullConnector = &connpostgres.PostgresConnector{}
	_ QRepPullConnector = &connmysql.MySqlConnector{}
	_ QRepPullConnector = &connmongo.MongoConnector{}

	_ QRepPullPgConnector = &connpostgres.PostgresConnector{}

	_ QRepSyncConnector = &connpostgres.PostgresConnector{}
	_ QRepSyncConnector = &connbigquery.BigQueryConnector{}
	_ QRepSyncConnector = &connsnowflake.SnowflakeConnector{}
	_ QRepSyncConnector = &connkafka.KafkaConnector{}
	_ QRepSyncConnector = &conns3.S3Connector{}
	_ QRepSyncConnector = &connclickhouse.ClickHouseConnector{}
	_ QRepSyncConnector = &connelasticsearch.ElasticsearchConnector{}

	_ QRepSyncPgConnector = &connpostgres.PostgresConnector{}

	_ QRepConsolidateConnector = &connsnowflake.SnowflakeConnector{}
	_ QRepConsolidateConnector = &connclickhouse.ClickHouseConnector{}

	_ RenameTablesConnector = &connsnowflake.SnowflakeConnector{}
	_ RenameTablesConnector = &connbigquery.BigQueryConnector{}
	_ RenameTablesConnector = &connpostgres.PostgresConnector{}
	_ RenameTablesConnector = &connclickhouse.ClickHouseConnector{}

	_ RawTableConnector = &connclickhouse.ClickHouseConnector{}
	_ RawTableConnector = &connbigquery.BigQueryConnector{}
	_ RawTableConnector = &connsnowflake.SnowflakeConnector{}
	_ RawTableConnector = &connpostgres.PostgresConnector{}

	_ ValidationConnector = &connpostgres.PostgresConnector{}
	_ ValidationConnector = &connsnowflake.SnowflakeConnector{}
	_ ValidationConnector = &connclickhouse.ClickHouseConnector{}
	_ ValidationConnector = &connbigquery.BigQueryConnector{}
	_ ValidationConnector = &conns3.S3Connector{}
	_ ValidationConnector = &connmysql.MySqlConnector{}

	_ MirrorSourceValidationConnector = &connpostgres.PostgresConnector{}
	_ MirrorSourceValidationConnector = &connmysql.MySqlConnector{}

	_ MirrorDestinationValidationConnector = &connclickhouse.ClickHouseConnector{}

	_ GetVersionConnector = &connclickhouse.ClickHouseConnector{}
	_ GetVersionConnector = &connpostgres.PostgresConnector{}
	_ GetVersionConnector = &connmysql.MySqlConnector{}
	_ GetVersionConnector = &connmongo.MongoConnector{}
)
