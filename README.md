# BigQuery Exporter

A Cloud Native Go microservice that exports BigQuery query results to destinations via a pluggable driver:
- GCS Parquet using BigQuery server-side EXPORT DATA
- StarRocks table load with automatic table creation and batched inserts

## Features

- **Driver Architecture**: Select destination via `EXPORT_DRIVER` (`GCS_PARQUET` or `STARROCKS`).
- **Efficient Export (GCS)**: Uses BigQuery's native `EXPORT DATA` statement (server-side export).
- **StarRocks Load**: Creates table if missing and performs batched inserts for high throughput.
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
| `GCP_PROJECT_ID` | Google Cloud Project ID | Detected from creds |
| `GOOGLE_APPLICATION_CREDENTIALS` | Path to Service Account JSON key | - |
| `GIN_MODE` | Gin framework mode (`release` or `debug`) | `release` (if unset) |
| `API_KEY` | Optional API key for request auth | - |
| `EXPORT_DRIVER` | Destination driver: `GCS_PARQUET` or `STARROCKS` | `GCS_PARQUET` |
| `STARROCKS_HOST` | StarRocks FE host | - |
| `STARROCKS_PORT` | StarRocks MySQL port | `9030` |
| `STARROCKS_USER` | StarRocks user | - |
| `STARROCKS_PASSWORD` | StarRocks password | - |
| `STARROCKS_DB` | Target database | - |
| `STARROCKS_HTTP_PORT` | StarRocks FE HTTP port | `8030` |
| `STARROCKS_BATCH_SIZE` | Insert batch size | `1000` |

## API Usage

### Endpoint: `POST /api/export`

Single endpoint supports both drivers. The body shape is unified; fields are validated per driver.

Request Body:
```json
{
  "query": "SELECT * FROM dataset.table",
  "query_location": "US",
  "table": "optional-for-starrocks",
  "output": "required-for-gcs",
  "filename": "optional-for-gcs",
  "use_timestamp": false
}
```

- Common:
  - `query` and `query_location` are required.
- GCS Parquet:
  - `output` required; `filename` and `use_timestamp` optional.
  - Response includes `gcs_path`.
- StarRocks:
  - `table` optional; defaults to `export`.
  - `create_ddl` optional; if provided, will be executed to create the table (e.g., full CREATE TABLE ... statement). If not provided, the service infers schema from the BigQuery result and:
    - Creates the table if missing using a default DUPLICATE KEY model (first column) and HASH distribution (8 buckets)
    - Performs automatic schema evolution by adding missing columns when the query returns new fields
  - Response includes `starrocks_table` and `rows_loaded`.

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
