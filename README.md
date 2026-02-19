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
  - `database` optional; overrides the default `STARROCKS_DB` for this request. If set, the service ensures the database exists (creates if missing).
  - `create_ddl` optional; if provided, will be executed to create the table (e.g., full CREATE TABLE ... statement). If not provided, the service infers schema from the BigQuery result and:
    - Creates the table if missing using a default DUPLICATE KEY model (first column) and HASH distribution (8 buckets)
    - Performs automatic schema evolution by adding missing columns when the query returns new fields
  - Response includes `starrocks_table` and `rows_loaded`.

### Curl Examples with Docker Compose Defaults

When running via `docker compose up`, the service listens on `localhost:8080`, requires the header `X-API-Key: apikey`, and defaults to `EXPORT_DRIVER=GCS_PARQUET`.

- StarRocks (switch driver to STARROCKS first: set `EXPORT_DRIVER=STARROCKS` in compose or env). You can omit `STARROCKS_DB` and specify `database` in the request:

```bash
curl -X POST http://localhost:8080/api/export \
  -H "Content-Type: application/json" \
  -d '{
    "query": "SELECT puskesmas_name, file_name FROM syntethic_data.data_ingestion_report LIMIT 10",
    "query_location": "asia-southeast2",
    "table": "data_ingestion_report",
    "database": "syntethic_data"
  }'
```

Explicit DDL (optional):

```bash
curl -X POST http://localhost:8080/api/export \
  -H "Content-Type: application/json" \
  -d '{
    "query": "SELECT id, name FROM dataset.table",
    "query_location": "US",
    "table": "users",
    "database": "analytics",
    "create_ddl": "CREATE TABLE IF NOT EXISTS analytics.users (id BIGINT, name VARCHAR(256)) ENGINE=OLAP DUPLICATE KEY(id) DISTRIBUTED BY HASH(id) BUCKETS 8 PROPERTIES (\"replication_num\" = \"1\")"
  }'
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

### Cloud Run Service (HTTP)

Use when each run finishes under 60 minutes.

```bash
gcloud run deploy bq-exporter \
  --image gcr.io/YOUR_PROJECT/bq-exporter \
  --platform managed \
  --region us-central1 \
  --allow-unauthenticated
```

Trigger with Cloud Scheduler HTTP target to POST /api/export. Prefer OIDC auth.

### Cloud Run Jobs (Batch)

Use for larger loads that may exceed the 60‑minute HTTP limit.

1. Build and deploy the same image as a Job:

```bash
gcloud run jobs create bq-exporter-job \
  --image gcr.io/YOUR_PROJECT/bq-exporter \
  --region us-central1 \
  --set-env-vars RUN_MODE=job
```

2. Provide the request payload via JOB_PAYLOAD env var (JSON for the same request body):

```bash
gcloud run jobs update bq-exporter-job \
  --region us-central1 \
  --set-env-vars JOB_PAYLOAD='{"query":"SELECT id FROM dataset.table","query_location":"US","table":"users","database":"analytics"}'
```

3. Run on demand or schedule via Cloud Scheduler using the Jobs API (e.g., Cloud Workflows or Cloud Functions as an orchestrator).

Job mode logs the result and exits; no HTTP server is started.

### Cloud Scheduler → Cloud Run Jobs API

Cloud Scheduler can call the Cloud Run Admin API to run the job on schedule.

- URL:
  - `POST https://run.googleapis.com/v2/projects/PROJECT_ID/locations/REGION/jobs/JOB_NAME:run`
- Auth:
  - Use OAuth Token with scope `https://www.googleapis.com/auth/cloud-platform`
  - Grant the Scheduler’s service account `roles/run.jobRunner` (or `roles/run.admin`)
- Body:
  - Pre-set envs: `{}` if `JOB_PAYLOAD` is already configured on the job
  - Per-run override (human-friendly envs):
    ```json
    {
      "overrides": {
        "containerOverrides": [
          { "name": "JOB_QUERY", "value": "SELECT id FROM dataset.table" },
          { "name": "JOB_QUERY_LOCATION", "value": "US" },
          { "name": "JOB_TABLE", "value": "users" },
          { "name": "JOB_DATABASE", "value": "analytics" },
          { "name": "JOB_OUTPUT", "value": "gs://my-bucket/exports/" },
          { "name": "JOB_FILENAME", "value": "daily" },
          { "name": "JOB_USE_TIMESTAMP", "value": "true" },
          { "name": "JOB_CREATE_DDL", "value": "" }
        ]
      }
    }
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

## StarRocks SQL (Docker)

- Ensure the stack is running:

```bash
docker compose up --detach --wait --wait-timeout 120
```

- Open the MySQL-compatible CLI inside the FE container:

```bash
docker compose exec starrocks-fe mysql -uroot -h starrocks-fe -P9030
```

- Example queries:

```sql
SHOW STORAGE VOLUMES;
SHOW COMPUTE NODES;
CREATE DATABASE IF NOT EXISTS analytics;
SHOW DATABASES;
```

- Notes:
  - The default storage volume is auto-created and set via FE config in `docker-compose.yml` (shared-data mode pointing at MinIO).
  - Databases and tables are not auto-created. Create them via SQL, or let the exporter create tables on first load when using the `STARROCKS` driver.

### Create Table and Insert Data

- Create a simple OLAP table (Duplicate Key model) and insert rows:

```sql
CREATE TABLE IF NOT EXISTS analytics.users (
  id BIGINT,
  name VARCHAR(256),
  created_at DATETIME
) ENGINE=OLAP
DUPLICATE KEY(id)
DISTRIBUTED BY HASH(id) BUCKETS 8
PROPERTIES ("replication_num" = "1");

INSERT INTO analytics.users (id, name, created_at) VALUES
  (1, 'Alice', '2026-02-19 09:00:00'),
  (2, 'Bob',   '2026-02-19 09:05:00');

SELECT * FROM analytics.users ORDER BY id;
SELECT COUNT(*) FROM analytics.users;
```

- Tip: You can run the above directly via:

```bash
docker compose exec -T starrocks-fe mysql -uroot -h starrocks-fe -P9030 -e "
CREATE TABLE IF NOT EXISTS analytics.users (
  id BIGINT,
  name VARCHAR(256),
  created_at DATETIME
) ENGINE=OLAP
DUPLICATE KEY(id)
DISTRIBUTED BY HASH(id) BUCKETS 8
PROPERTIES (\"replication_num\" = \"1\");
INSERT INTO analytics.users (id, name, created_at) VALUES
  (1, 'Alice', '2026-02-19 09:00:00'),
  (2, 'Bob',   '2026-02-19 09:05:00');
SELECT COUNT(*) FROM analytics.users;
SELECT * FROM analytics.users ORDER BY id;"
```
