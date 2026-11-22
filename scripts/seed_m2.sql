-- ==============================================================================
-- Seed pengembangan Milestone 2 (bagian SQL: pengguna, grup, leader, dan keanggotaan)
-- Catatan: Koneksi DB target memerlukan enkripsi AES-256-GCM dengan ENCRYPTION_KEY.
--          Jalankan `go run ./cmd/seed` untuk mengisi pengguna/grup dan koneksi target terenkripsi.
-- Kata sandi bawaan pengguna non-admin (bcrypt cost 10): DevPass!2026
-- ==============================================================================

INSERT INTO users (id, username, password_hash, is_platform_admin, is_active)
VALUES
    ('11111111-1111-1111-1111-111111111101', 'leader_core',      '$2a$10$U9HCGxTmtfFiVCpdeI6AhuEM2nMtwDjgFm6MmP3/5wcpl25SorpoO', FALSE, TRUE),
    ('11111111-1111-1111-1111-111111111102', 'leader_analytics', '$2a$10$U9HCGxTmtfFiVCpdeI6AhuEM2nMtwDjgFm6MmP3/5wcpl25SorpoO', FALSE, TRUE),
    ('11111111-1111-1111-1111-111111111103', 'editor_core',      '$2a$10$U9HCGxTmtfFiVCpdeI6AhuEM2nMtwDjgFm6MmP3/5wcpl25SorpoO', FALSE, TRUE),
    ('11111111-1111-1111-1111-111111111104', 'viewer_analytics', '$2a$10$U9HCGxTmtfFiVCpdeI6AhuEM2nMtwDjgFm6MmP3/5wcpl25SorpoO', FALSE, TRUE)
ON CONFLICT (username) DO NOTHING;

INSERT INTO groups (id, name, leader_user_id)
VALUES
    ('22222222-2222-2222-2222-222222222201', 'team-core',      '11111111-1111-1111-1111-111111111101'),
    ('22222222-2222-2222-2222-222222222202', 'team-analytics', '11111111-1111-1111-1111-111111111102')
ON CONFLICT (name) DO UPDATE SET leader_user_id = EXCLUDED.leader_user_id;

INSERT INTO user_groups (user_id, group_id)
VALUES
    ('11111111-1111-1111-1111-111111111101', '22222222-2222-2222-2222-222222222201'),
    ('11111111-1111-1111-1111-111111111103', '22222222-2222-2222-2222-222222222201'),
    ('11111111-1111-1111-1111-111111111102', '22222222-2222-2222-2222-222222222202'),
    ('11111111-1111-1111-1111-111111111104', '22222222-2222-2222-2222-222222222202')
ON CONFLICT (user_id, group_id) DO NOTHING;
