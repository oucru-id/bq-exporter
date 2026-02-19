package service

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"cloud.google.com/go/bigquery"
	_ "github.com/go-sql-driver/mysql"
	"google.golang.org/api/iterator"
)

type StarRocksService struct {
	db       *sql.DB
	host     string
	port     string
	user     string
	password string
	dbname   string
}

func NewStarRocksServiceFromEnv() (*StarRocksService, error) {
	host := os.Getenv("STARROCKS_HOST")
	port := os.Getenv("STARROCKS_PORT")
	user := os.Getenv("STARROCKS_USER")
	pass := os.Getenv("STARROCKS_PASSWORD")
	dbname := os.Getenv("STARROCKS_DB")

	if host == "" || port == "" || user == "" {
		return nil, fmt.Errorf("missing StarRocks env: require STARROCKS_HOST, STARROCKS_PORT, STARROCKS_USER")
	}

	var dsn string
	if dbname != "" {
		dsn = fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?charset=utf8mb4&parseTime=true&loc=Local&interpolateParams=true", user, pass, host, port, dbname)
	} else {
		dsn = fmt.Sprintf("%s:%s@tcp(%s:%s)/?charset=utf8mb4&parseTime=true&loc=Local&interpolateParams=true", user, pass, host, port)
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, err
	}
	db.SetConnMaxLifetime(30 * time.Minute)
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("failed to connect to StarRocks: %w", err)
	}

	wh := os.Getenv("STARROCKS_WAREHOUSE")
	if strings.TrimSpace(wh) == "" {
		wh = "default_warehouse"
	}
	if _, err := db.Exec(fmt.Sprintf("SET warehouse = '%s'", wh)); err != nil {
		return nil, fmt.Errorf("failed to set session warehouse %q: %w", wh, err)
	}

	return &StarRocksService{
		db:       db,
		host:     host,
		port:     port,
		user:     user,
		password: pass,
		dbname:   dbname,
	}, nil
}

func (s *StarRocksService) Close() error {
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}

// LoadFromBigQuery executes the SQL on BigQuery, ensures the StarRocks table exists (with optional
// custom DDL or automatic schema evolution), and inserts all rows.
func (s *StarRocksService) LoadFromBigQuery(ctx context.Context, bq *BigQueryService, sqlQuery, location, table, createDDL string) (int64, error) {
	// Run query
	q := bq.client.Query(sqlQuery)
	q.Location = location
	it, err := q.Read(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to execute query on BigQuery: %w", err)
	}

	// Ensure schema is populated. RowIterator.Schema may be empty until the first page is fetched.
	var prefetch []bigquery.Value
	var havePrefetch bool
	if len(it.Schema) == 0 {
		var vals []bigquery.Value
		if e := it.Next(&vals); e == nil {
			prefetch = vals
			havePrefetch = true
		} else if e != iterator.Done {
			return 0, fmt.Errorf("failed to fetch BigQuery rows: %w", e)
		}
	}
	if len(it.Schema) == 0 {
		return 0, fmt.Errorf("empty BigQuery schema")
	}

	// Ensure table exists (create or evolve)
	if err := s.ensureTable(ctx, it.Schema, table, createDDL); err != nil {
		return 0, fmt.Errorf("failed to ensure StarRocks table: %w", err)
	}

	// Insert rows
	rowsInserted, err := s.insertRows(ctx, it, it.Schema, table, prefetch, havePrefetch)
	if err != nil {
		return 0, fmt.Errorf("failed to insert rows into StarRocks: %w", err)
	}
	return rowsInserted, nil
}

func (s *StarRocksService) ensureTable(ctx context.Context, schema bigquery.Schema, table, createDDL string) error {
	if table == "" {
		return fmt.Errorf("table name is empty")
	}

	db, tbl := s.parseDBTable(table)

	if err := s.ensureDatabase(ctx, db); err != nil {
		return err
	}

	if strings.TrimSpace(createDDL) != "" {
		slog.InfoContext(ctx, "Applying user-provided StarRocks DDL")
		if _, err := s.db.ExecContext(ctx, createDDL); err != nil {
			return fmt.Errorf("failed to execute provided DDL: %w", err)
		}
		return nil
	}

	exists, err := s.tableExists(ctx, db, tbl)
	if err != nil {
		return err
	}
	if !exists {
		// Basic duplicate-key model using first column as key
		if len(schema) == 0 {
			return fmt.Errorf("empty BigQuery schema")
		}
		var cols []string
		for _, f := range schema {
			if f.Repeated || f.Type == bigquery.RecordFieldType {
				return fmt.Errorf("unsupported complex type for column %q", f.Name)
			}
			cols = append(cols, fmt.Sprintf("`%s` %s", f.Name, mapSRType(f)))
		}
		colDDL := strings.Join(cols, ", ")
		dupKey := fmt.Sprintf("`%s`", schema[0].Name)
		fullName := s.qualify(db, tbl)
		ddl := fmt.Sprintf(`
			CREATE TABLE IF NOT EXISTS %s (
				%s
			)
			ENGINE=OLAP
			DUPLICATE KEY (%s)
			DISTRIBUTED BY HASH(%s) BUCKETS 8
			PROPERTIES (
				"replication_num" = "1"
			)`, fullName, colDDL, dupKey, dupKey)

		slog.InfoContext(ctx, "Creating StarRocks table", "table", fullName)
		if _, err := s.db.ExecContext(ctx, ddl); err != nil {
			return err
		}
		return nil
	}

	// Evolve schema: add missing columns
	return s.evolveSchema(ctx, db, tbl, schema)
}

func (s *StarRocksService) ensureDatabase(ctx context.Context, db string) error {
	if strings.TrimSpace(db) == "" {
		return fmt.Errorf("database is empty")
	}
	_, err := s.db.ExecContext(ctx, fmt.Sprintf("CREATE DATABASE IF NOT EXISTS %s", db))
	return err
}
func (s *StarRocksService) tableExists(ctx context.Context, db, tbl string) (bool, error) {
	const q = `SELECT 1 FROM information_schema.tables WHERE table_schema = ? AND table_name = ? LIMIT 1`
	var one int
	err := s.db.QueryRowContext(ctx, q, db, tbl).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (s *StarRocksService) evolveSchema(ctx context.Context, db, tbl string, schema bigquery.Schema) error {
	cur, err := s.getExistingColumns(ctx, db, tbl)
	if err != nil {
		return err
	}
	existing := make(map[string]string, len(cur))
	for _, c := range cur {
		existing[c.Name] = strings.ToUpper(c.Type)
	}

	fullName := s.qualify(db, tbl)
	for _, f := range schema {
		if f.Repeated || f.Type == bigquery.RecordFieldType {
			return fmt.Errorf("unsupported complex type for column %q", f.Name)
		}
		if _, ok := existing[f.Name]; !ok {
			colType := mapSRType(f)
			ddl := fmt.Sprintf("ALTER TABLE %s ADD COLUMN `%s` %s", fullName, f.Name, colType)
			slog.InfoContext(ctx, "Adding missing StarRocks column", "table", fullName, "column", f.Name, "type", colType)
			if _, err := s.db.ExecContext(ctx, ddl); err != nil {
				return err
			}
		}
	}
	return nil
}

type srColumn struct {
	Name string
	Type string
}

func (s *StarRocksService) getExistingColumns(ctx context.Context, db, tbl string) ([]srColumn, error) {
	const q = `
		SELECT column_name, data_type
		FROM information_schema.columns
		WHERE table_schema = ? AND table_name = ?
		ORDER BY ordinal_position
	`
	rows, err := s.db.QueryContext(ctx, q, db, tbl)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []srColumn
	for rows.Next() {
		var c srColumn
		if err := rows.Scan(&c.Name, &c.Type); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *StarRocksService) parseDBTable(table string) (string, string) {
	if strings.Contains(table, ".") {
		parts := strings.SplitN(table, ".", 2)
		db := parts[0]
		tbl := parts[1]
		if db == "" {
			db = s.dbname
		}
		return db, tbl
	}
	return s.dbname, table
}

func (s *StarRocksService) qualify(db, tbl string) string {
	return fmt.Sprintf("%s.%s", db, tbl)
}

func (s *StarRocksService) insertRows(ctx context.Context, it *bigquery.RowIterator, schema bigquery.Schema, table string, prefetch []bigquery.Value, havePrefetch bool) (int64, error) {
	cols := make([]string, 0, len(schema))
	for _, f := range schema {
		cols = append(cols, fmt.Sprintf("`%s`", f.Name))
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	batchSize := 1000
	if v := os.Getenv("STARROCKS_BATCH_SIZE"); v != "" {
		if n, e := strconv.Atoi(v); e == nil && n > 0 {
			batchSize = n
		}
	}

	var total int64
	var batch [][]bigquery.Value
	if havePrefetch && len(prefetch) > 0 {
		batch = append(batch, prefetch)
	}
	for {
		var values []bigquery.Value
		err := it.Next(&values)
		if err == iterator.Done {
			if len(batch) > 0 {
				stmtStr, args := buildBatchInsert(table, cols, schema, batch)
				if _, err := tx.ExecContext(ctx, stmtStr, args...); err != nil {
					return 0, err
				}
				total += int64(len(batch))
				batch = batch[:0]
			}
			break
		}
		if err != nil {
			return 0, err
		}
		batch = append(batch, values)
		if len(batch) >= batchSize {
			stmtStr, args := buildBatchInsert(table, cols, schema, batch)
			if _, err := tx.ExecContext(ctx, stmtStr, args...); err != nil {
				return 0, err
			}
			total += int64(len(batch))
			batch = batch[:0]
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return total, nil
}

func buildBatchInsert(table string, cols []string, schema bigquery.Schema, batch [][]bigquery.Value) (string, []any) {
	valGroups := make([]string, len(batch))
	args := make([]any, 0, len(batch)*len(schema))
	for i := range batch {
		placeholders := make([]string, len(schema))
		for j := range placeholders {
			placeholders[j] = "?"
		}
		valGroups[i] = fmt.Sprintf("(%s)", strings.Join(placeholders, ", "))
		rowArgs := convertValues(batch[i], schema)
		args = append(args, rowArgs...)
	}
	stmt := fmt.Sprintf("INSERT INTO %s (%s) VALUES %s", table, strings.Join(cols, ", "), strings.Join(valGroups, ", "))
	return stmt, args
}

// mapSRType maps BigQuery field types to StarRocks types.
func mapSRType(f *bigquery.FieldSchema) string {
	switch f.Type {
	case bigquery.StringFieldType:
		return "VARCHAR(1024)"
	case bigquery.BytesFieldType:
		return "VARBINARY(1024)"
	case bigquery.IntegerFieldType:
		return "BIGINT"
	case bigquery.FloatFieldType:
		return "DOUBLE"
	case bigquery.BooleanFieldType:
		return "BOOLEAN"
	case bigquery.TimestampFieldType, bigquery.DateTimeFieldType:
		return "DATETIME"
	case bigquery.DateFieldType:
		return "DATE"
	case bigquery.TimeFieldType:
		return "VARCHAR(64)"
	case bigquery.NumericFieldType:
		return "DECIMAL(38,9)"
	case bigquery.GeographyFieldType:
		return "VARCHAR(2048)"
	case bigquery.JSONFieldType:
		return "JSON"
	default:
		return "VARCHAR(1024)"
	}
}

// convertValues converts BigQuery row values into types acceptable by the MySQL driver.
func convertValues(values []bigquery.Value, schema bigquery.Schema) []any {
	out := make([]any, len(values))
	for i, v := range values {
		switch schema[i].Type {
		case bigquery.TimestampFieldType:
			if t, ok := v.(time.Time); ok {
				out[i] = t
			} else {
				out[i] = nil
			}
		default:
			out[i] = v
		}
	}
	return out
}
