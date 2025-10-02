package yugabytedb

import (
	"context"
	"database/sql"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib" // sql driver
	"github.com/k3s-io/kine/pkg/drivers"
	"github.com/k3s-io/kine/pkg/drivers/generic"
	"github.com/k3s-io/kine/pkg/logstructured"
	"github.com/k3s-io/kine/pkg/logstructured/sqllog"
	"github.com/k3s-io/kine/pkg/server"
	"github.com/k3s-io/kine/pkg/tls"
	"github.com/k3s-io/kine/pkg/util"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sirupsen/logrus"
)

const (
	defaultDSN = "postgres://yugabyte:yugabyte@localhost:5433/yugabyte"
)

var (
	schema = []string{
		`CREATE SEQUENCE IF NOT EXISTS kine_seq CACHE 100;`,
		`CREATE TABLE IF NOT EXISTS kine
 			(
 				id INTEGER NOT NULL DEFAULT nextval('kine_seq'),
				name VARCHAR(630),
				created INTEGER,
				deleted INTEGER,
 				create_revision INTEGER,
 				prev_revision INTEGER,
 				lease INTEGER,
 				value bytea,
 				old_value bytea,
				PRIMARY KEY(id asc)
 			);`,
		`CREATE INDEX IF NOT EXISTS kine_name_index ON kine (name);`,
		`CREATE INDEX IF NOT EXISTS kine_name_id_index ON kine (name,id);`,
		`CREATE INDEX IF NOT EXISTS kine_id_deleted_index ON kine (id,deleted);`,
		`CREATE INDEX IF NOT EXISTS kine_prev_revision_index ON kine (prev_revision);`,
		`CREATE UNIQUE INDEX IF NOT EXISTS kine_name_prev_revision_uindex ON kine (name asc, prev_revision asc) include(id);`,
	}
	createDB = "CREATE DATABASE "
)

func New(ctx context.Context, cfg *drivers.Config) (bool, server.Backend, error) {
	parsedDSN, err := prepareDSN(cfg.DataSourceName, cfg.BackendTLSConfig)
	if err != nil {
		return false, nil, err
	}

	if err := createDBIfNotExist(parsedDSN); err != nil {

		return false, nil, err
	}

	dialect, err := generic.Open(ctx, "pgx", parsedDSN, cfg.ConnectionPoolConfig, "$", true, cfg.MetricsRegisterer)
	if err != nil {
		return false, nil, err
	}
	dialect.GetSizeSQL = `SELECT pg_total_relation_size('kine')`
	dialect.CompactSQL = `
		DELETE FROM kine AS kv
		USING	(
			SELECT kp.prev_revision AS id
			FROM kine AS kp
			WHERE
				kp.name != 'compact_rev_key' AND
				kp.prev_revision != 0 AND
				kp.id <= $1
			UNION
			SELECT kd.id AS id
			FROM kine AS kd
			WHERE
				kd.deleted != 0 AND
				kd.id <= $2
		) AS ks
		WHERE kv.id = ks.id`
	dialect.TranslateErr = func(err error) error {
		if err, ok := err.(*pgconn.PgError); ok && err.Code == pgerrcode.UniqueViolation {
			return server.ErrKeyExists
		}
		return err
	}
	dialect.ErrCode = func(err error) string {
		if err == nil {
			return ""
		}
		if err, ok := err.(*pgconn.PgError); ok {
			return err.Code
		}
		return err.Error()
	}

	if err := setup(dialect.DB); err != nil {
		return false, nil, err
	}

	dialect.Migrate(context.Background())
	return true, logstructured.New(sqllog.New(dialect)), nil
}

func setup(db *sql.DB) error {
	logrus.Infof("Configuring database table schema and indexes for YugabyteDB, this may take a moment...")

	for _, stmt := range schema {
		logrus.Tracef("SETUP EXEC : %v", util.Stripped(stmt))
		_, err := db.Exec(stmt)
		if err != nil {
			return err
		}
	}

	logrus.Infof("Database tables and indexes are up to date")
	return nil
}

func createDBIfNotExist(dataSourceName string) error {
	u, err := url.Parse(dataSourceName)
	if err != nil {
		return err
	}

	dbName := strings.SplitN(u.Path, "/", 2)[1]

	db, err := sql.Open("pgx", dataSourceName)
	if err != nil {
		return err
	}
	defer db.Close()

	err = db.Ping()
	// check if database already exists
	if _, ok := err.(*pgconn.PgError); !ok {
		err := enableBatchedNestedLoop(db, dbName)
		return err
	}

	if err := err.(*pgconn.PgError); err.Code != pgerrcode.DuplicateDatabase {
		if err.Code != pgerrcode.InvalidCatalogName {
			return err
		}
		// database doesn't exit, will try to create it
		u.Path = "/postgres"
		db, err := sql.Open("pgx", u.String())
		if err != nil {
			return err
		}
		defer db.Close()
		stmt := createDB + dbName + ";"
		logrus.Tracef("SETUP EXEC : %v", util.Stripped(stmt))
		_, err = db.Exec(stmt)
		if err != nil {
			return err
		}

		err = enableBatchedNestedLoop(db, dbName)
		if err != nil {
			return err
		}
	}

	return nil
}

func enableBatchedNestedLoop(db *sql.DB, dbName string) error {
	var err error

	stmt := "ALTER DATABASE " + dbName + " set yb_bnl_batch_size=1024;"

	logrus.Infof("Enabling the batched nested loop for database '" + dbName + "'")

	_, err = db.Exec(stmt)

	return err
}

func q(sql string) string {
	regex := regexp.MustCompile(`\?`)
	pref := "$"
	n := 0
	return regex.ReplaceAllStringFunc(sql, func(string) string {
		n++
		return pref + strconv.Itoa(n)
	})
}

func prepareDSN(dataSourceName string, tlsInfo tls.Config) (string, error) {
	if len(dataSourceName) == 0 {
		dataSourceName = defaultDSN
	} else {
		dataSourceName = "postgres://" + dataSourceName
	}
	u, err := url.Parse(dataSourceName)
	if err != nil {
		return "", err
	}
	if len(u.Path) == 0 || u.Path == "/" {
		u.Path = "/kubernetes"
	}

	queryMap, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return "", err
	}
	// set up tls dsn
	params := url.Values{}
	sslmode := ""
	if _, ok := queryMap["sslcert"]; tlsInfo.CertFile != "" && !ok {
		params.Add("sslcert", tlsInfo.CertFile)
		sslmode = "verify-full"
	}
	if _, ok := queryMap["sslkey"]; tlsInfo.KeyFile != "" && !ok {
		params.Add("sslkey", tlsInfo.KeyFile)
		sslmode = "verify-full"
	}
	if _, ok := queryMap["sslrootcert"]; tlsInfo.CAFile != "" && !ok {
		params.Add("sslrootcert", tlsInfo.CAFile)
		sslmode = "verify-full"
	}
	if _, ok := queryMap["sslmode"]; !ok && sslmode != "" {
		params.Add("sslmode", sslmode)
	}
	for k, v := range queryMap {
		params.Add(k, v[0])
	}
	u.RawQuery = params.Encode()
	return u.String(), nil
}

func init() {
	drivers.Register("yugabyte", New)
	drivers.Register("yugabytedb", New)
}
