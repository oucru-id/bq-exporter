package service

import (
	"context"
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
	rows, err := d.sr.LoadFromBigQuery(ctx, bq, params.Query, params.QueryLocation, table, params.CreateDDL)
	if err != nil {
		return ExportResult{}, err
	}
	return ExportResult{Table: table, Rows: rows}, nil
}
