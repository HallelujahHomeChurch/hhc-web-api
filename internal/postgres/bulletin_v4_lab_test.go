package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletinwatermark"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/migrations"
	"github.com/google/uuid"
)

// This opt-in lab shares disposable SQL state across twenty finite ACA replicas.
// Queue and Asset adapters are synthetic; production SQL/renderer/worker logic is real.
func TestBulletinV4DistributedLab(t *testing.T) {
	dsn := os.Getenv("V4_LAB_DSN")
	if dsn == "" {
		t.Skip("finite isolated V4 lab only")
	}
	if !validV4LabDSN(dsn) {
		t.Fatal("refusing a database outside the disposable V4 lab")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(3)
	for db.PingContext(ctx) != nil {
		select {
		case <-ctx.Done():
			t.Fatal("lab database unavailable")
		case <-time.After(time.Second):
		}
	}
	source, err := os.ReadFile("/v4/1739-sanitized.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if err := prepareV4Lab(ctx, db, source); err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	workerID := uuid.NewString()
	if _, err := db.ExecContext(ctx, `INSERT INTO v4_lab.workers(id) VALUES($1)`, workerID); err != nil {
		t.Fatal(err)
	}
	for {
		var n int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM v4_lab.workers`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n > 20 {
			t.Fatal("unexpected additional lab replica")
		}
		if n == 20 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("twenty-replica barrier timed out")
		case <-time.After(100 * time.Millisecond):
		}
	}
	runner, err := bulletinwatermark.NewRunner("/hhc-web-bulletin-watermark", filepath.Join(t.TempDir(), "renderer"))
	if err != nil {
		t.Fatal(err)
	}
	defer runner.Close()
	assets := &v4LabAssets{concurrentBulletinAssets: &concurrentBulletinAssets{source: source, missingSource: strings.Repeat("c", 32)}, db: db}
	worker := bulletinwatermark.NewDownloadWorker(New(db), &v4LabRenderer{Runner: runner, db: db}, assets, "v4-synthetic-only")
	queue := &v4LabQueue{db: db}
	for {
		var pending int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM v4_lab.queue`).Scan(&pending); err != nil {
			t.Fatal(err)
		}
		if pending == 0 {
			break
		}
		processed, err := bulletinwatermark.ProcessOne(ctx, queue, worker)
		if err != nil {
			t.Fatal(err)
		}
		if !processed {
			select {
			case <-ctx.Done():
				t.Fatal("lab work did not settle")
			case <-time.After(100 * time.Millisecond):
			}
		}
	}
	var ready, failed, stuck, retryAttempts, retries, workers, normalAttempts, peak, active int
	var normalSeconds, failureSeconds, lastSeconds float64
	var started time.Time
	err = db.QueryRowContext(ctx, `SELECT
	 count(*) FILTER(WHERE j.status='ready'),count(*) FILTER(WHERE j.status='failed' AND j.error_code='source_unavailable'),
	 count(*) FILTER(WHERE j.status NOT IN ('ready','failed')),
	 max(j.attempts) FILTER(WHERE c.scenario='retry'),
	 COALESCE(max(extract(epoch FROM(j.updated_at-s.started_at))) FILTER(WHERE c.scenario='normal'),0),
	 COALESCE(max(extract(epoch FROM(j.updated_at-s.started_at))) FILTER(WHERE c.scenario='404'),0),
	 COALESCE(max(extract(epoch FROM(j.updated_at-s.started_at))),0),
	 (SELECT sum(retries) FROM v4_lab.results),(SELECT count(*) FROM v4_lab.workers),s.started_at,
	 max(j.attempts) FILTER(WHERE c.scenario='normal'),s.peak,s.active
	 FROM v4_lab.cohort c JOIN hhc_web.bulletin_download_job j ON j.id=c.id CROSS JOIN v4_lab.state s GROUP BY s.started_at,s.peak,s.active`).Scan(&ready, &failed, &stuck, &retryAttempts, &normalSeconds, &failureSeconds, &lastSeconds, &retries, &workers, &started, &normalAttempts, &peak, &active)
	if err != nil {
		t.Fatal(err)
	}
	result := map[string]any{"event": "v4_lab_result", "ready": ready, "failed404": failed, "stuck": stuck, "retryAttempts": retryAttempts, "retries": retries, "workers": workers, "normalLastSeconds": normalSeconds, "failureSeconds": failureSeconds, "allLastSeconds": lastSeconds, "cohortCreatedAt": started.UTC().Format(time.RFC3339Nano), "queueAssetTransport": "synthetic SQL adapters", "renderer": "production immutable Python/TrustMark helper"}
	result["normalAttemptsMax"], result["renderPeak"], result["activeRenderers"] = normalAttempts, peak, active
	for key, paths := range map[string][]string{
		"memoryLimitBytes": {"/sys/fs/cgroup/memory.max", "/sys/fs/cgroup/memory/memory.limit_in_bytes"},
		"memoryPeakBytes":  {"/sys/fs/cgroup/memory.peak", "/sys/fs/cgroup/memory/memory.max_usage_in_bytes"},
		"cpuQuota":         {"/sys/fs/cgroup/cpu.max", "/sys/fs/cgroup/cpu/cpu.cfs_quota_us"},
		"cpuPeriod":        {"/sys/fs/cgroup/cpu/cpu.cfs_period_us"},
	} {
		for _, path := range paths {
			if value, err := os.ReadFile(path); err == nil {
				result[key] = strings.TrimSpace(string(value))
				break
			}
		}
	}
	if result["memoryLimitBytes"] != "4294967296" {
		t.Fatal("actual lab memory limit is not4Gi")
	}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Println(string(data))
	if ready != 21 || failed != 1 || stuck != 0 || retryAttempts != 2 || retries != 1 || workers != 20 || normalAttempts != 1 || peak != 20 || active != 0 || normalSeconds > 120 || lastSeconds > 120 || failureSeconds > 60 {
		t.Fatal("V4 acceptance gate failed; retain all results")
	}
}

func validV4LabDSN(dsn string) bool {
	u, err := url.Parse(dsn)
	return err == nil && u.Scheme == "postgres" && u.User != nil && u.User.Username() == "v4_lab" && u.Path == "/v4_lab" && (u.Hostname() == "localhost" || regexp.MustCompile(`^hhc-v4-db-[0-9]+-[0-9]+$`).MatchString(u.Hostname()))
}

func TestV4LabRefusesProductionDatabase(t *testing.T) {
	for _, dsn := range []string{"postgres://hhc_web:synthetic@172.16.68.4/hhc_web", "postgres://v4_lab:synthetic@172.16.68.4/v4_lab", "postgres://v4_lab:synthetic@localhost/hhc_web", "postgres://v4_lab:synthetic@hhc-v4-db-123.evil.example/v4_lab", "postgres://v4_lab:synthetic@hhc-v4-db-123-1/v4_lab"} {
		if validV4LabDSN(dsn) != (dsn == "postgres://v4_lab:synthetic@hhc-v4-db-123-1/v4_lab") {
			t.Fatal("invalid lab database boundary")
		}
	}
}

func TestV4LabQueueFencesAndHonorsBackoff(t *testing.T) {
	db := accessDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := db.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS v4_lab;
	DROP TABLE IF EXISTS v4_lab.queue; DROP TABLE IF EXISTS v4_lab.results;
	CREATE TABLE v4_lab.queue(id uuid PRIMARY KEY,visible_at timestamptz NOT NULL DEFAULT now(),receipt uuid,attempts bigint NOT NULL DEFAULT 0);
	CREATE TABLE v4_lab.results(id uuid PRIMARY KEY,retries integer NOT NULL DEFAULT 0);`)
	if err != nil {
		t.Fatal(err)
	}
	id := uuid.NewString()
	if _, err = db.ExecContext(ctx, `INSERT INTO v4_lab.queue(id) VALUES($1)`, id); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, `INSERT INTO v4_lab.results(id) VALUES($1)`, id); err != nil {
		t.Fatal(err)
	}
	q := &v4LabQueue{db: db}
	m, found, err := q.Dequeue(ctx)
	if err != nil || !found || m.ID != id || m.DequeueCount != 1 {
		t.Fatalf("initial delivery: found=%v err=%v", found, err)
	}
	if _, found, err = q.Dequeue(ctx); err != nil || found {
		t.Fatal("in-flight message delivered twice")
	}
	stale := m
	stale.PopReceipt = uuid.NewString()
	if q.Ack(ctx, stale) == nil || q.Retry(ctx, stale, time.Second) == nil {
		t.Fatal("stale queue fence accepted")
	}
	if err = q.Retry(ctx, m, 250*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if _, found, err = q.Dequeue(ctx); err != nil || found {
		t.Fatal("backoff ignored")
	}
	time.Sleep(300 * time.Millisecond)
	next, found, err := q.Dequeue(ctx)
	if err != nil || !found || next.DequeueCount != 2 || next.PopReceipt == m.PopReceipt {
		t.Fatal("redelivery did not increment/fence attempt")
	}
	if q.Ack(ctx, m) == nil {
		t.Fatal("old receipt acknowledged newer delivery")
	}
	if err = q.Ack(ctx, next); err != nil {
		t.Fatal(err)
	}
	if _, found, err = q.Dequeue(ctx); err != nil || found {
		t.Fatal("acknowledged message remained")
	}
	var retries int
	if err = db.QueryRowContext(ctx, `SELECT retries FROM v4_lab.results WHERE id=$1`, id).Scan(&retries); err != nil || retries != 1 {
		t.Fatal("retry accounting incorrect")
	}
}

func prepareV4Lab(ctx context.Context, db *sql.DB, source []byte) (err error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	// Blocking advisory-lock waiters keep snapshots that deadlock concurrent index builds.
	for {
		var acquired bool
		if err = conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock(721732019260799)`).Scan(&acquired); err != nil {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
			return err
		}
		if acquired {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer stop()
		_, unlockErr := conn.ExecContext(cleanup, `SELECT pg_advisory_unlock(721732019260799)`)
		if unlockErr != nil {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
		err = errors.Join(err, unlockErr)
	}()
	if err = migrations.Run(ctx, db); err != nil {
		return err
	}
	if _, err = conn.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS v4_lab;
	CREATE TABLE IF NOT EXISTS v4_lab.state(started_at timestamptz NOT NULL,flaky boolean NOT NULL,active integer NOT NULL DEFAULT 0,peak integer NOT NULL DEFAULT 0);
	CREATE TABLE IF NOT EXISTS v4_lab.workers(id uuid PRIMARY KEY);
	CREATE TABLE IF NOT EXISTS v4_lab.cohort(id uuid PRIMARY KEY,scenario text NOT NULL);
	CREATE TABLE IF NOT EXISTS v4_lab.queue(id uuid PRIMARY KEY,visible_at timestamptz NOT NULL DEFAULT now(),receipt uuid,attempts bigint NOT NULL DEFAULT 0);
	CREATE TABLE IF NOT EXISTS v4_lab.results(id uuid PRIMARY KEY,retries integer NOT NULL DEFAULT 0);`); err != nil {
		return err
	}
	var exists bool
	if err = conn.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM v4_lab.state)`).Scan(&exists); err != nil || exists {
		return err
	}
	issueID := uuid.NewString()
	now := time.Now().UTC()
	if _, err = conn.ExecContext(ctx, `INSERT INTO hhc_web.bulletin_issue(id,issue_number,issue_date,status,version,idempotency_key,created_by,updated_by,created_at,updated_at) VALUES($1,900002,'2099-01-05','published',1,$2,'v4-lab','v4-lab',$3,$3)`, issueID, uuid.NewString(), now); err != nil {
		return err
	}
	sum := sha256.Sum256(source)
	for i := 0; i < 22; i++ {
		asset, scenario := testWatermarkAssetID, "normal"
		if i == 0 {
			asset, scenario = strings.Repeat("c", 32), "404"
		}
		if i == 1 {
			asset, scenario = strings.Repeat("d", 32), "retry"
		}
		code, err := bulletinwatermark.NewCode()
		if err != nil {
			return err
		}
		fingerprint, err := bulletinwatermark.NewFingerprint()
		if err != nil {
			return err
		}
		job, err := New(db).CreateDownloadJob(ctx, bulletinwatermark.DownloadJobRequest{ID: uuid.NewString(), IdempotencyKey: uuid.NewString(), AccountID: uuid.NewString(), IssueDate: "2099-01-05", RendererVersion: "trustmark-pdf-v2", TraceCode: code, FingerprintPayload: fingerprint, SourceSHA256: hex.EncodeToString(sum[:]), Source: bulletinwatermark.Source{IssueID: issueID, Series: "general", Locale: "zh-Hant", Revision: 1, AssetID: asset, Filename: "synthetic.pdf"}, Now: now})
		if err != nil {
			return err
		}
		if _, err = conn.ExecContext(ctx, `INSERT INTO v4_lab.cohort VALUES($1,$2);`, job.ID, scenario); err != nil {
			return err
		}
		if _, err = conn.ExecContext(ctx, `INSERT INTO v4_lab.queue(id) VALUES($1);`, job.ID); err != nil {
			return err
		}
		if _, err = conn.ExecContext(ctx, `INSERT INTO v4_lab.results(id) VALUES($1);`, job.ID); err != nil {
			return err
		}
	}
	_, err = conn.ExecContext(ctx, `INSERT INTO v4_lab.state(started_at,flaky) VALUES(clock_timestamp(),true)`)
	return err
}

func TestV4LabConcurrentBootstrap(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" || os.Getenv("HHC_WEB_ALLOW_DESTRUCTIVE_TEST") != "1" {
		t.Skip("disposable local PostgreSQL admin only")
	}
	u, err := url.Parse(dsn)
	if err != nil || (u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1") || u.Path != "/postgres" {
		t.Fatal("refusing non-local bootstrap test admin")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Close() })
	name := "hhc_v4_bootstrap_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err = admin.ExecContext(ctx, `CREATE DATABASE "`+name+`"`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if _, err := admin.ExecContext(cleanup, `DROP DATABASE "`+name+`" WITH (FORCE)`); err != nil {
			t.Error("drop owned bootstrap test database:", err)
		}
	})
	u.Path = "/" + name
	source, err := os.ReadFile("../onlinebulletins/testdata/1739-sanitized.pdf")
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	errs := make(chan error, 20)
	var group sync.WaitGroup
	for range 20 {
		group.Go(func() {
			db, err := sql.Open("pgx", u.String())
			if err != nil {
				errs <- err
				return
			}
			defer db.Close()
			db.SetMaxOpenConns(3)
			<-start
			errs <- prepareV4Lab(ctx, db, source)
		})
	}
	close(start)
	group.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("concurrent bootstrap: %v", err)
		}
	}
	if t.Failed() {
		return
	}
	db, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var jobs, indexes int
	if err = db.QueryRowContext(ctx, `SELECT count(*) FROM v4_lab.cohort`).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRowContext(ctx, `SELECT count(*) FROM pg_index WHERE indexrelid=to_regclass('hhc_web.member_recording_comment_direct_children_idx') AND indisvalid`).Scan(&indexes); err != nil {
		t.Fatal(err)
	}
	if jobs != 22 || indexes != 1 {
		t.Fatalf("jobs=%d validCommentIndexes=%d", jobs, indexes)
	}
	t.Log("20 simultaneous bootstrap callers; exactly22 jobs and valid concurrent index")
}

type v4LabQueue struct{ db *sql.DB }

type v4LabRenderer struct {
	*bulletinwatermark.Runner
	db *sql.DB
}

func (r *v4LabRenderer) RenderScoped(ctx context.Context, source io.Reader, payload string, scope bulletinwatermark.PDFScopeMetadata, progress func(int)) (out *bulletinwatermark.Output, err error) {
	if _, err = r.db.ExecContext(ctx, `UPDATE v4_lab.state SET active=active+1,peak=greatest(peak,active+1)`); err != nil {
		return nil, err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		_, e := r.db.ExecContext(cleanup, `UPDATE v4_lab.state SET active=active-1`)
		err = errors.Join(err, e)
		if err != nil && out != nil {
			out.Close()
			out = nil
		}
	}()
	return r.Runner.RenderScoped(ctx, source, payload, scope, progress)
}

func (q *v4LabQueue) Dequeue(ctx context.Context) (bulletinwatermark.LeasedMessage, bool, error) {
	m := bulletinwatermark.LeasedMessage{WorkMessage: bulletinwatermark.WorkMessage{Kind: "download"}, PopReceipt: uuid.NewString()}
	err := q.db.QueryRowContext(ctx, `UPDATE v4_lab.queue SET receipt=$1,visible_at=clock_timestamp()+interval '300 seconds',attempts=attempts+1 WHERE id=(SELECT id FROM v4_lab.queue WHERE visible_at<=clock_timestamp() ORDER BY visible_at,id FOR UPDATE SKIP LOCKED LIMIT 1) RETURNING id::text,attempts`, m.PopReceipt).Scan(&m.ID, &m.DequeueCount)
	if errors.Is(err, sql.ErrNoRows) {
		return m, false, nil
	}
	m.MessageID = m.ID
	return m, err == nil, err
}
func (q *v4LabQueue) Ack(ctx context.Context, m bulletinwatermark.LeasedMessage) error {
	r, err := q.db.ExecContext(ctx, `DELETE FROM v4_lab.queue WHERE id=$1 AND receipt=$2`, m.ID, m.PopReceipt)
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err == nil && n != 1 {
		return errors.New("lab queue fence lost")
	}
	return err
}
func (q *v4LabQueue) Poison(context.Context, bulletinwatermark.LeasedMessage) error {
	return errors.New("unexpected poison message in lab")
}
func (q *v4LabQueue) Retry(ctx context.Context, m bulletinwatermark.LeasedMessage, delay time.Duration) error {
	tx, err := q.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	r, err := tx.ExecContext(ctx, `UPDATE v4_lab.queue SET visible_at=clock_timestamp()+$3*interval '1 second' WHERE id=$1 AND receipt=$2`, m.ID, m.PopReceipt, delay.Seconds())
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("lab retry fence lost")
	}
	if _, err = tx.ExecContext(ctx, `UPDATE v4_lab.results SET retries=retries+1 WHERE id=$1`, m.ID); err != nil {
		return err
	}
	return tx.Commit()
}

type v4LabAssets struct {
	*concurrentBulletinAssets
	db *sql.DB
}

func (a *v4LabAssets) AuthorizedDownload(ctx context.Context, method, id, r, etag string) (*http.Response, error) {
	if id == strings.Repeat("d", 32) {
		var changed bool
		err := a.db.QueryRowContext(ctx, `UPDATE v4_lab.state SET flaky=false WHERE flaky RETURNING true`).Scan(&changed)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if changed {
			return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader(""))}, nil
		}
	}
	return a.concurrentBulletinAssets.AuthorizedDownload(ctx, method, id, r, etag)
}
