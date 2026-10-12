package processing

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type workerMetric struct {
	online            bool
	age               float64
	reported          bool
	instances, active int64
	attemptAge        float64
}

var counterNames = map[string]string{
	"completed": "compute_jobs_completed_total", "failed": "compute_jobs_failed_total",
	"attempt_failed": "compute_attempt_failures_total", "retry": "compute_retry_attempts_total",
	"lease_expired": "compute_lease_expirations_total",
}

// ServeMetrics binds only the private monitoring network alias in Compose.
func (a *API) ServeMetrics(addr string) (*http.Server, error) {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", a.metrics)
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 64 * 1024}
	go server.Serve(listener)
	return server, nil
}
func (a *API) metrics(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	body, err := a.metricsText(ctx)
	if err != nil {
		http.Error(w, "queue metrics unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	fmt.Fprint(w, body)
}
func (a *API) metricsText(ctx context.Context) (string, error) {
	tx, err := a.Queue.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	workers := map[string]*workerMetric{}
	for id := range a.Hashes {
		workers[id] = &workerMetric{}
	}
	var b strings.Builder
	emit := func(name, labels string, value any) {
		fmt.Fprintf(&b, "%s{service=\"stash\"%s} %v\n", name, labels, value)
	}
	gauge := func(name string) { fmt.Fprintf(&b, "# TYPE %s gauge\n", name) }
	statuses := map[string]int64{"queued": 0, "running": 0, "done": 0, "failed": 0}
	rows, err := tx.Query(ctx, sql("metrics_jobs"))
	if err != nil {
		return "", err
	}
	for rows.Next() {
		var state string
		var count int64
		if err = rows.Scan(&state, &count); err != nil {
			rows.Close()
			return "", err
		}
		statuses[state] = count
	}
	if err = rows.Err(); err != nil {
		return "", err
	}
	rows.Close()
	gauge("compute_jobs")
	for _, state := range []string{"queued", "running", "done", "failed"} {
		emit("compute_jobs", fmt.Sprintf(",status=%q", state), statuses[state])
	}
	for _, item := range []struct{ query, name string }{{"metrics_wait", "compute_queue_oldest_wait_seconds"}, {"metrics_expired", "compute_expired_jobs"}} {
		var value float64
		if err = tx.QueryRow(ctx, sql(item.query)).Scan(&value); err != nil {
			return "", err
		}
		gauge(item.name)
		emit(item.name, "", value)
	}
	rows, err = tx.Query(ctx, sql("metrics_workers"), a.Queue.VisionModel, a.Queue.TextModel)
	if err != nil {
		return "", err
	}
	for rows.Next() {
		var id string
		var state workerMetric
		if err = rows.Scan(&id, &state.online, &state.age, &state.instances); err != nil {
			rows.Close()
			return "", err
		}
		if target := workers[id]; target != nil {
			state.reported = true
			*target = state
		}
	}
	if err = rows.Err(); err != nil {
		return "", err
	}
	rows.Close()
	rows, err = tx.Query(ctx, sql("metrics_active"))
	if err != nil {
		return "", err
	}
	for rows.Next() {
		var id string
		var count int64
		var age float64
		if err = rows.Scan(&id, &count, &age); err != nil {
			rows.Close()
			return "", err
		}
		if target := workers[id]; target != nil {
			target.active = count
			target.attemptAge = age
		}
	}
	if err = rows.Err(); err != nil {
		return "", err
	}
	rows.Close()
	for _, name := range []string{"compute_worker_online", "compute_worker_state", "compute_worker_last_report_age_seconds", "compute_worker_instances", "compute_worker_active_jobs", "compute_worker_attempt_age_seconds", "compute_fallback_allowed", "compute_model_info"} {
		gauge(name)
	}
	ids := make([]string, 0, len(workers))
	for id := range workers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	fallback := 1
	for _, id := range ids {
		v := workers[id]
		role := "fallback"
		for _, preferred := range a.Queue.Preferred {
			if id == preferred {
				role = "preferred"
				if v.online {
					fallback = 0
				}
				break
			}
		}
		labels := fmt.Sprintf(",worker_id=%q,worker_role=%q", id, role)
		online, state := 0, 0
		if v.online {
			online = 1
			state = 1
			if v.active > 0 {
				state = 2
			}
		}
		emit("compute_worker_online", labels, online)
		emit("compute_worker_state", labels, state)
		emit("compute_worker_instances", labels, v.instances)
		emit("compute_worker_active_jobs", labels, v.active)
		emit("compute_worker_attempt_age_seconds", labels, v.attemptAge)
		if v.reported {
			emit("compute_worker_last_report_age_seconds", labels, v.age)
		}
	}
	emit("compute_fallback_allowed", "", fallback)
	emit("compute_model_info", fmt.Sprintf(",model=%q,text_model=%q", a.Queue.VisionModel, a.Queue.TextModel), 1)
	counters := map[string]map[string]int64{}
	for event := range counterNames {
		counters[event] = map[string]int64{"other": 0}
		for id := range workers {
			counters[event][id] = 0
		}
	}
	rows, err = tx.Query(ctx, sql("metrics_counters"))
	if err != nil {
		return "", err
	}
	for rows.Next() {
		var id, event string
		var value int64
		if err = rows.Scan(&id, &event, &value); err != nil {
			rows.Close()
			return "", err
		}
		if workers[id] == nil {
			id = "other"
		}
		if counters[event] != nil {
			counters[event][id] += value
		}
	}
	if err = rows.Err(); err != nil {
		return "", err
	}
	rows.Close()
	for event, name := range counterNames {
		fmt.Fprintf(&b, "# TYPE %s counter\n", name)
		for id, value := range counters[event] {
			emit(name, fmt.Sprintf(",worker_id=%q", id), value)
		}
	}
	gauge("compute_job_processing_seconds")
	gauge("compute_job_turnaround_seconds")
	rows, err = tx.Query(ctx, sql("metrics_duration"))
	if err != nil {
		return "", err
	}
	for rows.Next() {
		var id string
		var p50, p95, t50, t95 float64
		if err = rows.Scan(&id, &p50, &p95, &t50, &t95); err != nil {
			rows.Close()
			return "", err
		}
		if workers[id] == nil {
			continue
		}
		for _, v := range []struct {
			name, q string
			value   float64
		}{{"compute_job_processing_seconds", "0.5", p50}, {"compute_job_processing_seconds", "0.95", p95}, {"compute_job_turnaround_seconds", "0.5", t50}, {"compute_job_turnaround_seconds", "0.95", t95}} {
			emit(v.name, fmt.Sprintf(",worker_id=%q,quantile=%q", id, v.q), v.value)
		}
	}
	if err = rows.Err(); err != nil {
		return "", err
	}
	rows.Close()
	if err = tx.Commit(ctx); err != nil {
		return "", err
	}
	return b.String(), nil
}
