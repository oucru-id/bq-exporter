# BigQuery Exporter

A Cloud Native Go microservice that exports BigQuery query results to Google Cloud Storage (GCS) in Parquet format. Designed for high performance, stateless execution on Cloud Run, and easy integration with Cloud Scheduler.

## Features

- **Efficient Export**: Uses BigQuery's native `EXPORT DATA` statement (server-side export).
- **Parquet Support**: Automatically exports data in Parquet format.
- **Cloud Native**:
  - Stateless architecture suitable for Cloud Run.
  - JSON structured logging (`slog`) for Cloud Logging.
  - Graceful shutdown handling.
  - Health check endpoint (`/health`).
- **Flexible Output**: Supports exporting to specific folders or wildcard paths in GCS.

## Prerequisites

- Go 1.25+
- Google Cloud Project with BigQuery and GCS enabled.
- Service Account with permissions:
  - `BigQuery Job User`
  - `BigQuery Data Viewer` (on source dataset)
  - `Storage Object Admin` (on destination bucket)

## Configuration

The application is configured via environment variables:

| Variable | Description | Default |
|----------|-------------|---------|
| `PORT` | HTTP Port to listen on | `8080` |
| `GCP_PROJECT_ID` | Google Cloud Project ID (Optional if using Service Account JSON) | Detected from creds |
| `GOOGLE_APPLICATION_CREDENTIALS` | Path to Service Account JSON key | - |
| `GIN_MODE` | Gin framework mode (`release` or `debug`) | `release` (if unset) |

## API Usage

### Endpoint: `POST /api/export`

**Request Body:**

```json
{
  "query": "SELECT * FROM my_dataset.my_table WHERE created_at > TIMESTAMP_SUB(CURRENT_TIMESTAMP(), INTERVAL 1 HOUR)",
  "output": "gs://my-bucket/exports/hourly/",
  "filename": "hourly-data",
  "query_location": "US"
}
```

- `query`: The SQL query to execute.
- `output`: The GCS destination directory.
- `filename`: (Optional) The prefix for the generated files.
  - If provided (e.g., `hourly-data`), result: `.../hourly-data-YYYYMMDD-HHmmss-*.parquet`
  - If omitted, defaults to `export`.
- `query_location`: BigQuery job location (e.g., `US`, `EU`, `asia-southeast1`). Required.

**Response:**

```json
{
  "message": "Export completed successfully",
  "gcs_path": "gs://my-bucket/exports/hourly/hourly-data-20231027-103000-*.parquet"
}
```

## Docker Compose

```bash
# Build and run with ADC mounted
docker compose up --build

# Stop
docker compose down
```

Place your service account JSON at the project root as `sa.json`. The compose file mounts it into the container and sets `GOOGLE_APPLICATION_CREDENTIALS=/app/creds/sa.json`. Optional environment variables can be provided via `.env`.

## Authentication

Set an API key via environment variable and include it in requests:

```
API_KEY=your-api-key
```

Send the header on requests:

```
X-API-Key: your-api-key
```

The `/health` endpoint is public; `/api/export` requires the header when `API_KEY` is set. For Cloud Scheduler, add the same header in the job configuration.

## Deployment

### Docker Build

```bash
docker build -t bq-exporter .
```

### Run Locally

```bash
# Create .env file
echo "GOOGLE_APPLICATION_CREDENTIALS=./key.json" > .env

# Run
go run main.go
```

### Cloud Run Deployment

1. **Build and Push Image**:
   ```bash
   gcloud builds submit --tag gcr.io/YOUR_PROJECT/bq-exporter
   ```

2. **Deploy**:
   ```bash
   gcloud run deploy bq-exporter \
     --image gcr.io/YOUR_PROJECT/bq-exporter \
     --platform managed \
     --region us-central1 \
     --allow-unauthenticated \
     --service-account YOUR-SERVICE-ACCOUNT@YOUR_PROJECT.iam.gserviceaccount.com
   ```

   *Note: Remove `--allow-unauthenticated` if you want to secure it with IAM.*

## Cloud Scheduler Integration

To trigger this service on a schedule (e.g., every hour):

1. Create a Cloud Scheduler job.
2. **Target type**: HTTP
3. **URL**: `https://your-cloud-run-url.run.app/api/export`
4. **HTTP Method**: POST
5. **Body**:
   ```json
   {
     "query": "SELECT * FROM dataset.table WHERE date = CURRENT_DATE()",
     "output": "gs://my-bucket/daily-export/"
   }
   ```
6. **Auth Header**: Add OIDC Token (select your service account).

The application automatically logs `X-CloudScheduler-JobName` and `X-CloudScheduler-ScheduleTime` headers to help you trace execution in Cloud Logging.
