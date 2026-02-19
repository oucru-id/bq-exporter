package service

import (
	"context"
)

type GCSDriver struct{}

func NewGCSDriver() *GCSDriver {
	return &GCSDriver{}
}

func (d *GCSDriver) Execute(ctx context.Context, bq *BigQueryService, params ExportParams) (ExportResult, error) {
	path, err := bq.ExportQueryToParquet(ctx, params.Query, params.Output, params.Filename, params.QueryLocation, params.UseTimestamp)
	if err != nil {
		return ExportResult{}, err
	}
	return ExportResult{GCSPath: path}, nil
}
