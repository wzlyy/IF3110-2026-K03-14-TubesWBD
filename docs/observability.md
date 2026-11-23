# Observabilitas migrasi

Dari root repo, jalankan `docker compose -f compose.yaml -f compose.observability.yaml up -d --build`. Buka [Grafana lokal](http://localhost:3000/d/migration-platform) dan pilih dashboard **Platform Migrasi**. Instalasi Grafana baru memakai login awal bawaan image dan meminta penggantian password. Port host dapat dipilih dengan `GRAFANA_PORT`.

API dan worker mengirim span OTLP/HTTP ke Collector. Docker meneruskan stdout/stderr kedua proses melalui logging driver Fluentd ke receiver Fluent Forward di Collector. Collector menyalurkan trace ke Tempo dan log ke endpoint OTLP Loki. Prometheus membaca `/metrics` tiap lima detik pada API dan worker. Datasource dan dashboard dimuat dari berkas provisioning saat Grafana mulai.

Dashboard memperlihatkan jumlah job per status, retry, p50/p95 waktu antre, waktu tunggu lock dan durasi eksekusi, laju batch/baris, serta pemakaian dan waktu tunggu pool koneksi. Gauge waiting/running mengambil nilai maksimum kedua proses karena keduanya membaca tabel request yang sama. Panel distribusi baru terisi setelah ada pengamatan dalam jendela waktu panel.

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
