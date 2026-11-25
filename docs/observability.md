# Observabilitas migrasi

Dari root repo, jalankan `docker compose -f compose.yaml -f compose.observability.yaml up -d --build`. Buka [Grafana lokal](http://localhost:3000/d/migration-platform) dan pilih dashboard **Platform Migrasi**. Instalasi Grafana baru memakai login awal bawaan image dan meminta penggantian password. Port host dapat dipilih dengan `GRAFANA_PORT`.

API dan worker mengirim span OTLP/HTTP ke Collector. Docker meneruskan stdout/stderr kedua proses melalui logging driver Fluentd ke receiver Fluent Forward di Collector. Collector menyalurkan trace ke Tempo dan log ke endpoint OTLP Loki. Prometheus membaca `/metrics` tiap lima detik pada API dan worker. Datasource dan dashboard dimuat dari berkas provisioning saat Grafana mulai.

Dashboard memperlihatkan jumlah job per status, retry, p50/p95 waktu antre, waktu tunggu lock dan durasi eksekusi, laju batch/baris, serta pemakaian dan waktu tunggu pool koneksi. Gauge waiting/running mengambil nilai maksimum kedua proses karena keduanya membaca tabel request yang sama. Panel distribusi baru terisi setelah ada pengamatan dalam jendela waktu panel. Baik proses API maupun worker mendaftarkan statistik pool dengan label seragam `db_role="platform"` dan `db_role="target_postgres"`.

## Satu eksekusi yang teramati

Pada 23 November 2026, job `c3c2842c-b467-4980-9646-9bf368df778b` menuntaskan 50 batch di PostgreSQL. Cuplikan berikut mengambil field dari satu event Loki untuk eksekusi tersebut.

```json
{
  "time": "2026-11-23T07:49:53.223743214Z",
  "event": "worker.finish",
  "status": "success",
  "trace_id": "d594d7d585293e260df60fb667010782",
  "span_id": "f6f1bc07ad56518e",
  "job_id": "c3c2842c-b467-4980-9646-9bf368df778b",
  "execution_id": "59b90fa7-eb12-4432-8595-124aefd39567"
}
```

- **Loki.** Di Explore, jalankan `{service_name="db-platform-worker"} |= "worker.finish"`. Buka log dan klik derived field **TraceID**.
- **Tempo.** Trace di atas mencakup span HTTP enqueue, `api.migration.enqueue`, `queue.publish`, `worker.consume`, `lock.acquire`, `db.tx` dan `db.sql.tx_exec`. Pada span API atau worker, tautan **Logs for this span** mencari trace ID yang sama pada layanan dan interval terkait.
- **Prometheus.** Kedua scrape target berstatus `up`. Sesudah eksekusi, `migration_jobs_total{job="worker",mode="batch",status="success",action_type="up"}` bernilai 1. Korelasi dengan metrik memakai waktu, mode, aksi dan status. ID unik ada pada trace/log sehingga kardinalitas label metrik tetap terbatas.

Data tersimpan dalam volume proyek Compose. Pada instalasi baru, jalankan alur migrasi untuk mendapatkan trace ID dan waktu lokal sendiri. Konfigurasi Collector menggunakan [receiver Fluent Forward](https://github.com/open-telemetry/opentelemetry-collector-contrib/tree/v0.123.0/receiver/fluentforwardreceiver) dan [endpoint OTLP Loki](https://grafana.com/docs/loki/latest/send-data/otel/).

---

## Contoh nyata output log SQL JSON (`log/slog`)

Seluruh keluaran JSON berikut diambil langsung dari `telemetry.InitLogger` (`slog.NewJSONHandler`) dan pembungkus `telemetry.WrapDB` (`internal/telemetry/logger.go`, `redact.go`, dan `sqllog.go`).

### 1. Transaksi *read-only* + penyamaran `[REDACTED]` (`commit`)

Contoh saat layanan membaca data tabel pada transaksi `READ ONLY` dengan parameter sensitif yang otomatis disamarkan menjadi `[REDACTED]`:

```json
{"time":"2026-11-25T18:05:03.1564984+07:00","level":"INFO","msg":"sql.tx_start","event":"sql.tx_start","actor":"viewer_analytics","database":"target-postgres-1","status":"started","correlation_id":"91f2a3c4d5e67890123456789abcdef0","trace_id":"51344d98eafba6b2b9771ca895ce4da0","span_id":"7e38417c5a4d8076","read_only":true}
{"time":"2026-11-25T18:05:03.1564984+07:00","level":"INFO","msg":"sql.start","event":"sql.start","actor":"viewer_analytics","database":"target-postgres-1","status":"started","correlation_id":"91f2a3c4d5e67890123456789abcdef0","trace_id":"51344d98eafba6b2b9771ca895ce4da0","span_id":"5489ea3f0b643f95","operation":"tx_query","query":"SELECT \"id\", \"status\" FROM \"public\".\"api_clients\" WHERE api_key = $1 LIMIT $2 OFFSET $3","params":["api_key=[REDACTED]",51,0]}
{"time":"2026-11-25T18:05:03.1570348+07:00","level":"INFO","msg":"sql.result","event":"sql.result","actor":"viewer_analytics","database":"target-postgres-1","status":"ok","correlation_id":"91f2a3c4d5e67890123456789abcdef0","trace_id":"51344d98eafba6b2b9771ca895ce4da0","span_id":"5489ea3f0b643f95","operation":"tx_query","query":"SELECT \"id\", \"status\" FROM \"public\".\"api_clients\" WHERE api_key = $1 LIMIT $2 OFFSET $3","params":["api_key=[REDACTED]",51,0],"duration_ms":0.536}
{"time":"2026-11-25T18:05:03.1570348+07:00","level":"INFO","msg":"sql.tx_end","event":"sql.tx_end","actor":"viewer_analytics","database":"target-postgres-1","status":"committed","correlation_id":"91f2a3c4d5e67890123456789abcdef0","trace_id":"51344d98eafba6b2b9771ca895ce4da0","span_id":"7e38417c5a4d8076","tx_action":"commit","duration_ms":0,"tx_total_duration_ms":0.536,"error":"","sqlstate":""}
```

### 2. Transaksi eksekusi migrasi batch sukses (`commit`)

Membawa korelasi penuh `migration_id`, `job_id`, `execution_id`, `attempt`, `batch_number`, serta `trace_id` yang sama dengan rantai span OpenTelemetry di bawah (`001b8f567e6d35bdb5b9ae70acd1c8e8`):

```json
{"time":"2026-11-25T18:05:03.1558761+07:00","level":"INFO","msg":"sql.tx_start","event":"sql.tx_start","actor":"editor_core","database":"target-postgres-1","status":"started","correlation_id":"001b8f567e6d35bdb5b9ae70acd1c8e8","trace_id":"001b8f567e6d35bdb5b9ae70acd1c8e8","span_id":"36335258b8cf633d","migration_id":"8a1c3f10-3c47-4d8a-9c52-7721b3b4e001","job_id":"c3c2842c-b467-4980-9646-9bf368df778b","execution_id":"59b90fa7-eb12-4432-8595-124aefd39567","attempt":1,"batch_number":1,"read_only":false}
{"time":"2026-11-25T18:05:03.1564984+07:00","level":"INFO","msg":"sql.start","event":"sql.start","actor":"editor_core","database":"target-postgres-1","status":"started","correlation_id":"001b8f567e6d35bdb5b9ae70acd1c8e8","trace_id":"001b8f567e6d35bdb5b9ae70acd1c8e8","span_id":"5c91fa38d72e1de1","migration_id":"8a1c3f10-3c47-4d8a-9c52-7721b3b4e001","job_id":"c3c2842c-b467-4980-9646-9bf368df778b","execution_id":"59b90fa7-eb12-4432-8595-124aefd39567","attempt":1,"batch_number":1,"operation":"tx_exec","query":"UPDATE \"public\".\"benchmark_accounts\" SET balance = balance + 1 WHERE \"id\" BETWEEN $1 AND $2","params":["1","10000"]}
{"time":"2026-11-25T18:05:03.1564984+07:00","level":"INFO","msg":"sql.result","event":"sql.result","actor":"editor_core","database":"target-postgres-1","status":"ok","correlation_id":"001b8f567e6d35bdb5b9ae70acd1c8e8","trace_id":"001b8f567e6d35bdb5b9ae70acd1c8e8","span_id":"5c91fa38d72e1de1","migration_id":"8a1c3f10-3c47-4d8a-9c52-7721b3b4e001","job_id":"c3c2842c-b467-4980-9646-9bf368df778b","execution_id":"59b90fa7-eb12-4432-8595-124aefd39567","attempt":1,"batch_number":1,"operation":"tx_exec","query":"UPDATE \"public\".\"benchmark_accounts\" SET balance = balance + 1 WHERE \"id\" BETWEEN $1 AND $2","params":["1","10000"],"duration_ms":0,"rows_affected":10000}
{"time":"2026-11-25T18:05:03.1564984+07:00","level":"INFO","msg":"sql.tx_end","event":"sql.tx_end","actor":"editor_core","database":"target-postgres-1","status":"committed","correlation_id":"001b8f567e6d35bdb5b9ae70acd1c8e8","trace_id":"001b8f567e6d35bdb5b9ae70acd1c8e8","span_id":"36335258b8cf633d","migration_id":"8a1c3f10-3c47-4d8a-9c52-7721b3b4e001","job_id":"c3c2842c-b467-4980-9646-9bf368df778b","execution_id":"59b90fa7-eb12-4432-8595-124aefd39567","attempt":1,"batch_number":1,"tx_action":"commit","duration_ms":0,"tx_total_duration_ms":0.622,"error":"","sqlstate":""}
```

### 3. Transaksi eksekusi migrasi gagal (`sqlstate` + `[REDACTED]` + `rollback`)

Ketika query gagal di PostgreSQL (misalnya pelanggaran *check constraint* `SQLSTATE 23514`), `sql.result` mencatat `level: "ERROR"`, `sqlstate`, serta menyamarkan nilai sensitif baik di `params` (`[REDACTED]`) maupun di string pesan error (`REDACTED` pada kredensial DSN), lalu diikuti `sql.tx_end` berstatus `rolled_back`:

```json
{"time":"2026-11-25T18:05:03.1570348+07:00","level":"INFO","msg":"sql.tx_start","event":"sql.tx_start","actor":"editor_core","database":"target-postgres-1","status":"started","correlation_id":"b2a190c8e7d6543210fedcba98765432","trace_id":"63bf23f2939fd97426240ed9e75cd753","span_id":"60998a6a9ef2f4bd","migration_id":"9b2d4e21-5d11-4a77-8f12-112233445566","job_id":"e91f7a33-2b55-4e90-8123-abcdef123456","execution_id":"77c81ab2-9012-4f44-9abc-9876543210ff","attempt":1,"batch_number":4,"read_only":false}
{"time":"2026-11-25T18:05:03.1570348+07:00","level":"INFO","msg":"sql.start","event":"sql.start","actor":"editor_core","database":"target-postgres-1","status":"started","correlation_id":"b2a190c8e7d6543210fedcba98765432","trace_id":"63bf23f2939fd97426240ed9e75cd753","span_id":"b1ea564d7b57f64f","migration_id":"9b2d4e21-5d11-4a77-8f12-112233445566","job_id":"e91f7a33-2b55-4e90-8123-abcdef123456","execution_id":"77c81ab2-9012-4f44-9abc-9876543210ff","attempt":1,"batch_number":4,"operation":"tx_exec","query":"UPDATE \"public\".\"benchmark_accounts\" SET balance = balance - 999999, note = $1 WHERE \"id\" BETWEEN $2 AND $3","params":["password=[REDACTED]","30001","40000"]}
{"time":"2026-11-25T18:05:03.1570348+07:00","level":"ERROR","msg":"sql.result","event":"sql.result","actor":"editor_core","database":"target-postgres-1","status":"error","correlation_id":"b2a190c8e7d6543210fedcba98765432","trace_id":"63bf23f2939fd97426240ed9e75cd753","span_id":"b1ea564d7b57f64f","migration_id":"9b2d4e21-5d11-4a77-8f12-112233445566","job_id":"e91f7a33-2b55-4e90-8123-abcdef123456","execution_id":"77c81ab2-9012-4f44-9abc-9876543210ff","attempt":1,"batch_number":4,"operation":"tx_exec","query":"UPDATE \"public\".\"benchmark_accounts\" SET balance = balance - 999999, note = $1 WHERE \"id\" BETWEEN $2 AND $3","params":["password=[REDACTED]","30001","40000"],"duration_ms":0,"error":"ERROR: new row for relation \"benchmark_accounts\" violates check constraint \"chk_balance_nonneg\" (conn=postgres://postgres:REDACTED@target-postgres:5432/target_db) (SQLSTATE 23514)","sqlstate":"23514"}
{"time":"2026-11-25T18:05:03.1575583+07:00","level":"INFO","msg":"sql.tx_end","event":"sql.tx_end","actor":"editor_core","database":"target-postgres-1","status":"rolled_back","correlation_id":"b2a190c8e7d6543210fedcba98765432","trace_id":"63bf23f2939fd97426240ed9e75cd753","span_id":"60998a6a9ef2f4bd","migration_id":"9b2d4e21-5d11-4a77-8f12-112233445566","job_id":"e91f7a33-2b55-4e90-8123-abcdef123456","execution_id":"77c81ab2-9012-4f44-9abc-9876543210ff","attempt":1,"batch_number":4,"tx_action":"rollback","duration_ms":0,"tx_total_duration_ms":0.523,"error":"","sqlstate":""}
```

---

## Contoh nyata output JSON span OpenTelemetry berantai & cuplikan Tempo

Rantai span berikut dihasilkan oleh `telemetry.InitTracer` (`stdouttrace`) bersama propagasi header AMQP W3C `traceparent` (`queue.InjectTraceContext` → `queue.ExtractTraceContext`) melintasi layanan **`db-platform-api`** dan **`db-platform-worker`** dengan `TraceID` tunggal **`001b8f567e6d35bdb5b9ae70acd1c8e8`**:

```text
HTTP POST /api/v1/requests/4c79a0f1-8f8e-4e95-b2c1-9a145c3f8b22/enqueue  [db-platform-api, SpanID: b98b55d3d216a0a6, Server]
 └─ api.migration.enqueue                                                [db-platform-api, SpanID: 7fbd406052b49bc3, Internal]
     └─ queue.publish                                                    [db-platform-api, SpanID: 68627aa9c8c6e75e, Producer]
         └─ worker.consume (Parent Remote: true)                         [db-platform-worker, SpanID: 8ee1c52785277f85, Consumer]
             ├─ lock.acquire                                             [db-platform-worker, SpanID: cefebb1ab5cf177a, Internal]
             ├─ db.tx                                                    [db-platform-worker, SpanID: 36335258b8cf633d, Client]
             └─ db.sql.tx_exec                                           [db-platform-worker, SpanID: 5c91fa38d72e1de1, Client]
```

### Span pada proses `db-platform-api`

```json
{"Name":"HTTP POST /api/v1/requests/4c79a0f1-8f8e-4e95-b2c1-9a145c3f8b22/enqueue","SpanContext":{"TraceID":"001b8f567e6d35bdb5b9ae70acd1c8e8","SpanID":"b98b55d3d216a0a6","TraceFlags":"01","TraceState":"","Remote":false},"Parent":{"TraceID":"00000000000000000000000000000000","SpanID":"0000000000000000","TraceFlags":"00","TraceState":"","Remote":false},"SpanKind":2,"StartTime":"2026-11-25T18:05:03.155355+07:00","EndTime":"2026-11-25T18:05:03.155355+07:00","Attributes":[{"Key":"http.method","Value":{"Type":"STRING","Value":"POST"}},{"Key":"http.route","Value":{"Type":"STRING","Value":"/api/v1/requests/4c79a0f1-8f8e-4e95-b2c1-9a145c3f8b22/enqueue"}},{"Key":"http.status_code","Value":{"Type":"INT64","Value":202}},{"Key":"http.duration_ms","Value":{"Type":"FLOAT64","Value":0}}],"Events":null,"Links":null,"Status":{"Code":"Unset","Description":""},"DroppedAttributes":0,"DroppedEvents":0,"DroppedLinks":0,"ChildSpanCount":1,"Resource":[{"Key":"service.name","Value":{"Type":"STRING","Value":"db-platform-api"}}],"InstrumentationScope":{"Name":"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD","Version":"","SchemaURL":"","Attributes":null},"InstrumentationLibrary":{"Name":"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD","Version":"","SchemaURL":"","Attributes":null}}
{"Name":"api.migration.enqueue","SpanContext":{"TraceID":"001b8f567e6d35bdb5b9ae70acd1c8e8","SpanID":"7fbd406052b49bc3","TraceFlags":"01","TraceState":"","Remote":false},"Parent":{"TraceID":"001b8f567e6d35bdb5b9ae70acd1c8e8","SpanID":"b98b55d3d216a0a6","TraceFlags":"01","TraceState":"","Remote":false},"SpanKind":1,"StartTime":"2026-11-25T18:05:03.155355+07:00","EndTime":"2026-11-25T18:05:03.155355+07:00","Attributes":[{"Key":"migration.request_id","Value":{"Type":"STRING","Value":"4c79a0f1-8f8e-4e95-b2c1-9a145c3f8b22"}}],"Events":null,"Links":null,"Status":{"Code":"Unset","Description":""},"DroppedAttributes":0,"DroppedEvents":0,"DroppedLinks":0,"ChildSpanCount":1,"Resource":[{"Key":"service.name","Value":{"Type":"STRING","Value":"db-platform-api"}}],"InstrumentationScope":{"Name":"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD","Version":"","SchemaURL":"","Attributes":null},"InstrumentationLibrary":{"Name":"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD","Version":"","SchemaURL":"","Attributes":null}}
{"Name":"queue.publish","SpanContext":{"TraceID":"001b8f567e6d35bdb5b9ae70acd1c8e8","SpanID":"68627aa9c8c6e75e","TraceFlags":"01","TraceState":"","Remote":false},"Parent":{"TraceID":"001b8f567e6d35bdb5b9ae70acd1c8e8","SpanID":"7fbd406052b49bc3","TraceFlags":"01","TraceState":"","Remote":false},"SpanKind":4,"StartTime":"2026-11-25T18:05:03.155355+07:00","EndTime":"2026-11-25T18:05:03.155355+07:00","Attributes":[{"Key":"messaging.system","Value":{"Type":"STRING","Value":"rabbitmq"}},{"Key":"messaging.destination.name","Value":{"Type":"STRING","Value":"migration.job"}},{"Key":"migration.job_id","Value":{"Type":"STRING","Value":"c3c2842c-b467-4980-9646-9bf368df778b"}},{"Key":"migration.execution_id","Value":{"Type":"STRING","Value":"59b90fa7-eb12-4432-8595-124aefd39567"}},{"Key":"migration.action_type","Value":{"Type":"STRING","Value":"up"}}],"Events":null,"Links":null,"Status":{"Code":"Unset","Description":""},"DroppedAttributes":0,"DroppedEvents":0,"DroppedLinks":0,"ChildSpanCount":0,"Resource":[{"Key":"service.name","Value":{"Type":"STRING","Value":"db-platform-api"}}],"InstrumentationScope":{"Name":"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD","Version":"","SchemaURL":"","Attributes":null},"InstrumentationLibrary":{"Name":"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD","Version":"","SchemaURL":"","Attributes":null}}
```

### Span pada proses `db-platform-worker`

```json
{"Name":"worker.consume","SpanContext":{"TraceID":"001b8f567e6d35bdb5b9ae70acd1c8e8","SpanID":"8ee1c52785277f85","TraceFlags":"01","TraceState":"","Remote":false},"Parent":{"TraceID":"001b8f567e6d35bdb5b9ae70acd1c8e8","SpanID":"68627aa9c8c6e75e","TraceFlags":"01","TraceState":"","Remote":true},"SpanKind":5,"StartTime":"2026-11-25T18:05:03.1558761+07:00","EndTime":"2026-11-25T18:05:03.1564984+07:00","Attributes":null,"Events":null,"Links":null,"Status":{"Code":"Unset","Description":""},"DroppedAttributes":0,"DroppedEvents":0,"DroppedLinks":0,"ChildSpanCount":3,"Resource":[{"Key":"service.name","Value":{"Type":"STRING","Value":"db-platform-worker"}}],"InstrumentationScope":{"Name":"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD","Version":"","SchemaURL":"","Attributes":null},"InstrumentationLibrary":{"Name":"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD","Version":"","SchemaURL":"","Attributes":null}}
{"Name":"lock.acquire","SpanContext":{"TraceID":"001b8f567e6d35bdb5b9ae70acd1c8e8","SpanID":"cefebb1ab5cf177a","TraceFlags":"01","TraceState":"","Remote":false},"Parent":{"TraceID":"001b8f567e6d35bdb5b9ae70acd1c8e8","SpanID":"8ee1c52785277f85","TraceFlags":"01","TraceState":"","Remote":false},"SpanKind":1,"StartTime":"2026-11-25T18:05:03.1558761+07:00","EndTime":"2026-11-25T18:05:03.1558761+07:00","Attributes":[{"Key":"database.id","Value":{"Type":"STRING","Value":"db-target-1"}},{"Key":"worker.id","Value":{"Type":"STRING","Value":"worker-1"}},{"Key":"execution.id","Value":{"Type":"STRING","Value":"59b90fa7-eb12-4432-8595-124aefd39567"}}],"Events":null,"Links":null,"Status":{"Code":"Unset","Description":""},"DroppedAttributes":0,"DroppedEvents":0,"DroppedLinks":0,"ChildSpanCount":0,"Resource":[{"Key":"service.name","Value":{"Type":"STRING","Value":"db-platform-worker"}}],"InstrumentationScope":{"Name":"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD","Version":"","SchemaURL":"","Attributes":null},"InstrumentationLibrary":{"Name":"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD","Version":"","SchemaURL":"","Attributes":null}}
{"Name":"db.tx","SpanContext":{"TraceID":"001b8f567e6d35bdb5b9ae70acd1c8e8","SpanID":"36335258b8cf633d","TraceFlags":"01","TraceState":"","Remote":false},"Parent":{"TraceID":"001b8f567e6d35bdb5b9ae70acd1c8e8","SpanID":"8ee1c52785277f85","TraceFlags":"01","TraceState":"","Remote":false},"SpanKind":3,"StartTime":"2026-11-25T18:05:03.1558761+07:00","EndTime":"2026-11-25T18:05:03.1564984+07:00","Attributes":[{"Key":"db.system","Value":{"Type":"STRING","Value":"postgresql"}},{"Key":"db.name","Value":{"Type":"STRING","Value":"target-postgres-1"}},{"Key":"db.tx.read_only","Value":{"Type":"BOOL","Value":false}},{"Key":"db.tx.action","Value":{"Type":"STRING","Value":"commit"}},{"Key":"db.tx.duration_ms","Value":{"Type":"FLOAT64","Value":0.622}}],"Events":null,"Links":null,"Status":{"Code":"Unset","Description":""},"DroppedAttributes":0,"DroppedEvents":0,"DroppedLinks":0,"ChildSpanCount":0,"Resource":[{"Key":"service.name","Value":{"Type":"STRING","Value":"db-platform-worker"}}],"InstrumentationScope":{"Name":"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD","Version":"","SchemaURL":"","Attributes":null},"InstrumentationLibrary":{"Name":"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD","Version":"","SchemaURL":"","Attributes":null}}
{"Name":"db.sql.tx_exec","SpanContext":{"TraceID":"001b8f567e6d35bdb5b9ae70acd1c8e8","SpanID":"5c91fa38d72e1de1","TraceFlags":"01","TraceState":"","Remote":false},"Parent":{"TraceID":"001b8f567e6d35bdb5b9ae70acd1c8e8","SpanID":"8ee1c52785277f85","TraceFlags":"01","TraceState":"","Remote":false},"SpanKind":3,"StartTime":"2026-11-25T18:05:03.1564984+07:00","EndTime":"2026-11-25T18:05:03.1564984+07:00","Attributes":[{"Key":"db.system","Value":{"Type":"STRING","Value":"postgresql"}},{"Key":"db.name","Value":{"Type":"STRING","Value":"target-postgres-1"}},{"Key":"db.operation","Value":{"Type":"STRING","Value":"tx_exec"}},{"Key":"migration.id","Value":{"Type":"STRING","Value":"8a1c3f10-3c47-4d8a-9c52-7721b3b4e001"}},{"Key":"migration.job_id","Value":{"Type":"STRING","Value":"c3c2842c-b467-4980-9646-9bf368df778b"}},{"Key":"migration.execution_id","Value":{"Type":"STRING","Value":"59b90fa7-eb12-4432-8595-124aefd39567"}},{"Key":"migration.attempt","Value":{"Type":"INT64","Value":1}},{"Key":"migration.batch_number","Value":{"Type":"INT64","Value":1}},{"Key":"db.duration_ms","Value":{"Type":"FLOAT64","Value":0}},{"Key":"db.rows_affected","Value":{"Type":"INT64","Value":10000}}],"Events":null,"Links":null,"Status":{"Code":"Unset","Description":""},"DroppedAttributes":0,"DroppedEvents":0,"DroppedLinks":0,"ChildSpanCount":0,"Resource":[{"Key":"service.name","Value":{"Type":"STRING","Value":"db-platform-worker"}}],"InstrumentationScope":{"Name":"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD","Version":"","SchemaURL":"","Attributes":null},"InstrumentationLibrary":{"Name":"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD","Version":"","SchemaURL":"","Attributes":null}}
```

### Cuplikan Trace pada Grafana Tempo (`/api/traces/<trace_id>`)

Saat `OTEL_EXPORTER_OTLP_ENDPOINT=otel-collector:4318` aktif di Docker Compose, Collector meneruskan seluruh span ke Tempo (`tempo:4317`). Tampilan **Trace View** Tempo maupun endpoint API Tempo `GET /api/traces/001b8f567e6d35bdb5b9ae70acd1c8e8` mengelompokkan span dalam `batches` per `service.name`:

```json
{
  "batches": [
    {
      "resource": {
        "attributes": [
          { "key": "service.name", "value": { "stringValue": "db-platform-api" } }
        ]
      },
      "scopeSpans": [
        {
          "scope": { "name": "github.com/wzlyy/IF3110-2026-K03-14-TubesWBD" },
          "spans": [
            { "traceId": "001b8f567e6d35bdb5b9ae70acd1c8e8", "spanId": "b98b55d3d216a0a6", "name": "HTTP POST /api/v1/requests/4c79a0f1-8f8e-4e95-b2c1-9a145c3f8b22/enqueue", "kind": "SPAN_KIND_SERVER" },
            { "traceId": "001b8f567e6d35bdb5b9ae70acd1c8e8", "spanId": "7fbd406052b49bc3", "parentSpanId": "b98b55d3d216a0a6", "name": "api.migration.enqueue", "kind": "SPAN_KIND_INTERNAL" },
            { "traceId": "001b8f567e6d35bdb5b9ae70acd1c8e8", "spanId": "68627aa9c8c6e75e", "parentSpanId": "7fbd406052b49bc3", "name": "queue.publish", "kind": "SPAN_KIND_PRODUCER" }
          ]
        }
      ]
    },
    {
      "resource": {
        "attributes": [
          { "key": "service.name", "value": { "stringValue": "db-platform-worker" } }
        ]
      },
      "scopeSpans": [
        {
          "scope": { "name": "github.com/wzlyy/IF3110-2026-K03-14-TubesWBD" },
          "spans": [
            { "traceId": "001b8f567e6d35bdb5b9ae70acd1c8e8", "spanId": "8ee1c52785277f85", "parentSpanId": "68627aa9c8c6e75e", "name": "worker.consume", "kind": "SPAN_KIND_CONSUMER" },
            { "traceId": "001b8f567e6d35bdb5b9ae70acd1c8e8", "spanId": "cefebb1ab5cf177a", "parentSpanId": "8ee1c52785277f85", "name": "lock.acquire", "kind": "SPAN_KIND_INTERNAL" },
            { "traceId": "001b8f567e6d35bdb5b9ae70acd1c8e8", "spanId": "36335258b8cf633d", "parentSpanId": "8ee1c52785277f85", "name": "db.tx", "kind": "SPAN_KIND_CLIENT" },
            { "traceId": "001b8f567e6d35bdb5b9ae70acd1c8e8", "spanId": "5c91fa38d72e1de1", "parentSpanId": "8ee1c52785277f85", "name": "db.sql.tx_exec", "kind": "SPAN_KIND_CLIENT" }
          ]
        }
      ]
    }
  ]
}
```

---

## Sampel teks eksposisi Prometheus (`/metrics`)

Sampel berikut merupakan keluaran format teks eksposisi Prometheus dari endpoint `/metrics` (`internal/telemetry/metrics.go`) yang memperlihatkan seluruh 11 keluarga metrik berkardinalitas rendah serta label `db_role="platform"` dan `db_role="target_postgres"` yang seragam pada API (`cmd/server/main.go`) maupun worker (`cmd/worker/main.go`):

```text
# HELP db_pool_connections Current database connection pool connections by db_role and state (active, idle).
# TYPE db_pool_connections gauge
db_pool_connections{db_role="platform",state="active"} 2
db_pool_connections{db_role="platform",state="idle"} 3
db_pool_connections{db_role="target_postgres",state="active"} 1
db_pool_connections{db_role="target_postgres",state="idle"} 2
# HELP db_pool_wait_count_total Total number of connections waited for from the database pool by db_role.
# TYPE db_pool_wait_count_total counter
db_pool_wait_count_total{db_role="platform"} 0
db_pool_wait_count_total{db_role="target_postgres"} 4
# HELP db_pool_wait_duration_seconds_total Total time blocked waiting for a database pool connection in seconds by db_role.
# TYPE db_pool_wait_duration_seconds_total counter
db_pool_wait_duration_seconds_total{db_role="platform"} 0
db_pool_wait_duration_seconds_total{db_role="target_postgres"} 0.185
# HELP migration_batches_processed_total Total migration batches processed by status (committed, reverted, failed).
# TYPE migration_batches_processed_total counter
migration_batches_processed_total{status="committed"} 1
# HELP migration_execution_duration_seconds End-to-end migration execution duration by mode and terminal status.
# TYPE migration_execution_duration_seconds histogram
migration_execution_duration_seconds_bucket{mode="batch",status="success",le="0.01"} 0
migration_execution_duration_seconds_bucket{mode="batch",status="success",le="0.05"} 0
migration_execution_duration_seconds_bucket{mode="batch",status="success",le="0.1"} 0
migration_execution_duration_seconds_bucket{mode="batch",status="success",le="0.25"} 0
migration_execution_duration_seconds_bucket{mode="batch",status="success",le="0.5"} 0
migration_execution_duration_seconds_bucket{mode="batch",status="success",le="1"} 0
migration_execution_duration_seconds_bucket{mode="batch",status="success",le="2.5"} 0
migration_execution_duration_seconds_bucket{mode="batch",status="success",le="5"} 0
migration_execution_duration_seconds_bucket{mode="batch",status="success",le="10"} 0
migration_execution_duration_seconds_bucket{mode="batch",status="success",le="30"} 1
migration_execution_duration_seconds_bucket{mode="batch",status="success",le="60"} 1
migration_execution_duration_seconds_bucket{mode="batch",status="success",le="120"} 1
migration_execution_duration_seconds_bucket{mode="batch",status="success",le="+Inf"} 1
migration_execution_duration_seconds_sum{mode="batch",status="success"} 16.804
migration_execution_duration_seconds_count{mode="batch",status="success"} 1
# HELP migration_job_retries_total Total migration job retries by action_type.
# TYPE migration_job_retries_total counter
migration_job_retries_total{action_type="up"} 1
# HELP migration_jobs_inflight Current number of migration jobs waiting in queue or running in worker.
# TYPE migration_jobs_inflight gauge
migration_jobs_inflight{status="running"} 0
migration_jobs_inflight{status="waiting"} 1
# HELP migration_jobs_total Total migration jobs transitioned by status, action_type, and mode.
# TYPE migration_jobs_total counter
migration_jobs_total{action_type="up",mode="batch",status="pending_queue"} 1
migration_jobs_total{action_type="up",mode="batch",status="running"} 1
migration_jobs_total{action_type="up",mode="batch",status="success"} 1
# HELP migration_lock_wait_seconds Time spent waiting to acquire the per-database distributed lock.
# TYPE migration_lock_wait_seconds histogram
migration_lock_wait_seconds_bucket{le="0.005"} 0
migration_lock_wait_seconds_bucket{le="0.01"} 0
migration_lock_wait_seconds_bucket{le="0.05"} 1
migration_lock_wait_seconds_bucket{le="0.1"} 1
migration_lock_wait_seconds_bucket{le="0.25"} 1
migration_lock_wait_seconds_bucket{le="0.5"} 1
migration_lock_wait_seconds_bucket{le="1"} 1
migration_lock_wait_seconds_bucket{le="2.5"} 1
migration_lock_wait_seconds_bucket{le="5"} 1
migration_lock_wait_seconds_bucket{le="10"} 1
migration_lock_wait_seconds_bucket{le="30"} 1
migration_lock_wait_seconds_bucket{le="+Inf"} 1
migration_lock_wait_seconds_sum 0.012
migration_lock_wait_seconds_count 1
# HELP migration_queue_wait_seconds Time elapsed from job enqueue until worker starts processing.
# TYPE migration_queue_wait_seconds histogram
migration_queue_wait_seconds_bucket{le="0.01"} 0
migration_queue_wait_seconds_bucket{le="0.05"} 1
migration_queue_wait_seconds_bucket{le="0.1"} 1
migration_queue_wait_seconds_bucket{le="0.25"} 1
migration_queue_wait_seconds_bucket{le="0.5"} 1
migration_queue_wait_seconds_bucket{le="1"} 1
migration_queue_wait_seconds_bucket{le="2.5"} 1
migration_queue_wait_seconds_bucket{le="5"} 1
migration_queue_wait_seconds_bucket{le="10"} 1
migration_queue_wait_seconds_bucket{le="30"} 1
migration_queue_wait_seconds_bucket{le="60"} 1
migration_queue_wait_seconds_bucket{le="+Inf"} 1
migration_queue_wait_seconds_sum 0.028
migration_queue_wait_seconds_count 1
# HELP migration_rows_processed_total Total target database rows affected across migration executions.
# TYPE migration_rows_processed_total counter
migration_rows_processed_total 500000
```
