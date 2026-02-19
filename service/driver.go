package service

import "context"

type ExportParams struct {
	Query         string
	Output        string
	Filename      string
	QueryLocation string
	UseTimestamp  bool
	Table         string
	CreateDDL     string
}

type ExportResult struct {
	GCSPath string
	Table   string
	Rows    int64
}

type ExportDriver interface {
	Execute(ctx context.Context, bq *BigQueryService, params ExportParams) (ExportResult, error)
}
