package api

import (
	"bq-exporter/service"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
)

type ExportRequest struct {
	Query         string `json:"query" binding:"required"`
	Output        string `json:"output" binding:"required"`
	Filename      string `json:"filename"`
	QueryLocation string `json:"query_location" binding:"required"`
	UseTimestamp  bool   `json:"use_timestamp"`
}

type ExportResponse struct {
	Message string `json:"message"`
	GCSPath string `json:"gcs_path"`
}

func ExportHandler(bqService *service.BigQueryService) gin.HandlerFunc {
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

		// Execute the export
		// Note: In a real production app, you should validate the query to prevent
		// malicious SQL or unintended costs.
		// Also, long running jobs might timeout HTTP requests.
		// For very large exports, consider running asynchronously.
		gcsPath, err := bqService.ExportQueryToParquet(c.Request.Context(), req.Query, req.Output, req.Filename, req.QueryLocation, req.UseTimestamp)
		if err != nil {
			slog.ErrorContext(c.Request.Context(), "Export failed", "error", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to export data: " + err.Error()})
			return
		}

		c.JSON(http.StatusOK, ExportResponse{
			Message: "Export completed successfully",
			GCSPath: gcsPath,
		})
	}
}
