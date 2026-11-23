-- Jalankan sekali di target PostgreSQL pengujian. Nama yang sudah ada membatalkan transaksi.
BEGIN;
CREATE TABLE public.benchmark_accounts (
    id bigint PRIMARY KEY,
    balance bigint NOT NULL,
    note text NOT NULL
);
INSERT INTO public.benchmark_accounts (id, balance, note)
SELECT id, 1000, md5(id::text) FROM generate_series(1, 500000) AS series(id);
ANALYZE public.benchmark_accounts;
COMMIT;
