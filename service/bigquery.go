package service

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"cloud.google.com/go/bigquery"
)

type BigQueryService struct {
	client    *bigquery.Client
	projectID string
}

func NewBigQueryService(ctx context.Context, projectID string) (*BigQueryService, error) {
	client, err := bigquery.NewClient(ctx, projectID)
	if err != nil {
		return nil, err
	}
	return &BigQueryService{
		client:    client,
		projectID: projectID,
	}, nil
}

func (s *BigQueryService) Close() error {
	return s.client.Close()
}

func (s *BigQueryService) ExportQueryToParquet(ctx context.Context, sqlQuery, outputURI, filename, location string, useTimestamp bool) (string, error) {
	// Generate timestamp for filename
	timestamp := time.Now().Format("20060102-150405")

	exportURI := outputURI

	// Determine the base filename prefix
	baseName := filename
	if baseName == "" {
		baseName = "export"
	}

	// Logic for generating the final URI:
	// 1. If it ends with "/", it's a folder. Append "{baseName}-{timestamp?-}*.parquet"
	// 2. If it doesn't have an extension (.parquet) and no wildcard (*):
	//    - Assume it's a folder path missing the slash. Append "/{baseName}-{timestamp?-}*.parquet"
	// 3. If user provided a specific pattern (e.g. ".../my-file-*.parquet"), use it as is (ignoring filename/timestamp injection to respect strict overrides)

	if strings.HasSuffix(outputURI, "/") {
		if useTimestamp {
			exportURI = fmt.Sprintf("%s%s-%s-*.parquet", outputURI, baseName, timestamp)
		} else {
			exportURI = fmt.Sprintf("%s%s-*.parquet", outputURI, baseName)
		}
	} else if !strings.HasSuffix(outputURI, ".parquet") && !strings.Contains(outputURI, "*") {
		// Treat as folder, append slash and filename pattern
		if useTimestamp {
			exportURI = fmt.Sprintf("%s/%s-%s-*.parquet", outputURI, baseName, timestamp)
		} else {
			exportURI = fmt.Sprintf("%s/%s-*.parquet", outputURI, baseName)
		}
	}

	// NOTE: If outputURI contained a pattern (case 3), we use it exactly as provided.
	// This supports "legacy" or explicit behavior where user wants full control.
	// However, if they provided 'filename', they should likely stick to folder paths in 'output'.

	slog.InfoContext(ctx, "Starting BigQuery export",
		"output_uri", outputURI,
		"filename", filename,
		"export_uri", exportURI,
		"timestamp", timestamp,
		"use_timestamp", useTimestamp,
	)

	// Construct the EXPORT DATA statement
	// We wrap the user query in parentheses to ensure syntax correctness
	// overwrite=true ensures that if we are re-running a job with the exact same timestamp (unlikely)
	// or if the user provided a fixed path, we overwrite.
	exportSQL := fmt.Sprintf(`
		EXPORT DATA OPTIONS(
			uri='%s',
			format='PARQUET',
			overwrite=true
		) AS
		(%s)
	`, exportURI, sqlQuery)

	// Run the query
	q := s.client.Query(exportSQL)
	q.Location = location

	// Execute the job
	job, err := q.Run(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to start export job: %w", err)
	}

	slog.InfoContext(ctx, "Export job submitted", "job_id", job.ID())

	// Wait for the job to complete
	status, err := job.Wait(ctx)
	if err != nil {
		return "", fmt.Errorf("job failed during execution: %w", err)
	}

	if err := status.Err(); err != nil {
		return "", fmt.Errorf("job completed with error: %w", err)
	}

	slog.InfoContext(ctx, "Export job completed successfully", "job_id", job.ID())

	return exportURI, nil
}
