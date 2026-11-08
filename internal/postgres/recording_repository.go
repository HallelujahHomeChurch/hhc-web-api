package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/publication"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
	"github.com/jackc/pgx/v5/pgconn"
)

type RecordingRepository struct {
	db                  *sql.DB
	auditOutbox         *AuditOutbox
	transactionMu       sync.Mutex
	transactions        int
	subjectWaiters      int
	subjectTransactions int
	transactionWake     chan struct{}
}

func NewRecordingRepository(db *sql.DB) *RecordingRepository { return &RecordingRepository{db: db} }
func (r *RecordingRepository) WithAudit(outbox *AuditOutbox) *RecordingRepository {
	r.auditOutbox = outbox
	return r
}
func (r *RecordingRepository) enqueueAudit(ctx context.Context, tx *sql.Tx, action, id, actor string, now time.Time) error {
	provenance, ok := auditclient.ProvenanceFromContext(ctx)
	if r.auditOutbox == nil {
		if ok && provenance.ActorType == "service" {
			return recordings.ErrAuditUnavailable
		}
		return nil
	}
	if !ok || provenance.ActorID != actor || !auditclient.ValidActorRequest(actor, provenance.RequestID) {
		return recordings.ErrAuditUnavailable
	}
	event, err := auditclient.NewEventForPrincipal(action, id, provenance.ActorType, actor, provenance.RequestID, now, auditclient.Metadata{})
	if err == nil {
		_, err = r.auditOutbox.EnqueueTx(ctx, tx, event, now)
	}
	if err != nil {
		return recordings.ErrAuditUnavailable
	}
	return nil
}

const recordingColumns = `id::text,title_override,uploaded_at,expires_at,draft_expires_at,featured,hidden,status,COALESCE(primary_asset_version_id,''),version,COALESCE(package_id,''),COALESCE(ready_at,CASE WHEN package_id IS NOT NULL THEN uploaded_at END),COALESCE(source_id,''),COALESCE(selected_cover_id,''),media_terminal,description,is_broadcast,CASE WHEN is_broadcast THEN COALESCE((SELECT (b.payload_json->>'StartedAt')::timestamptz FROM hhc_web.member_recording_broadcast b WHERE b.recording_id=member_recording.id),(SELECT min(s.observed_at) FROM hhc_web.member_recording_broadcast_signal s WHERE s.recording_id=member_recording.id AND s.phase='live' AND EXISTS(SELECT 1 FROM hhc_web.member_recording_broadcast_signal earlier WHERE earlier.recording_id=s.recording_id AND earlier.id<s.id AND earlier.phase IN ('draft','scheduled','preview','start_pending')))) END,COALESCE((SELECT public_id FROM hhc_web.member_recording_public_id p WHERE p.recording_id=member_recording.id),'')`

func scanRecording(row *sql.Row) (recordings.Recording, error) {
	var v recordings.Recording
	err := row.Scan(&v.ID, &v.Title, &v.UploadedAt, &v.ExpiresAt, &v.DraftExpiresAt, &v.Featured, &v.Hidden, &v.Status, &v.PrimaryAssetVersionID, &v.Version, &v.PackageID, &v.ReadyAt, &v.SourceID, &v.SelectedCoverID, &v.MediaTerminal, &v.Description, &v.Broadcast, &v.BroadcastStartedAt, &v.PublicID)
	if errors.Is(err, sql.ErrNoRows) {
		return v, recordings.ErrNotFound
	}
	return v, err
}
func recordingConflict(err error) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) && (pg.Code == "23505" || pg.Code == "23503") {
		return recordings.ErrConflict
	}
	return err
}
func (r *RecordingRepository) Get(ctx context.Context, id string) (recordings.Recording, error) {
	return readSubjectGet(ctx, r.db, id)
}
func readSubjectGet(ctx context.Context, q recordingRowReader, id string) (recordings.Recording, error) {
	return scanRecording(q.QueryRowContext(ctx, `SELECT `+recordingColumns+` FROM hhc_web.member_recording WHERE id::text=$1 AND deleted_at IS NULL`, id))
}

func (r *RecordingRepository) Create(ctx context.Context, title, actor, key string, now time.Time) (recordings.Recording, error) {
	if !recordings.ValidTitle(title) || actor == "" || key == "" {
		return recordings.Recording{}, recordings.ErrInvalid
	}
	tx, finishTx, err := r.beginRecordingTransaction(ctx)
	if err != nil {
		return recordings.Recording{}, err
	}
	defer finishTx()
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, actor); err != nil {
		return recordings.Recording{}, err
	}
	var existingID, existingActor, existingTitle string
	err = tx.QueryRowContext(ctx, `SELECT id::text,created_by,create_title FROM hhc_web.member_recording WHERE idempotency_key=$1`, key).Scan(&existingID, &existingActor, &existingTitle)
	if err == nil {
		if existingActor != actor || existingTitle != title {
			return recordings.Recording{}, recordings.ErrConflict
		}
		if err = tx.Commit(); err != nil {
			return recordings.Recording{}, err
		}
		return r.Get(ctx, existingID)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return recordings.Recording{}, err
	}
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM hhc_web.member_recording WHERE created_by=$1 AND NOT is_broadcast AND deleted_at IS NULL AND uploaded_at IS NULL AND draft_expires_at>$2`, actor, now).Scan(&count); err != nil {
		return recordings.Recording{}, err
	}
	if count >= 2 {
		return recordings.Recording{}, recordings.ErrDraftLimit
	}
	id := platform.NewID()
	_, err = tx.ExecContext(ctx, `INSERT INTO hhc_web.member_recording(id,title_override,create_title,draft_expires_at,idempotency_key,created_by,updated_by,created_at,updated_at) VALUES($1,$2,$2,$3,$4,$5,$5,$6,$6)`, id, title, now.Add(24*time.Hour), key, actor, now)
	if err != nil {
		return recordings.Recording{}, recordingConflict(err)
	}
	if _, err = assignRecordingPublicID(ctx, tx, id, newRecordingPublicID); err != nil {
		return recordings.Recording{}, err
	}
	if err = r.enqueueAudit(ctx, tx, "cms.recording.create", id, actor, now); err != nil {
		return recordings.Recording{}, err
	}
	if err = tx.Commit(); err != nil {
		return recordings.Recording{}, err
	}
	return r.Get(ctx, id)
}

func (r *RecordingRepository) list(ctx context.Context, query string, args ...any) ([]recordings.Recording, error) {
	return r.queryRecordings(ctx, `SELECT `+recordingColumns+` FROM hhc_web.member_recording `+query, args...)
}
func (r *RecordingRepository) queryRecordings(ctx context.Context, query string, args ...any) ([]recordings.Recording, error) {
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]recordings.Recording, 0)
	for rows.Next() {
		var v recordings.Recording
		if err := rows.Scan(&v.ID, &v.Title, &v.UploadedAt, &v.ExpiresAt, &v.DraftExpiresAt, &v.Featured, &v.Hidden, &v.Status, &v.PrimaryAssetVersionID, &v.Version, &v.PackageID, &v.ReadyAt, &v.SourceID, &v.SelectedCoverID, &v.MediaTerminal, &v.Description, &v.Broadcast, &v.BroadcastStartedAt, &v.PublicID); err != nil {
			return nil, err
		}
		values = append(values, v)
	}
	return values, rows.Err()
}
func (r *RecordingRepository) ListAdmin(ctx context.Context, origin string) ([]recordings.Recording, error) {
	if origin != "" && origin != "upload" && origin != "live" {
		return nil, recordings.ErrInvalid
	}
	return r.list(ctx, `WHERE deleted_at IS NULL AND ($1='' OR is_broadcast=($1='live')) AND ($1<>'live' OR EXISTS(SELECT 1 FROM hhc_web.member_recording_broadcast b WHERE b.recording_id=member_recording.id AND b.payload_json->'View'->>'phase' IN ('processing','archived'))) ORDER BY created_at DESC,id DESC LIMIT 500`, origin)
}
func (r *RecordingRepository) ListVisible(ctx context.Context, now time.Time) ([]recordings.Recording, error) {
	return r.list(ctx, `WHERE deleted_at IS NULL AND NOT media_terminal AND status='published' AND hidden=false AND package_id IS NOT NULL AND uploaded_at IS NOT NULL AND expires_at>$1 ORDER BY uploaded_at DESC,id DESC LIMIT 500`, now)
}
func (r *RecordingRepository) ListVisiblePage(ctx context.Context, now time.Time, at *time.Time, id string, limit int, query string) ([]recordings.Recording, error) {
	var afterID any
	if id != "" {
		afterID = id
	}
	where := `WHERE deleted_at IS NULL AND NOT media_terminal AND status='published' AND hidden=false AND package_id IS NOT NULL AND uploaded_at IS NOT NULL AND expires_at>$1 AND ($2::timestamptz IS NULL OR (uploaded_at,id)<($2::timestamptz,$3::uuid))`
	args := []any{now, at, afterID, limit}
	escape := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	for _, term := range strings.Fields(query) {
		args = append(args, "%"+escape.Replace(term)+"%")
		where += fmt.Sprintf(` AND (title_override ILIKE $%d ESCAPE '\' OR description ILIKE $%d ESCAPE '\')`, len(args), len(args))
	}
	return r.list(ctx, where+` ORDER BY uploaded_at DESC,id DESC LIMIT $4`, args...)
}

func (r *RecordingRepository) ListPendingCompletions(ctx context.Context) ([]recordings.Recording, error) {
	// Rotate unfinished/erroring sessions so the oldest 100 cannot starve later completions.
	return r.queryRecordings(ctx, `WITH picked AS (
 SELECT id FROM hhc_web.member_recording WHERE deleted_at IS NULL AND uploaded_at IS NULL AND (source_id IS NOT NULL OR package_id IS NOT NULL)
 ORDER BY completion_checked_at NULLS FIRST,id LIMIT 100 FOR UPDATE SKIP LOCKED
 ) UPDATE hhc_web.member_recording SET completion_checked_at=clock_timestamp()
 WHERE id IN(SELECT id FROM picked) RETURNING `+recordingColumns)
}

const publishRecordingQuery = `UPDATE hhc_web.member_recording SET status='published',hidden=false,version=version+1,updated_by=$3,updated_at=$4,published_at=COALESCE(published_at,$4) WHERE id::text=$1 AND version=$2 AND status='draft' AND uploaded_at IS NOT NULL AND expires_at>$4 AND trim(title_override)<>'' AND package_id IS NOT NULL AND (NOT is_broadcast OR EXISTS(SELECT 1 FROM hhc_web.member_recording_broadcast b WHERE b.recording_id=member_recording.id AND b.payload_json->>'Revoked'='false' AND b.payload_json->>'RangePending'='false' AND b.payload_json->'Projection'->>'state'='ready' AND b.payload_json->'View'->'boundaries'->>'endSequenceExclusive' IS NOT NULL))`

func (r *RecordingRepository) Publish(ctx context.Context, id string, expected int64, actor string, now time.Time) (recordings.Recording, error) {
	return r.mutate(ctx, id, expected, actor, now, "cms.recording.publish", publishRecordingQuery, actor, now)
}
func (r *RecordingRepository) Unpublish(ctx context.Context, id string, expected int64, actor string, now time.Time) (recordings.Recording, error) {
	return r.mutate(ctx, id, expected, actor, now, "cms.recording.unpublish", `UPDATE hhc_web.member_recording SET status='draft',hidden=false,version=version+1,updated_by=$3,updated_at=$4 WHERE id::text=$1 AND version=$2 AND (status='published' AND expires_at>$4 OR status='draft' AND EXISTS(SELECT 1 FROM hhc_web.member_recording_capture c WHERE c.recording_id=member_recording.id AND (c.active OR c.payload_json->'Status'->>'autoPublish'='pending')))`, actor, now)
}
func (r *RecordingRepository) UpdateTitle(ctx context.Context, id string, expected int64, title, actor string, now time.Time, canPublish bool, description *string) (recordings.Recording, error) {
	if !recordings.ValidTitle(title) || (description != nil && !recordings.ValidDescription(*description)) {
		return recordings.Recording{}, recordings.ErrInvalid
	}
	return r.mutate(ctx, id, expected, actor, now, "cms.recording.title.update", `UPDATE hhc_web.member_recording SET title_override=$3,description=COALESCE($7,description),version=version+1,updated_by=$4,updated_at=$5 WHERE id::text=$1 AND version=$2 AND (expires_at>$5 OR expires_at IS NULL AND (is_broadcast OR draft_expires_at>$5)) AND (status='draft' OR $6)`, title, actor, now, canPublish, description)
}
func (r *RecordingRepository) SetExposure(ctx context.Context, id string, expected int64, featured, hidden bool, actor string, now time.Time) (recordings.Recording, error) {
	return r.mutate(ctx, id, expected, actor, now, "cms.recording.exposure.update", `UPDATE hhc_web.member_recording SET featured=$3,hidden=$4,version=version+1,updated_by=$5,updated_at=$6 WHERE id::text=$1 AND version=$2 AND COALESCE(expires_at,draft_expires_at)>$6 AND (status='published' OR NOT $4)`, featured, hidden, actor, now)
}
func (r *RecordingRepository) mutate(ctx context.Context, id string, expected int64, actor string, now time.Time, action, query string, args ...any) (recordings.Recording, error) {
	tx, finishTx, err := r.beginRecordingTransaction(ctx)
	if err != nil {
		return recordings.Recording{}, err
	}
	defer finishTx()
	if _, err = r.mutateTx(ctx, tx, id, expected, actor, now, action, query, args...); err != nil {
		return recordings.Recording{}, err
	}
	if err = tx.Commit(); err != nil {
		return recordings.Recording{}, err
	}
	return r.Get(ctx, id)
}

func (r *RecordingRepository) mutateTx(ctx context.Context, tx *sql.Tx, id string, expected int64, actor string, now time.Time, action, query string, args ...any) (recordings.Recording, error) {
	var version int64
	var publishedAt sql.NullTime
	var title string
	var broadcast bool
	if err := tx.QueryRowContext(ctx, `SELECT version,published_at,title_override,is_broadcast FROM hhc_web.member_recording WHERE id::text=$1 AND deleted_at IS NULL FOR UPDATE`, id).Scan(&version, &publishedAt, &title, &broadcast); errors.Is(err, sql.ErrNoRows) {
		return recordings.Recording{}, recordings.ErrNotFound
	} else if err != nil {
		return recordings.Recording{}, err
	}
	if version != expected {
		return recordings.Recording{}, recordings.ErrPrecondition
	}
	result, err := tx.ExecContext(ctx, query, append([]any{id, expected}, args...)...)
	if err != nil {
		return recordings.Recording{}, recordingConflict(err)
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return recordings.Recording{}, recordings.ErrConflict
	}
	if action != "cms.recording.capture.publish" {
		if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.member_recording SET human_edit_generation=human_edit_generation+1 WHERE id::text=$1`, id); err != nil {
			return recordings.Recording{}, err
		}
	}
	if action == "cms.recording.publish" || action == "cms.recording.capture.publish" {
		if err := publishBroadcastAuthorityTx(ctx, tx, id); err != nil {
			return recordings.Recording{}, err
		}
	}
	if action == "cms.recording.unpublish" {
		if err := closeCapturePublicationTx(ctx, tx, id); err != nil {
			return recordings.Recording{}, err
		}
	}
	if action != "cms.recording.capture.publish" {
		if err := r.enqueueAudit(ctx, tx, action, id, actor, now); err != nil {
			return recordings.Recording{}, err
		}
	}
	if (action == "cms.recording.publish" || action == "cms.recording.capture.publish") && !publishedAt.Valid && !broadcast {
		if err := enqueueRecordingNotification(ctx, tx, id, title, actor, version+1, now); err != nil {
			return recordings.Recording{}, err
		}
	}
	return scanRecording(tx.QueryRowContext(ctx, `SELECT `+recordingColumns+` FROM hhc_web.member_recording WHERE id::text=$1`, id))
}

func enqueueRecordingNotification(ctx context.Context, tx *sql.Tx, id, title, actor string, version int64, now time.Time, live ...bool) error {
	var publicID string
	err := tx.QueryRowContext(ctx, `SELECT public_id FROM hhc_web.member_recording_public_id WHERE recording_id=$1`, id).Scan(&publicID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	eventID := platform.NewID()
	key := "notification:v1:recording:" + id + ":g0"
	translations := make(map[string]publication.NotificationTranslation)
	required := make(map[string]string)
	subjects := map[string]string{"zh-Hant": "影音專區有新錄影", "zh-Hans": "影音专区有新录像", "en": "A new recording is available", "ja": "新しい録画を公開しました", "ko": "새로운 녹화 영상이 있습니다"}
	if len(live) > 0 && live[0] {
		subjects = map[string]string{"zh-Hant": "直播已開始", "zh-Hans": "直播已开始", "en": "The live broadcast has started", "ja": "ライブ配信が始まりました", "ko": "라이브 방송이 시작되었습니다"}
	}
	for locale, subject := range subjects {
		path := "/" + locale + "/member-videos/" + id
		if publicID != "" {
			path = "/video/" + publicID
		}
		translations[locale] = publication.NotificationTranslation{Subject: subject, Body: title, ClickBehavior: "url", ActionURL: path}
		required[locale] = "video.meeting-recordings.access"
	}
	payload := publication.NotificationSubmissionPayload{Name: "Member recording " + id, Source: &publication.BulletinSource{Kind: "recording", RecordingID: id}, ActorID: actor, RequestID: eventID, IdempotencyKey: key}
	for _, channel := range []string{"email", "web_push"} {
		payload.Notifications = append(payload.Notifications, publication.NotificationCommand{Channel: channel, AudienceType: "recording_members", Translations: translations, RequiredEntitlements: required})
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO hhc_web.outbox_event(id,destination,event_type,aggregate_type,aggregate_id,aggregate_version,payload_json,idempotency_key,status,next_attempt_at,created_at,updated_at) VALUES($1,'engagement-api','recording.notification.submit','recording',$2,$3,$4,$5,'pending',$6,$6,$6) ON CONFLICT(destination,idempotency_key) DO NOTHING`, eventID, id, version, encoded, key, now)
	return err
}
func (r *RecordingRepository) ListExpired(ctx context.Context, now time.Time) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id::text FROM hhc_web.member_recording WHERE deleted_at IS NULL AND uploaded_at IS NOT NULL AND expires_at<=$1 AND package_id IS NOT NULL AND (status<>'draft' OR hidden) ORDER BY expires_at,id LIMIT 100`, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
func (r *RecordingRepository) Expire(ctx context.Context, id string, now time.Time) error {
	result, err := r.db.ExecContext(ctx, `UPDATE hhc_web.member_recording SET status='draft',hidden=false,updated_at=$2,version=version+CASE WHEN status<>'draft' OR hidden THEN 1 ELSE 0 END WHERE id::text=$1 AND deleted_at IS NULL AND (expires_at<=$2 OR expires_at IS NULL AND NOT is_broadcast AND draft_expires_at<=$2)`, id, now)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return recordings.ErrConflict
	}
	return nil
}
