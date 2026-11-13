# Playground API

[`openapi.yml`](../openapi.yml) is the canonical API specification. It defines request and response schemas, parameters, status codes, and examples. The production API base URL is `https://fiddle.clickhouse.com/api`.

AI agents can do a great job of explaining the specification, generating a client, or producing request examples from `openapi.yml`. Treat the OpenAPI document as the source of truth when this page and the specification differ.

## Endpoints

- `GET /tags` lists the ClickHouse version tags available for query runs.
- `GET /build-types` lists supported build types. `release` uses a Docker Hub image; non-release types require local builds to be enabled.
- `POST /images/prepare` starts, or checks, preparation of a non-release image for a version and build type.
- `GET /images/status` returns the preparation state for a version and build type: `building`, `ready`, or `failed`.
- `POST /runs` executes a SQL query and returns its output and a query-run ID.
- `GET /runs/{id}` retrieves the details and output of a previously completed query run.

## Output size

The server reads at most `max_output_length` bytes of output. A longer output is cut there and the response has `output_truncated: true`; the query itself still succeeds.

The output is stored gzipped. When the stored run would exceed the DynamoDB item size limit (400 KB), the run is stored without its output and both `POST /runs` and `GET /runs/{id}` report `output_dropped: true`. The `POST` response still carries the output; `GET` returns the query with an empty output, and the client has to run the query again to see the result.

## Flows

For a release query, call `GET /tags`, select a version, then send the SQL and version to `POST /runs`. Use the returned `query_run_id` with `GET /runs/{id}` when you need to retrieve the stored run.

For a debug or sanitizer query, call `GET /tags` and `GET /build-types`, then call `POST /images/prepare`. Poll `GET /images/status` until the image is `ready`; only then submit `POST /runs` with the same version and build type. A `409` response from `/runs` means the image is not ready yet.
