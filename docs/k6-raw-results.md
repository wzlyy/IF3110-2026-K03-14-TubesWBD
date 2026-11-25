# Hasil k6, 23 November 2026

Docker Desktop, k6 1.2.3, API dan empat consumer worker, dua PostgreSQL target, observabilitas aktif. Pada run baseline tanggal 23 November 2026, skrip awal `tests/k6/stress_test.js` dijalankan terhadap layanan Compose dengan seed 20 request yang sudah disetujui pada setiap database (40 job total yang diluncurkan serentak mulai `t = 15s`). Waktu antre diukur dari sebelum POST enqueue sampai `started_at` pada attempt pertama. Durasi penyelesaian berakhir ketika polling melihat status terminal.

| Metrik k6 | Hasil |
|---|---:|
| HTTP requests, termasuk login setup | 27.010 |
| Throughput seluruh run | 167,82 req/s |
| HTTP durasi p50 / p95 / maksimum | 9,42 / 71,39 / 30.102 ms |
| HTTP gagal | 0 / 27.010 |
| Job selesai / gagal | 40 / 0 |
| Waktu antre p50 / p95 | 28 / 4.175 ms |
| Waktu enqueue sampai sukses p50 / p95 | 301 / 4.308 ms |
| Throughput penyelesaian job, seluruh run | 0,249 job/s |

Rincian di bawah dihitung dari time series `--out json`, berdasarkan waktu respons selesai. Jendela dihitung dari sampel skenario pertama. Pembagian mendekati tahap VU yang dijadwalkan karena skrip aslinya hanya memberi tag skenario dan endpoint. Jendela terakhir mencakup 7,87 detik sampel yang tersisa. Login setup dikeluarkan dari tabel.

| Jendela waktu (s) | HTTP selesai | HTTP p50 (ms) | HTTP p95 (ms) | req/s | HTTP gagal | Job sukses |
|---|---:|---:|---:|---:|---:|---:|
| 0–30 | 1.359 | 5,42 | 22,36 | 45,30 | 0 | 40 |
| 30–60 | 4.413 | 5,00 | 23,68 | 147,10 | 0 | 0 |
| 60–90 | 9.610 | 5,10 | 22,44 | 320,33 | 0 | 0 |
| 90–120 | 11.130 | 16,43 | 98,46 | 371,00 | 0 | 0 |
| 120–150 | 392 | 51,77 | 29.257,98 | 13,07 | 0 | 0 |
| 150–akhir | 105 | 7,37 | 17,82 | 13,34 | 0 | 0 |

Keempat puluh job pada run 23 November 2026 tersebar merata pada dua DB dan selesai pada jendela pertama (1,33 job/s di jendela itu).

## Analisis lonjakan p95 pada transisi 100 VU ke ramp-down (`120–150s`)

Lonjakan `p95` menjadi **29.257,98 ms** pada jendela `120–150s` bersumber dari kombinasi tiga mekanisme:

1. **Antrean pool koneksi target (`SetMaxOpenConns(5)`):** Pada [`internal/database/target_manager.go`](../internal/database/target_manager.go#L139), setiap pool PostgreSQL target dibatasi `SetMaxOpenConns(5)`. Ketika ~100 VU pada puncak `90–120s` memanggil `GET /api/v1/databases/{id}/schema` secara serentak pada dua target (~50 VU per target, rasio kontensi 10:1), setiap panggilan `LoadCatalog` (`internal/database/catalog.go`) menahan satu koneksi dalam transaksi `REPEATABLE READ READ ONLY` untuk mengeksekusi 6 kueri katalog berurutan beserta penulisan log/span, sementara request baca lain juga memperebutkan pool Platform DB (`SetMaxOpenConns(25)` di [`internal/database/platform.go`](../internal/database/platform.go#L264)).
2. **Backpressure soket & batas `WriteTimeout`:** `http_req_sending` p95 k6 pada fase tersebut ikut melonjak ke `27,80 detik` dengan latensi maksimum `30.102 ms` (tepat di sekitar batas `WriteTimeout: 30s` di [`cmd/server/main.go`](../cmd/server/main.go#L117)) tanpa satu pun kegagalan HTTP (`0%`).
3. **Completion-time bucketing pada jendela ramp-down:** Sampel `--out json` dikelompokkan menurut waktu respons *selesai*. Request ekor yang tertahan di ujung fase puncak `90–120s` baru tuntas setelah `t = 120s`. Karena pada `120–150s` beban turun drastis menjadi 10 VU dengan hanya `392` respons selesai (vs. `11.130` respons pada `90–120s`), sekitar 20–30 respons ekor warisan puncak sudah cukup menggeser persentil ke-95 ke `29,26 detik` sebelum pulih normal ke `17,82 ms` di jendela `150–akhir`.

## Pembaruan skenario `stress_test.js` & `setup_preapproved.ps1`

Untuk mengatasi ketiadaan sampel job pada fase beban menengah hingga pemulihan:
- `tests/k6/setup_preapproved.ps1` menaikkan kuota bawaan `-CountPerDB` menjadi `30` (**60 job pra-persetujuan** pada dua target).
- `tests/k6/stress_test.js` menyebarkan pengiriman job ke lima jendela tahap beban secara proporsional (`baseline_10vu` `5–28s`, `ramp_30vu` `30–58s`, `ramp_60vu` `60–88s`, `peak_100vu` `90–118s`, dan `recovery_10vu` `120–148s`) dengan tag `load_stage` (mode burst awal tetap tersedia melalui `K6_JOB_MODE=burst`).
