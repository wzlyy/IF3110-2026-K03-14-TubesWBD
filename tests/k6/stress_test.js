import http from 'k6/http';
import { check, sleep } from 'k6';
import { Counter, Rate, Trend } from 'k6/metrics';
import { textSummary } from 'https://jslib.k6.io/k6-summary/0.0.4/index.js';

// Metrik kustom untuk memisahkan respons enqueue HTTP (202) dari penyelesaian job asinkron.
const queueWaitMS = new Trend('migration_queue_wait_ms', true);
const jobCompletionDurationMS = new Trend('migration_job_completion_duration_ms', true);
const jobsCompletedTotal = new Counter('migration_jobs_completed_total');
const jobsFailedTotal = new Counter('migration_jobs_failed_total');
const jobSuccessRate = new Rate('migration_job_success_rate');

const BASE_URL = __ENV.BASE_URL || 'http://127.0.0.1:8080';
const USERNAME = __ENV.K6_USERNAME || 'editor_core';
const PASSWORD = __ENV.K6_PASSWORD || 'DevPass!2026';
const DB1_ID = __ENV.K6_DB1_ID || '';
const DB2_ID = __ENV.K6_DB2_ID || '';
// Daftar ID request pra-persetujuan (dipisah koma) dari tests/k6/setup_preapproved.ps1
const SAME_DB_REQUEST_IDS = (__ENV.K6_SAME_DB_REQUEST_IDS || '').split(',').filter(Boolean);
const DIFF_DB_REQUEST_IDS = (__ENV.K6_DIFF_DB_REQUEST_IDS || '').split(',').filter(Boolean);
const POLL_TIMEOUT_SEC = parseInt(__ENV.K6_POLL_TIMEOUT_SEC || '90', 10);
const JOB_MODE = __ENV.K6_JOB_MODE || 'staged';

// Bagi seluruh job secara proporsional ke lima tahap beban agar fase baseline,
// ramp-up, puncak 100 VU, dan ramp-down pemulihan sama-sama terukur oleh metrik k6.
const JOB_STAGE_WINDOWS = [
  { name: 'baseline_10vu', startMs: 5000, endMs: 28000, weight: 0.15 },
  { name: 'ramp_30vu', startMs: 30000, endMs: 58000, weight: 0.20 },
  { name: 'ramp_60vu', startMs: 60000, endMs: 88000, weight: 0.25 },
  { name: 'peak_100vu', startMs: 90000, endMs: 118000, weight: 0.25 },
  { name: 'recovery_10vu', startMs: 120000, endMs: 148000, weight: 0.15 },
];

function scheduledStageWindow(index, total) {
  if (JOB_MODE === 'burst' || total <= 1) {
    return { offsetMs: 15000, stage: 'baseline_10vu' };
  }
  const progress = (index + 0.5) / total;
  let cumulative = 0;
  for (let i = 0; i < JOB_STAGE_WINDOWS.length; i++) {
    const win = JOB_STAGE_WINDOWS[i];
    const prev = cumulative;
    cumulative += win.weight;
    if (progress <= cumulative || i === JOB_STAGE_WINDOWS.length - 1) {
      const localProgress = Math.min(1, Math.max(0, (progress - prev) / win.weight));
      const offsetMs = Math.round(win.startMs + localProgress * (win.endMs - win.startMs));
      return { offsetMs, stage: win.name };
    }
  }
  return { offsetMs: 15000, stage: 'baseline_10vu' };
}

const HAS_JOB_SCENARIOS = SAME_DB_REQUEST_IDS.length > 0 || DIFF_DB_REQUEST_IDS.length > 0;
const scenarios = {
  // Skenario 1: Beban baca naik bertahap (10 -> 30 -> 60 -> 100 VU) lalu turun untuk pemulihan (10 -> 0 VU)
  api_read_stress: {
    executor: 'ramping-vus',
    exec: 'runReadTraffic',
    startVUs: 0,
    stages: [
      { duration: '30s', target: 10 },  // Beban normal
      { duration: '30s', target: 30 },  // Kenaikan tahap 1
      { duration: '30s', target: 60 },  // Beban tinggi
      { duration: '30s', target: 100 }, // Beban puncak
      { duration: '30s', target: 10 },  // Penurunan pemulihan
      { duration: '20s', target: 0 },   // Pendinginan
    ],
  },
};

if (SAME_DB_REQUEST_IDS.length > 0) {
  // Skenario 2A: Jalankan migrasi pra-persetujuan DB1 secara sekuensial berpasangan
  // agar urutan versi linear terjaga sambil tetap menguji kontensi distributed lock.
  scenarios.same_db_migration_jobs = {
    executor: 'per-vu-iterations',
    exec: 'runSameDBMigrationJobs',
    vus: 1,
    iterations: 1,
    maxDuration: '4m',
    startTime: '0s',
  };
}

if (DIFF_DB_REQUEST_IDS.length > 0) {
  // Skenario 2B: Jalankan migrasi pra-persetujuan DB2 paralel dengan DB1
  // sambil menjaga urutan versi linear pada DB2.
  scenarios.diff_db_migration_jobs = {
    executor: 'per-vu-iterations',
    exec: 'runDiffDBMigrationJobs',
    vus: 1,
    iterations: 1,
    maxDuration: '4m',
    startTime: '0s',
  };
}

export const options = {
  scenarios,
  thresholds: {
    http_req_duration: ['p(95)<500'],
    http_req_failed: ['rate<0.02'],
    ...(HAS_JOB_SCENARIOS ? { migration_job_success_rate: ['rate>0.95'] } : {}),
  },
};

export function setup() {
  const loginRes = http.post(
    `${BASE_URL}/api/v1/auth/login`,
    JSON.stringify({ username: USERNAME, password: PASSWORD }),
    { headers: { 'Content-Type': 'application/json' } }
  );
  check(loginRes, {
    'setup login status 200': (r) => r.status === 200,
  });
  const body = loginRes.json();
  const cookie = loginRes.cookies.session_id ? loginRes.cookies.session_id[0].value : '';
  return {
    csrfToken: body ? body.csrf_token : '',
    sessionCookie: cookie,
    startedAtMs: Date.now(),
  };
}

function authHeaders(data) {
  return {
    'Content-Type': 'application/json',
    'X-CSRF-Token': data.csrfToken,
    Cookie: `session_id=${data.sessionCookie}`,
  };
}

function isTerminalFailure(status) {
  return (
    status === 'failed' ||
    status === 'partial' ||
    status === 'partially_reverted' ||
    status === 'uncertain'
  );
}

function waitUntilOffset(startedAtMs, offsetMs) {
  if (!startedAtMs) {
    return;
  }
  const waitMs = startedAtMs + offsetMs - Date.now();
  if (waitMs > 0) {
    sleep(waitMs / 1000);
  }
}

function enqueueRequest(requestID, headers, scenarioTag, stageTag) {
  const enqueuedAtMs = Date.now();
  const metricTags = { scenario_type: scenarioTag, load_stage: stageTag };
  // Respons 202 Accepted tidak dihitung sebagai sukses eksekusi migrasi;
  // hanya status terminal 'success' dari pollJobUntilDone yang dihitung selesai.
  const enqRes = http.post(`${BASE_URL}/api/v1/requests/${requestID}/enqueue`, null, {
    headers,
    tags: { endpoint: 'enqueue', scenario_type: scenarioTag, load_stage: stageTag },
  });
  const accepted = check(enqRes, {
    'POST /enqueue 202 Accepted': (r) => r.status === 202,
  });
  if (!accepted) {
    jobsFailedTotal.add(1, metricTags);
    jobSuccessRate.add(false, metricTags);
    return null;
  }

  const payload = enqRes.json();
  if (!payload || !payload.job_id) {
    jobsFailedTotal.add(1, metricTags);
    jobSuccessRate.add(false, metricTags);
    return null;
  }
  return { jobId: payload.job_id, enqueuedAtMs };
}

function waitUntilRunningOrTerminal(jobID, headers, scenarioTag, stageTag) {
  const deadline = Date.now() + POLL_TIMEOUT_SEC * 1000;
  while (Date.now() < deadline) {
    const res = http.get(`${BASE_URL}/api/v1/jobs/${jobID}`, {
      headers,
      tags: { endpoint: 'job_status', scenario_type: scenarioTag, load_stage: stageTag },
    });
    if (res.status === 200) {
      const view = res.json();
      const status = view.request_status || (view.execution ? view.execution.status : '');
      if (status === 'running' || status === 'success' || isTerminalFailure(status)) {
        return;
      }
    }
    sleep(0.05);
  }
}

function pollJobUntilDone(jobID, enqueuedAtMs, headers, scenarioTag, stageTag) {
  const deadline = Date.now() + POLL_TIMEOUT_SEC * 1000;
  let recordedQueueWait = false;
  const metricTags = { scenario_type: scenarioTag, load_stage: stageTag };

  while (Date.now() < deadline) {
    const res = http.get(`${BASE_URL}/api/v1/jobs/${jobID}`, {
      headers,
      tags: { endpoint: 'job_status', scenario_type: scenarioTag, load_stage: stageTag },
    });
    if (res.status !== 200) {
      sleep(0.5);
      continue;
    }
    const view = res.json();
    const status = view.request_status || (view.execution ? view.execution.status : '');

    if (!recordedQueueWait && view.execution && view.execution.started_at) {
      const startedMs = Date.parse(view.execution.started_at);
      if (!isNaN(startedMs) && startedMs >= enqueuedAtMs) {
        queueWaitMS.add(startedMs - enqueuedAtMs, metricTags);
        recordedQueueWait = true;
      }
    }

    if (status === 'success') {
      jobCompletionDurationMS.add(Date.now() - enqueuedAtMs, metricTags);
      jobsCompletedTotal.add(1, metricTags);
      jobSuccessRate.add(true, metricTags);
      return true;
    }
    if (isTerminalFailure(status)) {
      jobCompletionDurationMS.add(Date.now() - enqueuedAtMs, metricTags);
      jobsFailedTotal.add(1, metricTags);
      jobSuccessRate.add(false, metricTags);
      return false;
    }
    sleep(0.25);
  }

  jobsFailedTotal.add(1, metricTags);
  jobSuccessRate.add(false, metricTags);
  return false;
}

export function runReadTraffic(data) {
  const headers = authHeaders(data);

  const listRes = http.get(`${BASE_URL}/api/v1/databases`, {
    headers,
    tags: { endpoint: 'list_databases' },
  });
  check(listRes, { 'GET /databases 200': (r) => r.status === 200 });

  const targetDB = __VU % 2 === 0 && DB2_ID ? DB2_ID : DB1_ID;
  if (targetDB) {
    const histRes = http.get(`${BASE_URL}/api/v1/databases/${targetDB}/history?hide_rollback=false`, {
      headers,
      tags: { endpoint: 'history' },
    });
    check(histRes, { 'GET /history 200': (r) => r.status === 200 });

    const schemaRes = http.get(`${BASE_URL}/api/v1/databases/${targetDB}/schema`, {
      headers,
      tags: { endpoint: 'schema' },
    });
    check(schemaRes, { 'GET /schema 200': (r) => r.status === 200 });
  }

  sleep(0.5);
}

export function runSameDBMigrationJobs(data) {
  const headers = authHeaders(data);
  for (let i = 0; i < SAME_DB_REQUEST_IDS.length; i += 2) {
    const { offsetMs, stage } = scheduledStageWindow(i, SAME_DB_REQUEST_IDS.length);
    waitUntilOffset(data.startedAtMs, offsetMs);

    const firstJob = enqueueRequest(SAME_DB_REQUEST_IDS[i], headers, 'same_db', stage);
    if (firstJob) {
      // Tunggu hingga worker memulai job pertama sebelum mengirim job pasangannya pada DB sama
      // agar lock-contention tetap terjadi tanpa membalik urutan versi linear.
      waitUntilRunningOrTerminal(firstJob.jobId, headers, 'same_db', stage);
    }

    let secondJob = null;
    if (i + 1 < SAME_DB_REQUEST_IDS.length) {
      secondJob = enqueueRequest(SAME_DB_REQUEST_IDS[i + 1], headers, 'same_db', stage);
    }

    if (firstJob) {
      pollJobUntilDone(firstJob.jobId, firstJob.enqueuedAtMs, headers, 'same_db', stage);
    }
    if (secondJob) {
      pollJobUntilDone(secondJob.jobId, secondJob.enqueuedAtMs, headers, 'same_db', stage);
    }
  }
}

export function runDiffDBMigrationJobs(data) {
  const headers = authHeaders(data);
  for (let i = 0; i < DIFF_DB_REQUEST_IDS.length; i++) {
    const { offsetMs, stage } = scheduledStageWindow(i, DIFF_DB_REQUEST_IDS.length);
    waitUntilOffset(data.startedAtMs, offsetMs);

    const job = enqueueRequest(DIFF_DB_REQUEST_IDS[i], headers, 'diff_db', stage);
    if (job) {
      pollJobUntilDone(job.jobId, job.enqueuedAtMs, headers, 'diff_db', stage);
    }
  }
}

export function handleSummary(data) {
  return {
    stdout: textSummary(data, { indent: ' ', enableColors: false }),
    'tests/k6/summary.json': JSON.stringify(data, null, 2),
  };
}
