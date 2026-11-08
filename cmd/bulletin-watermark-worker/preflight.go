package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
)

// The preflight (`--preflight`) is a manual Job execution run by the release
// before the persistent worker App is removed. It proves, without consuming
// work, that the Job's identity, secrets and database role can do everything
// a real execution needs. It logs exactly one line with a fixed class per
// check and never logs identifiers, URLs, payloads or error text.
const (
	classOK      = "ok"
	classSkipped = "skipped"
	preflightLog = "bulletin watermark preflight"
)

// tablePrivilege is one privilege the worker's repository methods require.
type tablePrivilege struct{ table, privilege string }

// workerPrivileges is derived from the internal/postgres methods reached by
// DownloadWorker (Claim/Complete/Fail/Release/Exhaust/UpdateDownloadJobProgress),
// InvestigationWorker (ClaimWatermarkInvestigationByID, Find*, ListWatermarkCandidates,
// CompleteWatermarkInvestigation, ExhaustWatermarkInvestigation) and ProcessOne.
// UPDATE on the receipt table is required because receipt reads use FOR SHARE
// row locks, which PostgreSQL authorizes with UPDATE.
var workerPrivileges = []tablePrivilege{
	{"hhc_web.bulletin_download_job", "SELECT"},
	{"hhc_web.bulletin_download_job", "UPDATE"},
	{"hhc_web.bulletin_watermark_receipt", "SELECT"},
	{"hhc_web.bulletin_watermark_receipt", "INSERT"},
	{"hhc_web.bulletin_watermark_receipt", "UPDATE"},
	{"hhc_web.bulletin_watermark_subject", "SELECT"},
	{"hhc_web.bulletin_watermark_investigation", "SELECT"},
	{"hhc_web.bulletin_watermark_investigation", "UPDATE"},
}

type preflightDatabase interface {
	PingContext(context.Context) error
	// MissingPrivileges counts required privileges current_user lacks.
	MissingPrivileges(context.Context, string, []tablePrivilege) (int, error)
}

type preflightQueue interface {
	Peek(context.Context) error
}

type preflightAssets interface {
	Get(context.Context, string) (assetclient.Asset, error)
}

type preflightDeps struct {
	configErr error
	db        preflightDatabase
	queue     preflightQueue
	assets    preflightAssets
	randomID  func() (string, error)
}

// runPreflight executes every check (later checks are skipped once config
// fails), logs one structured line and reports whether all passed.
func runPreflight(ctx context.Context, deps preflightDeps) bool {
	results := []any{}
	ok := true
	record := func(name, class string) {
		results = append(results, name, class)
		ok = ok && class == classOK
	}
	if deps.configErr != nil {
		record("config", "invalid")
		for _, name := range []string{"database", "privileges", "queue", "asset"} {
			record(name, classSkipped)
		}
	} else {
		record("config", classOK)
		database, privileges := checkDatabase(ctx, deps.db)
		record("database", database)
		record("privileges", privileges)
		record("queue", checkQueue(ctx, deps.queue))
		record("asset", checkAsset(ctx, deps.assets, deps.randomID))
	}
	attrs := append([]any{"ok", ok}, results...)
	if ok {
		slog.InfoContext(ctx, preflightLog, attrs...)
	} else {
		slog.ErrorContext(ctx, preflightLog, attrs...)
	}
	return ok
}

func checkDatabase(ctx context.Context, db preflightDatabase) (database, privileges string) {
	if db == nil {
		return "unavailable", classSkipped
	}
	pingCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		return "unreachable", classSkipped
	}
	missing, err := db.MissingPrivileges(pingCtx, "hhc_web", workerPrivileges)
	switch {
	case err != nil:
		return classOK, "query_failed"
	case missing > 0:
		return classOK, "missing"
	default:
		return classOK, classOK
	}
}

func checkQueue(ctx context.Context, queue preflightQueue) string {
	if queue == nil {
		return "unavailable"
	}
	peekCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	return classifyQueueError(queue.Peek(peekCtx))
}

func classifyQueueError(err error) string {
	if err == nil {
		return classOK
	}
	var credential *azidentity.AuthenticationFailedError
	if errors.As(err, &credential) {
		return "credential"
	}
	var response *azcore.ResponseError
	if errors.As(err, &response) {
		switch response.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			return "denied"
		case http.StatusNotFound:
			return "not_found"
		}
	}
	return "unavailable"
}

func checkAsset(ctx context.Context, assets preflightAssets, randomID func() (string, error)) string {
	if assets == nil {
		return "unavailable"
	}
	if randomID == nil {
		randomID = randomAssetID
	}
	id, err := randomID()
	if err != nil {
		return "unavailable"
	}
	getCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	_, err = assets.Get(getCtx, id)
	return classifyAssetProbe(err)
}

// classifyAssetProbe maps the probe of a random, non-existent asset. 404 is
// the only pass: Easy Auth accepted the token and Asset API admitted the
// caller before looking the asset up. 401/403 mean the identity is not
// accepted; a 2xx for a random ID is never expected.
func classifyAssetProbe(err error) string {
	switch {
	case errors.Is(err, assetclient.ErrNotFound):
		return classOK
	case err == nil:
		return "unexpected_found"
	case errors.Is(err, assetclient.ErrForbidden):
		return "denied"
	case errors.Is(err, assetclient.ErrUnavailable):
		return "unavailable"
	}
	message := err.Error()
	if strings.HasPrefix(message, "asset api status 401") || strings.HasPrefix(message, "asset api status 403") {
		return "denied"
	}
	return "unexpected_status"
}

func randomAssetID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

// sqlPreflightDatabase checks privileges inside a read-only transaction.
type sqlPreflightDatabase struct{ db *sql.DB }

func (d sqlPreflightDatabase) PingContext(ctx context.Context) error { return d.db.PingContext(ctx) }

func (d sqlPreflightDatabase) MissingPrivileges(ctx context.Context, schema string, privileges []tablePrivilege) (int, error) {
	tx, err := d.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	missing := 0
	var allowed bool
	if err = tx.QueryRowContext(ctx, `SELECT has_schema_privilege(current_user, $1, 'USAGE')`, schema).Scan(&allowed); err != nil {
		return 0, err
	}
	if !allowed {
		missing++
	}
	for _, p := range privileges {
		if err = tx.QueryRowContext(ctx, `SELECT has_table_privilege(current_user, $1, $2)`, p.table, p.privilege).Scan(&allowed); err != nil {
			return 0, err
		}
		if !allowed {
			missing++
		}
	}
	return missing, nil
}
