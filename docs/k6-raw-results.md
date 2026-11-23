# Hasil k6, 23 November 2026

Docker Desktop, k6 1.2.3, API dan empat consumer worker, dua PostgreSQL target, observabilitas aktif. Skrip asli `tests/k6/stress_test.js` dijalankan terhadap layanan Compose. Seed menyiapkan 20 request yang sudah disetujui pada setiap database. Waktu antre diukur dari sebelum POST enqueue sampai `started_at` pada attempt pertama. Durasi penyelesaian berakhir ketika polling melihat status terminal.

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

Keempat puluh job tersebar merata pada dua DB dan selesai pada jendela pertama (1,33 job/s di jendela itu). Jendela berikutnya mengukur trafik baca. Tidak ada sampel antre atau durasi job untuk menghitung persentil per tahap berikutnya.
