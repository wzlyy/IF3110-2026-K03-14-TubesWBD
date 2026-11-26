param(
    [string]$BaseUrl = "http://127.0.0.1:8080",
    [string]$EditorUser = "editor_core",
    [string]$LeaderCoreUser = "leader_core",
    [string]$LeaderAnalyticsUser = "leader_analytics",
    [string]$Password = "DevPass!2026",
    [int]$CountPerDB = 30
)

$ErrorActionPreference = 'Stop'

function Invoke-ApiLogin([string]$Username, [string]$Pass) {
    $session = New-Object Microsoft.PowerShell.Commands.WebRequestSession
    $body = @{ username = $Username; password = $Pass } | ConvertTo-Json
    $resp = Invoke-RestMethod -Uri "$BaseUrl/api/v1/auth/login" -Method Post -ContentType "application/json" -Body $body -WebSession $session
    return @{ Session = $session; Csrf = $resp.csrf_token }
}

function Invoke-ApiPost([hashtable]$Auth, [string]$Path, $Payload) {
    $headers = @{ "X-CSRF-Token" = $Auth.Csrf }
    $json = if ($null -ne $Payload) { $Payload | ConvertTo-Json -Depth 5 } else { "{}" }
    return Invoke-RestMethod -Uri "$BaseUrl$Path" -Method Post -ContentType "application/json" -Headers $headers -Body $json -WebSession $Auth.Session
}

Write-Host "[k6-setup] Logging in seeded editor and group leaders..."
$editor = Invoke-ApiLogin $EditorUser $Password
$leaderCore = Invoke-ApiLogin $LeaderCoreUser $Password
$leaderAnalytics = Invoke-ApiLogin $LeaderAnalyticsUser $Password

$dbs = (Invoke-RestMethod -Uri "$BaseUrl/api/v1/databases" -WebSession $editor.Session).databases
if ($dbs.Count -lt 2) {
    throw "Expected at least 2 seeded PostgreSQL databases (target-postgres-1 and target-postgres-2). Run 'go run ./cmd/seed' first."
}

$db1 = $dbs[0].id
$db2 = $dbs[1].id
$sameDbReqs = @()
$diffDbReqs = @()

for ($i = 1; $i -le $CountPerDB; $i++) {
    # Migrasi pra-persetujuan di DB1 (butuh leader_core & leader_analytics)
    # Sisipkan beban baris agar durasi eksekusi cukup memicu kontensi distributed lock saat dikirim berpasangan.
    $m1 = Invoke-ApiPost $editor "/api/v1/databases/$db1/migrations" @{
        name     = "k6_same_db_$i"
        sql_up   = "CREATE TABLE IF NOT EXISTS k6_probe_1_$i (id INT PRIMARY KEY, created_at TIMESTAMPTZ DEFAULT NOW()); INSERT INTO k6_probe_1_$i (id) SELECT generate_series(1, 30000) ON CONFLICT DO NOTHING;"
        sql_down = "DROP TABLE IF EXISTS k6_probe_1_$i;"
    }
    $r1 = Invoke-ApiPost $editor "/api/v1/databases/$db1/requests" @{
        migration_id = $m1.id
        action_type  = "up"
    }
    $null = Invoke-ApiPost $editor "/api/v1/requests/$($r1.id)/submit" $null
    $null = Invoke-ApiPost $leaderCore "/api/v1/requests/$($r1.id)/approve" @{ comment = "k6 pre-approved core" }
    $null = Invoke-ApiPost $leaderAnalytics "/api/v1/requests/$($r1.id)/approve" @{ comment = "k6 pre-approved analytics" }
    $sameDbReqs += $r1.id

    # Migrasi pra-persetujuan di DB2 (butuh leader_core)
    $m2 = Invoke-ApiPost $editor "/api/v1/databases/$db2/migrations" @{
        name     = "k6_diff_db_$i"
        sql_up   = "CREATE TABLE IF NOT EXISTS k6_probe_2_$i (id SERIAL PRIMARY KEY, created_at TIMESTAMPTZ DEFAULT NOW());"
        sql_down = "DROP TABLE IF EXISTS k6_probe_2_$i;"
    }
    $r2 = Invoke-ApiPost $editor "/api/v1/databases/$db2/requests" @{
        migration_id = $m2.id
        action_type  = "up"
    }
    $null = Invoke-ApiPost $editor "/api/v1/requests/$($r2.id)/submit" $null
    $null = Invoke-ApiPost $leaderCore "/api/v1/requests/$($r2.id)/approve" @{ comment = "k6 pre-approved core db2" }
    $diffDbReqs += $r2.id
}

Write-Host ""
Write-Host "Pre-approved requests ready! Run k6 with:"
Write-Host "`$env:K6_DB1_ID='$db1'"
Write-Host "`$env:K6_DB2_ID='$db2'"
Write-Host "`$env:K6_SAME_DB_REQUEST_IDS='$($sameDbReqs -join ',')'"
Write-Host "`$env:K6_DIFF_DB_REQUEST_IDS='$($diffDbReqs -join ',')'"
Write-Host "k6 run tests/k6/stress_test.js"
