package telemetry

import (
	"context"
	"database/sql"
	"net/http"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// PoolStatsSource mengambil statistik sql.DBStats dengan label role berkardinalitas rendah.
type PoolStatsSource struct {
	Role  string
	Stats func() sql.DBStats
}

// Metrics menyimpan seluruh metrik Prometheus dengan label berkardinalitas rendah.
type Metrics struct {
	Registry *prometheus.Registry

	JobsTotal            *prometheus.CounterVec
	JobsInflight         *prometheus.GaugeVec
	JobRetriesTotal      *prometheus.CounterVec
	QueueWaitSeconds     prometheus.Histogram
	LockWaitSeconds      prometheus.Histogram
	ExecutionDurationSec *prometheus.HistogramVec
	BatchesProcessed     *prometheus.CounterVec
	RowsProcessed        prometheus.Counter

	poolMu           sync.RWMutex
	poolSources      []PoolStatsSource
	inflightSource   func(ctx context.Context) (waiting, running float64, ok bool)
	inflightMu       sync.Mutex
	inflightCachedAt time.Time
	inflightWaiting  float64
	inflightRunning  float64
	inflightHasCache bool
}

const inflightCacheTTL = 2 * time.Second

var (
	metricsOnce   sync.Once
	globalMetrics *Metrics
)

// GlobalMetrics mengembalikan registry Prometheus tingkat proses.
func GlobalMetrics() *Metrics {
	metricsOnce.Do(func() {
		globalMetrics = NewMetrics()
	})
	return globalMetrics
}

// NewMetrics membuat instance Metrics baru dengan registry terisolasi.
func NewMetrics() *Metrics {
	reg := prometheus.NewRegistry()

	m := &Metrics{
		Registry: reg,
		JobsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "migration_jobs_total",
			Help: "Total migration jobs transitioned by status, action_type, and mode.",
		}, []string{"status", "action_type", "mode"}),
		JobsInflight: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "migration_jobs_inflight",
			Help: "Current number of migration jobs waiting in queue or running in worker.",
		}, []string{"status"}),
		JobRetriesTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "migration_job_retries_total",
			Help: "Total migration job retries by action_type.",
		}, []string{"action_type"}),
		QueueWaitSeconds: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "migration_queue_wait_seconds",
			Help:    "Time elapsed from job enqueue until worker starts processing.",
			Buckets: []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60},
		}),
		LockWaitSeconds: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "migration_lock_wait_seconds",
			Help:    "Time spent waiting to acquire the per-database distributed lock.",
			Buckets: []float64{0.005, 0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30},
		}),
		ExecutionDurationSec: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "migration_execution_duration_seconds",
			Help:    "End-to-end migration execution duration by mode and terminal status.",
			Buckets: []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120},
		}, []string{"mode", "status"}),
		BatchesProcessed: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "migration_batches_processed_total",
			Help: "Total migration batches processed by status (committed, reverted, failed).",
		}, []string{"status"}),
		RowsProcessed: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "migration_rows_processed_total",
			Help: "Total target database rows affected across migration executions.",
		}),
	}

	reg.MustRegister(
		m.JobsTotal,
		m.JobsInflight,
		m.JobRetriesTotal,
		m.QueueWaitSeconds,
		m.LockWaitSeconds,
		m.ExecutionDurationSec,
		m.BatchesProcessed,
		m.RowsProcessed,
		&dbPoolCollector{
			metrics: m,
			connectionsDesc: prometheus.NewDesc(
				"db_pool_connections",
				"Current database connection pool connections by db_role and state (active, idle).",
				[]string{"db_role", "state"}, nil,
			),
			waitCountDesc: prometheus.NewDesc(
				"db_pool_wait_count_total",
				"Total number of connections waited for from the database pool by db_role.",
				[]string{"db_role"}, nil,
			),
			waitDurationDesc: prometheus.NewDesc(
				"db_pool_wait_duration_seconds_total",
				"Total time blocked waiting for a database pool connection in seconds by db_role.",
				[]string{"db_role"}, nil,
			),
		},
	)

	// Inisialisasi nilai awal gauge agar dashboard langsung tampil saat boot.
	m.JobsInflight.WithLabelValues("waiting").Set(0)
	m.JobsInflight.WithLabelValues("running").Set(0)

	return m
}

// RegisterDBPool mendaftarkan penyedia statistik pool DB dengan label role terbatas.
func (m *Metrics) RegisterDBPool(role string, statsFn func() sql.DBStats) {
	if role == "" || statsFn == nil {
		return
	}
	m.poolMu.Lock()
	m.poolSources = append(m.poolSources, PoolStatsSource{Role: role, Stats: statsFn})
	m.poolMu.Unlock()
	m.inflightMu.Lock()
	m.inflightHasCache = false
	m.inflightMu.Unlock()
}

// RegisterJobInflightDB mengikat gauge migration_jobs_inflight ke hitungan langsung Platform DB.
func (m *Metrics) RegisterJobInflightDB(db *sql.DB) {
	if db == nil {
		return
	}
	m.poolMu.Lock()
	m.inflightSource = func(ctx context.Context) (float64, float64, bool) {
		qCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		rows, err := db.QueryContext(qCtx, `
			SELECT status, COUNT(*)
			FROM migration_requests
			WHERE status IN ('pending_queue', 'running')
			GROUP BY status
		`)
		if err != nil {
			return 0, 0, false
		}
		defer rows.Close()
		var waiting, running float64
		for rows.Next() {
			var st string
			var cnt int64
			if err := rows.Scan(&st, &cnt); err == nil {
				switch st {
				case "pending_queue":
					waiting = float64(cnt)
				case "running":
					running = float64(cnt)
				}
			}
		}
		return waiting, running, rows.Err() == nil
	}
	m.poolMu.Unlock()
	m.inflightMu.Lock()
	m.inflightHasCache = false
	m.inflightMu.Unlock()
}

// RecordJobEnqueued menambah counter pending_queue (serta gauge lokal jika tanpa DB).
func (m *Metrics) RecordJobEnqueued(actionType, mode string) {
	m.JobsTotal.WithLabelValues("pending_queue", sanitizeLabel(actionType), sanitizeLabel(mode)).Inc()
	m.poolMu.RLock()
	hasDB := m.inflightSource != nil
	m.poolMu.RUnlock()
	if !hasDB {
		m.JobsInflight.WithLabelValues("waiting").Inc()
	}
}

// RecordJobStarted mencatat counter job berjalan dan waktu tunggu antrean.
func (m *Metrics) RecordJobStarted(actionType, mode string, queueWait time.Duration) {
	m.JobsTotal.WithLabelValues("running", sanitizeLabel(actionType), sanitizeLabel(mode)).Inc()
	m.poolMu.RLock()
	hasDB := m.inflightSource != nil
	m.poolMu.RUnlock()
	if !hasDB {
		m.JobsInflight.WithLabelValues("running").Inc()
	}
	if queueWait >= 0 {
		m.QueueWaitSeconds.Observe(queueWait.Seconds())
	}
}

// RecordJobFinished mencatat status akhir dan durasi eksekusi job.
func (m *Metrics) RecordJobFinished(actionType, mode, status string, duration time.Duration) {
	m.poolMu.RLock()
	hasDB := m.inflightSource != nil
	m.poolMu.RUnlock()
	if !hasDB {
		m.JobsInflight.WithLabelValues("running").Dec()
	}
	m.JobsTotal.WithLabelValues(sanitizeLabel(status), sanitizeLabel(actionType), sanitizeLabel(mode)).Inc()
	if duration >= 0 {
		m.ExecutionDurationSec.WithLabelValues(sanitizeLabel(mode), sanitizeLabel(status)).Observe(duration.Seconds())
	}
}

// RecordJobRetry menambah counter retry untuk kegagalan sementara.
func (m *Metrics) RecordJobRetry(actionType string) {
	m.JobRetriesTotal.WithLabelValues(sanitizeLabel(actionType)).Inc()
}

// ObserveLockWait mencatat durasi tunggu saat mengambil distributed lock DB.
func (m *Metrics) ObserveLockWait(d time.Duration) {
	if d >= 0 {
		m.LockWaitSeconds.Observe(d.Seconds())
	}
}

// RecordBatchProcessed menambah counter batch dan jumlah baris terdampak.
func (m *Metrics) RecordBatchProcessed(status string, rowsAffected int64) {
	m.BatchesProcessed.WithLabelValues(sanitizeLabel(status)).Inc()
	if rowsAffected > 0 {
		m.RowsProcessed.Add(float64(rowsAffected))
	}
}

// Handler mengembalikan HTTP handler Prometheus dan menyegarkan gauge inflight dari DB.
func (m *Metrics) Handler() http.Handler {
	promHandler := promhttp.HandlerFor(m.Registry, promhttp.HandlerOpts{})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.refreshInflightGauges(r.Context())
		promHandler.ServeHTTP(w, r)
	})
}

func (m *Metrics) refreshInflightGauges(ctx context.Context) {
	m.poolMu.RLock()
	src := m.inflightSource
	m.poolMu.RUnlock()
	if src == nil {
		return
	}
	m.inflightMu.Lock()
	defer m.inflightMu.Unlock()
	if m.inflightHasCache && time.Since(m.inflightCachedAt) < inflightCacheTTL {
		m.JobsInflight.WithLabelValues("waiting").Set(m.inflightWaiting)
		m.JobsInflight.WithLabelValues("running").Set(m.inflightRunning)
		return
	}
	if waiting, running, ok := src(ctx); ok {
		m.inflightWaiting = waiting
		m.inflightRunning = running
		m.inflightCachedAt = time.Now()
		m.inflightHasCache = true
		m.JobsInflight.WithLabelValues("waiting").Set(waiting)
		m.JobsInflight.WithLabelValues("running").Set(running)
	}
}

// StartMetricsServer menjalankan server HTTP /metrics (untuk proses worker).
func StartMetricsServer(ctx context.Context, addr string, m *Metrics) *http.Server {
	if m == nil {
		m = GlobalMetrics()
	}
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", m.Handler())
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		_ = srv.ListenAndServe()
	}()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	return srv
}

func sanitizeLabel(v string) string {
	switch v {
	case "pending_queue", "waiting", "running", "success", "failed", "partial", "partially_reverted", "uncertain",
		"up", "rollback", "forward", "resume_batch", "revert_batch",
		"normal", "batch", "committed", "reverted":
		return v
	default:
		return "other"
	}
}

type dbPoolCollector struct {
	metrics          *Metrics
	connectionsDesc  *prometheus.Desc
	waitCountDesc    *prometheus.Desc
	waitDurationDesc *prometheus.Desc
}

func (c *dbPoolCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.connectionsDesc
	ch <- c.waitCountDesc
	ch <- c.waitDurationDesc
}

func (c *dbPoolCollector) Collect(ch chan<- prometheus.Metric) {
	c.metrics.poolMu.RLock()
	sources := append([]PoolStatsSource(nil), c.metrics.poolSources...)
	c.metrics.poolMu.RUnlock()

	type agg struct {
		inUse        int
		idle         int
		waitCount    int64
		waitDuration time.Duration
	}
	byRole := make(map[string]*agg)
	for _, src := range sources {
		st := src.Stats()
		a := byRole[src.Role]
		if a == nil {
			a = &agg{}
			byRole[src.Role] = a
		}
		a.inUse += st.InUse
		a.idle += st.Idle
		a.waitCount += st.WaitCount
		a.waitDuration += st.WaitDuration
	}

	for role, a := range byRole {
		ch <- prometheus.MustNewConstMetric(c.connectionsDesc, prometheus.GaugeValue, float64(a.inUse), role, "active")
		ch <- prometheus.MustNewConstMetric(c.connectionsDesc, prometheus.GaugeValue, float64(a.idle), role, "idle")
		ch <- prometheus.MustNewConstMetric(c.waitCountDesc, prometheus.CounterValue, float64(a.waitCount), role)
		ch <- prometheus.MustNewConstMetric(c.waitDurationDesc, prometheus.CounterValue, a.waitDuration.Seconds(), role)
	}
}
