# Alibaba Cloud ARMS Receiver

| Status                   |                       |
| ------------------------ |-----------------------|
| Stability                | [development]: traces |
| Distributions            | []                    |
| Issues                   | [![Open issues](https://img.shields.io/github/issues-search/open-telemetry/opentelemetry-collector-contrib?query=is%3Aissue%20is%3Aopen%20label%3Areceiver%2Falibabacloudarms%20&label=open&color=orange&logo=opentelemetry)](https://github.com/open-telemetry/opentelemetry-collector-contrib/issues?q=is%3Aopen+is%3Aissue+label%3Areceiver%2Falibabacloudarms) [![Closed issues](https://img.shields.io/github/issues-search/open-telemetry/opentelemetry-collector-contrib?query=is%3Aissue%20is%3Aclosed%20label%3Areceiver%2Falibabacloudarms%20&label=closed&color=blue&logo=opentelemetry)](https://github.com/open-telemetry/opentelemetry-collector-contrib/issues?q=is%3Aclosed+is%3Aissue+label%3Areceiver%2Falibabacloudarms) |
| Code Owners              | — |

[development]: https://github.com/open-telemetry/opentelemetry-collector/blob/main/docs/component-stability.md#development

Polls **Alibaba Cloud ARMS OpenAPI** (`SearchTracesByPage` → `GetTrace` → optional `GetStack`),
converts EagleEye call-chain spans to OTLP traces.

Designed for private-cloud environments where traces are **not** available via SLS.

## Configuration

```yaml
receivers:
  alibabacloudarms:
    endpoint: arms.example.com
    region_id: test-region
    scheme: http
    api_version: "2019-08-08"

    access_key_id: ${env:ARMS_ACCESS_KEY_ID}
    access_key_secret: ${env:ARMS_ACCESS_KEY_SECRET}
    organization_id: ${env:ARMS_ORG_ID}
    resource_group_id: ${env:ARMS_RESOURCE_GROUP_ID}

    collection_interval: 30s
    query_delay: 3m
    overlap_slack: 0s

    services:
      - my-service-a
      - my-service-b

    page:
      page_size: 100
      max_page: 20

    stack:
      enabled: false
      only_on_error: true
      max_calls_per_trace: 3
```

Replace the example endpoint, region, and service names with values for your environment.

### Notes

- **`services` is required** (allowlist). Do not poll the whole region.
- **`api_version`** is the POP `Version` parameter (API contract), default `2019-08-08`.
- **`query_delay`** (default `3m`) shifts the query end time back from the current time, allowing more time for ARMS data to become available. On startup, the receiver queries the five minutes preceding this delayed end time; backlog is queried in segments of at most 30 minutes. Set `query_delay: 0s` to query up to the current time.
- Watermarks are in memory. The initial query start is retained across failed polls, but after a restart only the five minutes preceding the delayed query end are initially re-queried; a longer outage can leave a gap.
- **`overlap_slack`** (default `0s`) controls how far the next query starts before the previous successful query end. A positive value re-queries recent data; larger values increase API calls and may help find late spans. Segmented backlog catch-up does not apply overlap.
- Each polling round fetches a TraceID once across services after a successful fetch and export. Subsequent rounds fetch it again if the search returns it, and suppress previously exported spans using an in-memory cache of up to 50,000 OTLP TraceID and SpanID pairs. The cache uses FIFO eviction and is lost on restart; evicted spans may be exported again. Updates to already-exported spans are not re-exported.
- Late spans can only be collected if the TraceID is returned by a later search; neither the query delay nor an overlap window guarantees that all late spans will be found.
- If a time window exceeds `page.max_page`, the receiver probes for another page and splits the time range to continue collecting. A failed `GetTrace` response does not advance the service watermark; an empty response is logged and skipped to avoid blocking collection. If that trace is not returned by a later search, its spans may be missed.
- `GetTrace` returns coarse call-chain spans (sometimes a partial slice). Method/SQL detail is in `GetStack` (off by default).
- `arms.rpc_type` retains the source `RpcType`. Database and cache types are treated as client operations and populated with `db.system.name` when the product is known. Most ARMS database type names do not explicitly define a direction, so the client mapping is an inference; TDDL and the generic database type do not identify a DBMS product. An existing `db.system.name` tag takes precedence. Destination attributes are not inferred from `ServiceIp`, which identifies the application host. Types whose direction cannot be inferred use an unspecified span kind; Kafka send/receive operation names provide direction hints. See [Alibaba Cloud's GetTrace metadata](https://github.com/aliyun/aliyun-openapi-meta/blob/8095c38507e858189b96974f7dce2914abd108a6/canonical/arms/2019-08-08/GetTrace.json) for documented values.
- ARMS `grpc` and `thrift` types are treated as server-side operations, paired with their respective client types. Scheduler types are treated as server entry points; this is a receiver mapping choice, not a general OpenTelemetry requirement for local jobs.
- When `RpcType` does not determine a span kind, a `span.kind` tag takes precedence over `messaging.operation.type` and Kafka operation-name hints. Messaging `create` maps to producer, `process` to consumer, and `receive` or `settle` to client; `send` alone is ambiguous. Invalid or conflicting direction tags remain unspecified. Parent-child position alone is not used to infer direction because traces may be partial.
- Database products without a registered OpenTelemetry identifier use a custom lowercase name. ARMS type `58`, documented as `infuxdb`, is interpreted as InfluxDB. Database topology recognition depends on the downstream backend and on destination attributes supplied by ARMS.
- HTTP spans (`RpcType` `0`/`25`, or spans that already carry HTTP status attributes / a path-like `OperationName`) are enriched for downstream call-chain search: missing `url.path` / `http.route` / `http.target` are filled from `OperationName` when it looks like a URL path, and missing `http.request.method` / `http.method` default to `GET` (or the method prefix when `OperationName` is like `POST /path`). Existing ARMS tags are left unchanged.

## Test data

The [`testdata`](testdata/) directory contains a sample configuration and anonymized API response fixtures:
[`search_traces.json`](testdata/search_traces.json), [`get_trace.json`](testdata/get_trace.json),
and [`get_stack.json`](testdata/get_stack.json).

## Debug logging

Set `service.telemetry.logs.level: debug` to log the business parameters and full HTTP response
body for each `SearchTracesByPage`, `GetTrace`, and `GetStack` request. Access keys, signatures,
and authentication headers are not logged as request parameters. Responses can contain sensitive
trace attributes, SQL, and other application data; enable debug logging only temporarily and
protect the resulting logs. `GetStack` is only called when stack enrichment is enabled.

Each service poll also logs a debug summary with the attempted query window, result, and watermark
before and after the poll. A skipped poll has `query_attempted: false`; its start and end describe
the proposed window rather than an API request. `watermark_before` is the initialized start on the
first poll. A successful poll does not guarantee delivery to the final backend: it may contain no
new spans, skip empty trace responses, or pass spans to a downstream component that queues them.
