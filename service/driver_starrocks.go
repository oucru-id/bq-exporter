package service

import (
	"context"
	"fmt"
	"strings"
)

type StarRocksDriver struct {
	sr *StarRocksService
}

func NewStarRocksDriver(sr *StarRocksService) *StarRocksDriver {
	return &StarRocksDriver{sr: sr}
}

func (d *StarRocksDriver) Execute(ctx context.Context, bq *BigQueryService, params ExportParams) (ExportResult, error) {
	table := params.Table
	if table == "" {
		table = "export"
	}
	if !strings.Contains(table, ".") {
		if strings.TrimSpace(params.Database) != "" {
			table = params.Database + "." + table
		} else if strings.TrimSpace(d.sr.dbname) != "" {
			table = d.sr.dbname + "." + table
		} else {
			return ExportResult{}, fmt.Errorf("database not specified; provide 'database' or use table in 'db.table' format")
		}
	}
	rows, err := d.sr.LoadFromBigQuery(ctx, bq, params.Query, params.QueryLocation, table, params.CreateDDL)
	if err != nil {
		return ExportResult{}, err
	}
	return ExportResult{Table: table, Rows: rows}, nil
}
