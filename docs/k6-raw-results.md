# Hasil Uji Beban k6 (25 November 2026)

Pengujian dijalankan pada **25 November 2026** menggunakan `grafana/k6:1.2.3` terhadap stack Docker Compose lengkap (`backend-api`, `worker` dengan `WORKER_CONCURRENCY=4`, `platform-db`, `target-postgres`, `target-postgres-2`, `rabbitmq`, serta stack observabilitas OTel Collector, Tempo, Loki, Prometheus, dan Grafana).

Sebanyak **60 request migrasi pra-persetujuan** (`30` `same_db` pada `target-postgres-1` dan `30` `diff_db` pada `target-postgres-2`, disiapkan melalui `tests/k6/setup_preapproved.ps1 -CountPerDB 30`) dijalankan dalam mode **`K6_JOB_MODE=staged`** sehingga pengiriman job tersebar proporsional ke lima tahap beban (`baseline_10vu`, `ramp_30vu`, `ramp_60vu`, `peak_100vu`, `recovery_10vu`) secara bersamaan dengan trafik baca (`GET /databases`, `GET /history`, `GET /schema`) yang naik bertahap dari `0 → 10 → 30 → 60 → 100 VU` lalu turun kembali ke `10 → 0 VU`. Waktu tunggu antrean diukur dari sebelum `POST /enqueue` sampai `started_at` pada attempt eksekusi; durasi penyelesaian job diukur dari sebelum `POST /enqueue` sampai polling `GET /api/v1/jobs/{id}` melihat status terminal (`success`), sehingga respons `202 Accepted` tidak dihitung sebagai keberhasilan eksekusi.

## 1. Ringkasan Agregat & Status Threshold

| Metrik k6 | Hasil | Ambang / Status |
|---|---:|---|
| Durasi total run | 170,50 detik (`170.496,68 ms`) | — |
| Total HTTP requests (termasuk 1 login setup) | 35.455 | — |
| Throughput HTTP agregat | 207,95 req/s | — |
| HTTP durasi (`p50` / `p90` / `p95` / `max`) | 5,11 / 17,99 / **28,08** / 654,25 ms | `p(95)<500` (**PASS**) |
| HTTP gagal (`http_req_failed`) | **0 / 35.455 (0,00%)** | `rate<0.02` (**PASS**) |
| Checks k6 lulus | 35.335 / 35.335 (100,00%) | — |
| Job migrasi selesai / gagal | **60 / 0** (`100,00%`) | `rate>0.95` (**PASS**) |
| Throughput penyelesaian job agregat | 0,352 job/s | — |
| Waktu tunggu antrean (`min` / `p50` / `p95` / `max`) | 10 / 18,0 / 34,25 / 49 ms | — |
| Durasi enqueue hingga sukses (`min` / `p50` / `p95` / `max`) | 266 / 279,5 / 304,2 / 326 ms | — |

## 2. Rincian Per Jendela Waktu (30 Detik)

Sampel `--out json` (`401.932` titik data dalam rentang skenario `167,44 detik`, tidak termasuk login setup) dikelompokkan ke enam jendela 30 detik berdasarkan waktu selesainya setiap sampel:

| Jendela waktu | HTTP selesai | req/s | HTTP p50 (ms) | HTTP p95 (ms) | HTTP gagal | Job sukses | Job/s | Queue wait p50 / p95 (ms) | Durasi job p50 / p95 (ms) |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| `0–30s` (0 → 10 VU) | 999 | 33,30 | 6,53 | 20,47 | 0 | 10 | 0,333 | 19,0 / 30,65 | 277,0 / 285,55 |
| `30–60s` (10 → 30 VU) | 3.738 | 124,60 | 6,12 | 26,10 | 0 | 12 | 0,400 | 18,0 / 30,20 | 282,0 / 305,80 |
| `60–90s` (30 → 60 VU) | 7.949 | 264,97 | 5,24 | 39,36 | 0 | 15 | 0,500 | 17,0 / 36,50 | 280,0 / 303,90 |
| `90–120s` (60 → 100 VU) | 14.165 | 472,17 | 5,24 | 27,70 | 0 | 15 | 0,500 | 18,0 / 34,05 | 282,0 / 301,50 |
| `120–150s` (100 → 10 VU) | 8.131 | 271,03 | 4,49 | 22,31 | 0 | 8 | 0,267 | 18,5 / 23,30 | 277,0 / 287,30 |
| `150s–akhir` (17,44s, → 0 VU) | 472 | 27,07 | 3,83 | 14,45 | 0 | 0 | 0,000 | — | — |

## 3. Rincian Metrik Job Per Tahap Beban (`load_stage`)

Berdasarkan tag `load_stage` yang dilekatkan `tests/k6/stress_test.js` pada saat penjadwalan tiap job:

| Tahap (`load_stage`) | Job sukses / gagal | Queue wait p50 / p95 (ms) | Durasi selesai p50 / p95 (ms) |
|---|---:|---:|---:|
| `baseline_10vu` (`5–28s`) | 9 / 0 | 20,0 / 30,8 | 277,0 / 285,6 |
| `ramp_30vu` (`30–58s`) | 12 / 0 | 18,0 / 30,2 | 279,0 / 295,2 |
| `ramp_60vu` (`60–88s`) | 15 / 0 | 17,0 / 37,0 | 280,0 / 311,6 |
| `peak_100vu` (`90–118s`) | 15 / 0 | 19,0 / 32,2 | 282,0 / 301,5 |
| `recovery_10vu` (`120–148s`) | 9 / 0 | 19,0 / 25,2 | 277,0 / 289,8 |

## 4. Perbandingan `same_db` vs `diff_db` & Rincian Endpoint

| Kelompok / Endpoint | Jumlah | Sukses / Gagal | Latensi / Durasi `p50` | `p95` | `max` |
|---|---:|---:|---:|---:|---:|
| Job `same_db` (`target-postgres-1`) — queue wait | 30 | 30 / 0 | 19,0 ms | 41,75 ms | 49,0 ms |
| Job `same_db` (`target-postgres-1`) — total selesai | 30 | 30 / 0 | 283,5 ms | 312,80 ms | 326,0 ms |
| Job `diff_db` (`target-postgres-2`) — queue wait | 30 | 30 / 0 | 18,0 ms | 24,10 ms | 26,0 ms |
| Job `diff_db` (`target-postgres-2`) — total selesai | 30 | 30 / 0 | 277,0 ms | 289,65 ms | 308,0 ms |
| `GET /api/v1/databases` | 11.758 | 11.758 / 0 | 3,42 ms | 19,53 ms | 625,83 ms |
| `GET /api/v1/databases/{id}/history` | 11.758 | 11.758 / 0 | 3,46 ms | 13,61 ms | 654,25 ms |
| `GET /api/v1/databases/{id}/schema` | 11.758 | 11.758 / 0 | 11,98 ms | 38,07 ms | 566,87 ms |
| `POST /api/v1/requests/{id}/enqueue` | 60 | 60 / 0 | 15,36 ms | 24,87 ms | 39,67 ms |
| `GET /api/v1/jobs/{job_id}` (polling) | 120 | 120 / 0 | 5,64 ms | 15,78 ms | 37,98 ms |

## 5. Analisis Setiap Tingkat Beban

1. **Baseline (`0–30s`, menuju 10 VU):** Throughput baca `33,30 req/s` dengan `p50 = 6,53 ms` dan `p95 = 20,47 ms`. Sepuluh job selesai (`0,333 job/s`) dengan `queue_wait p50 = 19 ms`.
2. **Ramp-up tahap 1 (`30–60s`, menuju 30 VU):** Throughput naik hampir 4× ke `124,60 req/s`, sementara `p95` HTTP hanya bergerak tipis ke `26,10 ms` dengan `12` job tuntas (`p95 = 305,80 ms`).
3. **Ramp-up beban tinggi (`60–90s`, menuju 60 VU):** Throughput mencapai `264,97 req/s` dengan `15` job tuntas (`0,500 job/s`). Terjadi lonjakan sesaat pada ekor maksimum (`654,25 ms`, `p95 = 39,36 ms`) karena kontensi sesaat pada pool target (`SetMaxOpenConns(5)` di `internal/database/target_manager.go`) saat `GET /schema` mengeksekusi transaksi `REPEATABLE READ` katalog secara bersamaan dengan eksekusi migrasi.
4. **Puncak (`90–120s`, menuju 100 VU):** Throughput mencapai puncak **`472,17 req/s`** (`14.165` HTTP selesai) dengan `p50 = 5,24 ms`, `p95 = 27,70 ms`, dan `0` kegagalan. Sebanyak `15` job tetap tuntas lancar (`0,500 job/s`, queue wait `p95 = 34,05 ms`, durasi `p95 = 301,50 ms`) berkat pemisahan eksekusi SQL asinkron ke worker dan kestabilan koneksi RabbitMQ.
5. **Pemulihan (`120–150s`, turun 100 → 10 VU & `150s–akhir` menuju 0 VU):** Tidak ada penumpukan antrean ekor yang menahan sistem—`p95` HTTP langsung turun ke `22,31 ms` (`120–150s`) dan `14,45 ms` (`150s–akhir`). Delapan job terakhir selesai di awal jendela pemulihan (`queue_wait p95 = 23,30 ms`) sehingga saat pendinginan seluruh `60 / 60` job sudah tuntas dan gauge inflight kembali ke `0`.
6. **Perbandingan `same_db` vs `diff_db`:** `same_db` menunjukkan ekor waktu tunggu antrean lebih tinggi (`p95 = 41,75 ms`, `max = 49 ms`) dibanding `diff_db` (`p95 = 24,10 ms`, `max = 26 ms`) karena eksekusi pada `target-postgres-1` harus bergantian lewat distributed lock per database, sedangkan `diff_db` pada `target-postgres-2` berjalan paralel pada pool worker (`WORKER_CONCURRENCY=4`). Catatan: pada eksperimen awal 23 November 2026 (`K6_JOB_MODE=burst`, 40 job serentak di `t = 15s`), pengiriman serentak di satu titik memicu `queue_wait p95 = 4.175 ms` untuk antrean DB yang sama; pemisahan bertahap pada run 25 November ini menunjukkan perilaku antrean dan pemulihan di semua tingkat beban secara merata.
