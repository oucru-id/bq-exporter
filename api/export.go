package api

import (
	"bq-exporter/service"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
)

type ExportRequest struct {
	Query         string `json:"query" binding:"required"`
	Output        string `json:"output"`
	Filename      string `json:"filename"`
	QueryLocation string `json:"query_location" binding:"required"`
	UseTimestamp  bool   `json:"use_timestamp"`
	Table         string `json:"table"`
	CreateDDL     string `json:"create_ddl"`
}

type ExportResponse struct {
	Message string `json:"message"`
	GCSPath string `json:"gcs_path,omitempty"`
	Table   string `json:"starrocks_table,omitempty"`
	Rows    int64  `json:"rows_loaded,omitempty"`
}

func ExportHandler(bqService *service.BigQueryService, driver service.ExportDriver) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req ExportRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			slog.WarnContext(c.Request.Context(), "Invalid request body", "error", err)
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		slog.InfoContext(c.Request.Context(), "Received export request",
			"query", req.Query,
			"output", req.Output,
			"filename", req.Filename,
			"location", req.QueryLocation,
			"use_timestamp", req.UseTimestamp,
		)

		params := service.ExportParams{
			Query:         req.Query,
			Output:        req.Output,
			Filename:      req.Filename,
			QueryLocation: req.QueryLocation,
			UseTimestamp:  req.UseTimestamp,
			Table:         req.Table,
			CreateDDL:     req.CreateDDL,
		}
		res, err := driver.Execute(c.Request.Context(), bqService, params)
		if err != nil {
			slog.ErrorContext(c.Request.Context(), "Export failed", "error", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to process export: " + err.Error()})
			return
		}
		c.JSON(http.StatusOK, ExportResponse{
			Message: "OK",
			GCSPath: res.GCSPath,
			Table:   res.Table,
			Rows:    res.Rows,
		})
	}
}
