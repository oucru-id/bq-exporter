package main

import (
	"bq-exporter/api"
	"bq-exporter/service"
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"cloud.google.com/go/bigquery"
	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
	"golang.org/x/oauth2/google"
)

func main() {
	// Initialize structured logging (JSON format for Cloud Run)
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	// Load environment variables
	if err := godotenv.Load(); err != nil {
		slog.Info("No .env file found, using system environment variables")
	}

	ctx := context.Background()

	projectID := os.Getenv("GCP_PROJECT_ID")
	if projectID == "" {
		slog.Info("GCP_PROJECT_ID not set, attempting to detect from credentials...")
		creds, err := google.FindDefaultCredentials(ctx, bigquery.Scope)
		if err != nil {
			slog.Error("Failed to find default credentials", "error", err)
			os.Exit(1)
		}
		if creds.ProjectID == "" {
			slog.Error("GCP_PROJECT_ID is not set and could not be detected from credentials")
			os.Exit(1)
		}
		projectID = creds.ProjectID
		slog.Info("Detected Project ID", "project_id", projectID)
	}

	// Initialize BigQuery Service
	bqService, err := service.NewBigQueryService(ctx, projectID)
	if err != nil {
		slog.Error("Failed to initialize BigQuery service", "error", err)
		os.Exit(1)
	}
	defer bqService.Close()

	// Initialize driver
	var driver service.ExportDriver
	if os.Getenv("EXPORT_DRIVER") == "STARROCKS" {
		srService, err := service.NewStarRocksServiceFromEnv()
		if err != nil {
			slog.Error("Failed to initialize StarRocks service", "error", err)
			os.Exit(1)
		}
		defer srService.Close()
		driver = service.NewStarRocksDriver(srService)
	} else {
		driver = service.NewGCSDriver()
	}

	// Job mode: execute once and exit (for Cloud Run Jobs)
	if os.Getenv("RUN_MODE") == "job" {
		req := api.ExportRequest{}
		req.Query = os.Getenv("JOB_QUERY")
		req.QueryLocation = os.Getenv("JOB_QUERY_LOCATION")
		req.Table = os.Getenv("JOB_TABLE")
		req.Database = os.Getenv("JOB_DATABASE")
		req.Output = os.Getenv("JOB_OUTPUT")
		req.Filename = os.Getenv("JOB_FILENAME")
		req.CreateDDL = os.Getenv("JOB_CREATE_DDL")
		ut := strings.ToLower(os.Getenv("JOB_USE_TIMESTAMP"))
		req.UseTimestamp = ut == "true" || ut == "1" || ut == "yes"
		if req.Query == "" || req.QueryLocation == "" {
			slog.Error("JOB_QUERY or JOB_QUERY_LOCATION is empty")
			os.Exit(1)
		}
		params := service.ExportParams{
			Query:         req.Query,
			Output:        req.Output,
			Filename:      req.Filename,
			QueryLocation: req.QueryLocation,
			UseTimestamp:  req.UseTimestamp,
			Table:         req.Table,
			Database:      req.Database,
			CreateDDL:     req.CreateDDL,
		}
		res, err := driver.Execute(ctx, bqService, params)
		if err != nil {
			slog.Error("Job execution failed", "error", err)
			os.Exit(1)
		}
		slog.Info("Job execution completed", "gcs_path", res.GCSPath, "table", res.Table, "rows", res.Rows)
		return
	}

	// Initialize Gin
	// Release mode is better for production performance
	if os.Getenv("GIN_MODE") == "" {
		gin.SetMode(gin.ReleaseMode)
	}
	r := gin.New() // Use New() to skip default logger/recovery middleware for custom ones
	r.Use(gin.Recovery())

	apiKey := os.Getenv("API_KEY")
	if apiKey != "" {
		r.Use(func(c *gin.Context) {
			if c.Request.URL.Path == "/health" {
				c.Next()
				return
			}
			if c.GetHeader("X-API-Key") != apiKey {
				c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
				return
			}
			c.Next()
		})
	}

	// Custom logger middleware for Gin that uses slog
	r.Use(func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path
		raw := c.Request.URL.RawQuery

		c.Next()

		latency := time.Since(start)
		status := c.Writer.Status()

		msg := "Request processed"
		attrs := []any{
			slog.String("method", c.Request.Method),
			slog.String("path", path),
			slog.Int("status", status),
			slog.Duration("latency", latency),
			slog.String("client_ip", c.ClientIP()),
		}
		if raw != "" {
			attrs = append(attrs, slog.String("query", raw))
		}

		// Cloud Scheduler specific headers
		if jobName := c.GetHeader("X-CloudScheduler-JobName"); jobName != "" {
			attrs = append(attrs, slog.String("scheduler_job", jobName))
		}
		if scheduleTime := c.GetHeader("X-CloudScheduler-ScheduleTime"); scheduleTime != "" {
			attrs = append(attrs, slog.String("scheduler_time", scheduleTime))
		}

		if status >= 500 {
			slog.Error(msg, attrs...)
		} else {
			slog.Info(msg, attrs...)
		}
	})

	// CORS configuration
	config := cors.DefaultConfig()
	config.AllowAllOrigins = true
	r.Use(cors.New(config))

	// Health Check Endpoint (Vital for Cloud Run)
	r.GET("/health", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	// Routes
	r.POST("/api/export", api.ExportHandler(bqService, driver))

	// Server setup with Graceful Shutdown
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	srv := &http.Server{
		Addr:    ":" + port,
		Handler: r,
	}

	// Start server in goroutine
	go func() {
		slog.Info("Server starting", "port", port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("Failed to start server", "error", err)
			os.Exit(1)
		}
	}()

	// Wait for interrupt signal to gracefully shutdown the server
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	slog.Info("Shutting down server...")

	// The context is used to inform the server it has 5 seconds to finish
	// the request it is currently handling
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		slog.Error("Server forced to shutdown", "error", err)
	}

	slog.Info("Server exiting")
}
