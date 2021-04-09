package postgres

import (
	"context"
	dbsql "database/sql"
	"fmt"

	sq "github.com/Masterminds/squirrel"
	"github.com/golang/protobuf/proto"
	"github.com/jmoiron/sqlx"

	"github.com/authzed/spicedb/internal/datastore"
	pb "github.com/authzed/spicedb/pkg/REDACTEDapi/api"
)

const (
	errUnableToWriteConfig  = "unable to write namespace config: %w"
	errUnableToReadConfig   = "unable to read namespace config: %w"
	errUnableToDeleteConfig = "unable to delete namespace config: %w"
)

var (
	writeNamespace = psql.Insert(tableNamespace).Columns(
		colNamespace,
		colConfig,
		colCreatedTxn,
	)

	readNamespace = psql.Select(colConfig, colCreatedTxn).
			From(tableNamespace).
			Where(sq.Eq{colDeletedTxn: liveDeletedTxnID})

	deleteNamespace = psql.Update(tableNamespace).Where(sq.Eq{colDeletedTxn: liveDeletedTxnID})

	deleteNamespaceTuples = psql.Update(tableTuple).Where(sq.Eq{colDeletedTxn: liveDeletedTxnID})
)

func (pgd *pgDatastore) WriteNamespace(ctx context.Context, newConfig *pb.NamespaceDefinition) (uint64, error) {
	serialized, err := proto.Marshal(newConfig)
	if err != nil {
		return 0, fmt.Errorf(errUnableToWriteConfig, err)
	}

	tx, err := pgd.db.Beginx()
	if err != nil {
		return 0, fmt.Errorf(errUnableToWriteConfig, err)
	}

	defer tx.Rollback()

	newTxnID, err := createNewTransaction(tx)
	if err != nil {
		return 0, fmt.Errorf(errUnableToWriteConfig, err)
	}

	sql, args, err := writeNamespace.Values(newConfig.Name, serialized, newTxnID).ToSql()
	if err != nil {
		return 0, fmt.Errorf(errUnableToWriteConfig, err)
	}

	_, err = tx.Exec(sql, args...)
	if err != nil {
		return 0, fmt.Errorf(errUnableToWriteConfig, err)
	}

	err = tx.Commit()
	if err != nil {
		return 0, fmt.Errorf(errUnableToWriteConfig, err)
	}

	return newTxnID, nil
}

func (pgd *pgDatastore) ReadNamespace(ctx context.Context, nsName string) (*pb.NamespaceDefinition, uint64, error) {
	tx, err := pgd.db.Beginx()
	if err != nil {
		return nil, 0, fmt.Errorf(errUnableToReadConfig, err)
	}
	defer tx.Rollback()

	loaded, version, err := loadNamespace(nsName, tx)
	switch err {
	case datastore.ErrNamespaceNotFound:
		return nil, 0, err
	case nil:
		return loaded, version, nil
	default:
		return nil, 0, fmt.Errorf(errUnableToReadConfig, err)
	}
}

func (pgd *pgDatastore) DeleteNamespace(ctx context.Context, nsName string) (uint64, error) {
	tx, err := pgd.db.Beginx()
	if err != nil {
		return 0, fmt.Errorf(errUnableToDeleteConfig, err)
	}

	defer tx.Rollback()

	_, version, err := loadNamespace(nsName, tx)
	switch err {
	case datastore.ErrNamespaceNotFound:
		return 0, err
	case nil:
		break
	default:
		return 0, fmt.Errorf(errUnableToDeleteConfig, err)
	}

	newTxnID, err := createNewTransaction(tx)
	if err != nil {
		return 0, fmt.Errorf(errUnableToDeleteConfig, err)
	}

	delSQL, delArgs, err := deleteNamespace.
		Set(colDeletedTxn, newTxnID).
		Where(sq.Eq{colNamespace: nsName, colCreatedTxn: version}).
		ToSql()
	if err != nil {
		return 0, fmt.Errorf(errUnableToDeleteConfig, err)
	}

	_, err = tx.Exec(delSQL, delArgs...)
	if err != nil {
		return 0, fmt.Errorf(errUnableToDeleteConfig, err)
	}

	deleteTupleSQL, deleteTupleArgs, err := deleteNamespaceTuples.
		Set(colDeletedTxn, newTxnID).
		Where(sq.Eq{colNamespace: nsName}).
		ToSql()
	if err != nil {
		return 0, fmt.Errorf(errUnableToDeleteConfig, err)
	}

	_, err = tx.Exec(deleteTupleSQL, deleteTupleArgs...)
	if err != nil {
		return 0, fmt.Errorf(errUnableToDeleteConfig, err)
	}

	err = tx.Commit()
	if err != nil {
		return 0, fmt.Errorf(errUnableToDeleteConfig, err)
	}

	return version, nil
}

func loadNamespace(namespace string, tx *sqlx.Tx) (*pb.NamespaceDefinition, uint64, error) {
	sql, args, err := readNamespace.Where(sq.Eq{colNamespace: namespace}).ToSql()
	if err != nil {
		return nil, 0, err
	}

	var config []byte
	var version uint64
	err = tx.QueryRowx(sql, args...).Scan(&config, &version)
	if err != nil {
		if err == dbsql.ErrNoRows {
			err = datastore.ErrNamespaceNotFound
		}
		return nil, 0, err
	}

	loaded := &pb.NamespaceDefinition{}
	err = proto.Unmarshal(config, loaded)
	if err != nil {
		return nil, 0, err
	}

	return loaded, version, nil
}
