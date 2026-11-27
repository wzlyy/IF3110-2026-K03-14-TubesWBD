# Hasil Uji Beban k6 (27 November 2026)

Pengujian dijalankan pada **27 November 2026** menggunakan `grafana/k6:1.2.3` terhadap stack Docker Compose lengkap (`backend-api`, `worker` dengan `WORKER_CONCURRENCY=4`, `platform-db`, `target-postgres`, `target-postgres-2`, `rabbitmq`, serta stack observabilitas OTel Collector, Tempo, Loki, Prometheus, dan Grafana).

Sebanyak **60 request migrasi pra-persetujuan** (`30` `same_db` pada `target-postgres-1` dan `30` `diff_db` pada `target-postgres-2`, disiapkan melalui `tests/k6/setup_preapproved.ps1 -CountPerDB 30`) dijalankan dalam mode **`K6_JOB_MODE=staged`** menggunakan tiga skenario konkuren di `tests/k6/stress_test.js`:

1. **`api_read_stress` (`ramping-vus`)**: menaikkan trafik baca (`GET /databases`, `GET /history`, `GET /schema`) secara bertahap dari `0 → 10 → 30 → 60 → 100 VU` lalu menurunkannya kembali ke `10 → 0 VU` (total 2m50s).
2. **`same_db_migration_jobs` (`per-vu-iterations`, 1 VU)**: mengirim 30 job `target-postgres-1` secara berpasangan lintas lima tahap beban (`baseline_10vu`, `ramp_30vu`, `ramp_60vu`, `peak_100vu`, `recovery_10vu`). Job pertama tiap pasangan ditunggu hingga masuk status `running`/terminal melalui `waitUntilRunningOrTerminal` sebelum job pasangannya dikirim, sehingga kontensi *distributed lock* tetap terpicu tanpa melanggar urutan versi linear.
3. **`diff_db_migration_jobs` (`per-vu-iterations`, 1 VU)**: mengirim 30 job `target-postgres-2` secara paralel dengan DB1 lintas lima tahap beban yang sama.

Waktu tunggu antrean (`migration_queue_wait_ms`) diukur dari sebelum `POST /enqueue` sampai `started_at` pada attempt eksekusi; durasi penyelesaian job (`migration_job_completion_duration_ms`) diukur dari sebelum `POST /enqueue` sampai polling `GET /api/v1/jobs/{id}` membaca `request_status` terminal (`success`), sehingga respons `202 Accepted` tidak dihitung sebagai keberhasilan eksekusi.

## 1. Ringkasan Agregat & Status Threshold

| Metrik k6 | Hasil | Ambang / Status |
|---|---:|---|
| Durasi total run | 171,16 detik (`171.156,72 ms`) | — |
| Total HTTP requests (termasuk 1 login setup) | 35.144 | — |
| Throughput HTTP agregat | 205,33 req/s | — |
| HTTP durasi (`p50` / `p90` / `p95` / `max`) | 8,53 / 26,18 / **36,42** / 180,90 ms | `p(95)<500` (**PASS**) |
| HTTP gagal (`http_req_failed`) | **0 / 35.144 (0,00%)** | `rate<0.02` (**PASS**) |
| Checks k6 lulus | 34.882 / 34.882 (100,00%) | — |
| Job migrasi selesai / gagal | **60 / 0** (`100,00%`) | `rate>0.95` (**PASS**) |
| Throughput penyelesaian job agregat | 0,351 job/s | — |
| Waktu tunggu antrean (`min` / `p50` / `p95` / `max`) | 28 / 45,0 / 2.066,95 / 2.189 ms | — |
| Durasi enqueue hingga sukses (`min` / `p50` / `p95` / `max`) | 280 / 369,0 / 2.649,40 / 2.758 ms | — |

## 2. Rincian Per Jendela Waktu (30 Detik)

Sampel `--out json` (`398.216` baris JSON pada rentang skenario `167,59 detik`, tidak termasuk 1 request login setup) dikelompokkan ke enam jendela 30 detik berdasarkan waktu selesainya setiap sampel:

| Jendela waktu | HTTP selesai | req/s | HTTP p50 (ms) | HTTP p95 (ms) | HTTP gagal | Job sukses | Job/s | Queue wait p50 / p95 (ms) | Durasi job p50 / p95 (ms) |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| `0–30s` (0 → 10 VU) | 1.018 | 33,93 | 7,85 | 15,80 | 0 | 11 | 0,367 | 45,0 / 2.113,50 | 367,0 / 2.557,50 |
| `30–60s` (10 → 30 VU) | 3.700 | 123,33 | 8,58 | 28,64 | 0 | 12 | 0,400 | 48,5 / 2.113,10 | 354,0 / 2.575,95 |
| `60–90s` (30 → 60 VU) | 8.023 | 267,43 | 8,05 | 30,15 | 0 | 15 | 0,500 | 42,0 / 2.051,00 | 373,0 / 2.645,50 |
| `90–120s` (60 → 100 VU) | 13.609 | 453,63 | 10,26 | 48,20 | 0 | 14 | 0,467 | 44,0 / 2.047,40 | 353,5 / 2.666,45 |
| `120–150s` (100 → 10 VU) | 8.310 | 277,00 | 6,46 | 24,90 | 0 | 8 | 0,267 | 43,5 / 2.138,35 | 346,0 / 2.473,25 |
| `150s–akhir` (17,59s, → 0 VU) | 483 | 27,45 | 8,95 | 29,79 | 0 | 0 | 0,000 | — | — |

## 3. Rincian Metrik Job Per Tahap Beban (`load_stage`)

Berdasarkan tag `load_stage` yang dilekatkan `tests/k6/stress_test.js` pada saat penjadwalan tiap job:

| Tahap (`load_stage`) | Job sukses / gagal | Queue wait p50 / p95 (ms) | Durasi selesai p50 / p95 (ms) |
|---|---:|---:|---:|
| `baseline_10vu` (`5–28s`) | 11 / 0 | 45,0 / 2.113,50 | 367,0 / 2.557,50 |
| `ramp_30vu` (`30–58s`) | 12 / 0 | 48,5 / 2.113,10 | 354,0 / 2.575,95 |
| `ramp_60vu` (`60–88s`) | 13 / 0 | 45,0 / 2.051,00 | 373,0 / 2.646,00 |
| `peak_100vu` (`90–118s`) | 16 / 0 | 42,5 / 2.047,00 | 353,5 / 2.663,75 |
| `recovery_10vu` (`120–148s`) | 8 / 0 | 43,5 / 2.138,35 | 346,0 / 2.473,25 |

## 4. Perbandingan `same_db` vs `diff_db` & Rincian Endpoint

| Kelompok / Endpoint | Jumlah | Sukses / Gagal | Latensi / Durasi `p50` | `p95` | `max` |
|---|---:|---:|---:|---:|---:|
| Job `same_db` (`target-postgres-1`) — queue wait | 30 | 30 / 0 | 1.084,0 ms | 2.180,00 ms | 2.189 ms |
| Job `same_db` (`target-postgres-1`) — total selesai | 30 | 30 / 0 | 1.510,0 ms | 2.671,85 ms | 2.758 ms |
| Job `diff_db` (`target-postgres-2`) — queue wait | 30 | 30 / 0 | 40,0 ms | 60,65 ms | 104 ms |
| Job `diff_db` (`target-postgres-2`) — total selesai | 30 | 30 / 0 | 295,0 ms | 321,20 ms | 373 ms |
| `GET /api/v1/databases` | 11.607 | 11.607 / 0 | 4,25 ms | 24,81 ms | 162,52 ms |
| `GET /api/v1/databases/{id}/history` | 11.607 | 11.607 / 0 | 6,36 ms | 25,56 ms | 164,95 ms |
| `GET /api/v1/databases/{id}/schema` | 11.607 | 11.607 / 0 | 13,91 ms | 50,45 ms | 180,90 ms |
| `POST /api/v1/requests/{id}/enqueue` | 60 | 60 / 0 | 25,73 ms | 45,83 ms | 118,93 ms |
| `GET /api/v1/jobs/{job_id}` (polling) | 262 | 262 / 0 | 7,81 ms | 27,87 ms | 44,00 ms |

## 5. Analisis Setiap Tingkat Beban

1. **Baseline (`0–30s`, `0 → 10 VU`):** Throughput baca `33,93 req/s` dengan `p50 = 7,85 ms` dan `p95 = 15,80 ms`. Sebelas job selesai (`0,367 job/s`) dengan `queue_wait p50 = 45,0 ms` dan `p95 = 2.113,50 ms`.
2. **Ramp-up tahap 1 (`30–60s`, `10 → 30 VU`):** Throughput naik hampir 4× ke `123,33 req/s` dengan `p50 = 8,58 ms` dan `p95 = 28,64 ms`. Dua belas job tuntas (`0,400 job/s`, durasi selesai `p50 = 354,0 ms`, `p95 = 2.575,95 ms`).
3. **Ramp-up beban tinggi (`60–90s`, `30 → 60 VU`):** Throughput mencapai `267,43 req/s` dengan `p50 = 8,05 ms`, `p95 = 30,15 ms`, dan puncak ekor tunggal `180,90 ms` (masih jauh di bawah ambang `500 ms`). Lima belas job selesai pada jendela ini (`0,500 job/s`).
4. **Puncak (`90–120s`, `60 → 100 VU`):** Throughput mencapai puncak **`453,63 req/s`** (`13.609` HTTP selesai) dengan `p50 = 10,26 ms`, `p95 = 48,20 ms`, dan `0` kegagalan. Antrean job tetap stabil (`14` job selesai pada jendela ini; `16` job bertag `peak_100vu`, `queue_wait p50 = 42,5 ms` / `p95 = 2.047,0 ms`) karena SQL migrasi dieksekusi asinkron oleh worker.
5. **Pemulihan (`120–150s`, turun `100 → 10 VU` & `150s–akhir` menuju `0 VU`):** Sistem langsung pulih tanpa *tail latency* tertahan—`p50` HTTP turun ke `6,46 ms` dan `p95` HTTP turun ke `24,90 ms` (`120–150s`). Delapan job tersisa tuntas pada fase `recovery_10vu` (`durasi p50 = 346,0 ms`), dan setelah run berakhir `migration_executions` mencatat `60 / 60` `success` dengan `0` baris tersisa di `database_locks`.
6. **Perbandingan `same_db` vs `diff_db`:** Pada `diff_db` (`target-postgres-2`), seluruh 30 job diproses langsung tanpa antrean lock (`queue_wait p50 = 40,0 ms`, `p95 = 60,65 ms`, `max = 104 ms`; durasi selesai `p95 = 321,20 ms`). Sebaliknya pada `same_db` (`target-postgres-1`), distribusi `queue_wait` bersifat bimodal (`p50 = 1.084,0 ms`, `p95 = 2.180,00 ms`, `max = 2.189 ms`): job pertama di tiap pasangan langsung memperoleh lock (~31–60 ms), sedangkan job kedua dalam pasangan sengaja menemui `ErrLockBusy` saat job pertama sedang berjalan lalu masuk antrean ulang `migration.jobs.retry` dengan jeda `2 detik` (`internal/worker/consumer.go`) sambil tetap mempertahankan urutan versi linear (`60 / 60` sukses).
