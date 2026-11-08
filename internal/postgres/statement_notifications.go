package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/content"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/publication"
	"time"
)

func (r *Repository) PublishStatement(ctx context.Context, id string, expected int64, actor string, request content.StatementNotificationRequest, now time.Time) (content.Item, error) {
	return r.startNewsPublish(ctx, id, expected, actor, now, false, &request)
}
func enqueueNotificationSubmission(ctx context.Context, tx *sql.Tx, id string, version int64, actor string, request content.StatementNotificationRequest, href string, now time.Time) error {
	notifications := make([]publication.NotificationCommand, 0, len(request.Channels))
	for _, channel := range request.Channels {
		audience := "all"
		if channel == "email" {
			audience = "newsletter"
		}
		notifications = append(notifications, publication.NotificationCommand{
			Channel: channel, AudienceType: audience,
			Translations: map[string]publication.NotificationTranslation{"zh-Hant": {Subject: request.Subject, Body: request.Body, ClickBehavior: "url", ActionURL: href}},
		})
	}
	key := fmt.Sprintf("notification:v2:content:%s:v%d:%s", id, version, request.RequestID)
	payload, err := json.Marshal(publication.NotificationSubmissionPayload{Name: "Website notification", Notifications: notifications, ActorID: actor, RequestID: request.RequestID, IdempotencyKey: key})
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO hhc_web.outbox_event(id,destination,event_type,aggregate_type,aggregate_id,aggregate_version,payload_json,idempotency_key,status,next_attempt_at,created_at,updated_at) VALUES($1,'engagement-api','content.notification.submit','news',$2,$3,$4,$5,'pending',$6,$6,$6) ON CONFLICT(destination,idempotency_key) DO NOTHING`, platform.NewID(), id, version, payload, key, now)
	return err
}

const statementBatchColumns = `id::text,content_id::text,published_version,request_id::text,channel,COALESCE(campaign_id::text,''),status,actor_id,preview_json->>'subject',preview_json->>'body',preview_json->>'href',cancelled_at,created_at`

func scanStatementBatch(row interface{ Scan(...any) error }) (content.StatementNotificationBatch, error) {
	var b content.StatementNotificationBatch
	err := row.Scan(&b.ID, &b.ContentID, &b.PublishedVersion, &b.RequestID, &b.Channel, &b.CampaignID, &b.Status, &b.ActorID, &b.Subject, &b.Body, &b.Href, &b.CancelledAt, &b.CreatedAt)
	return b, err
}
func (r *Repository) StatementNotification(ctx context.Context, id string) (content.StatementNotificationBatch, error) {
	return scanStatementBatch(r.db.QueryRowContext(ctx, `SELECT `+statementBatchColumns+` FROM hhc_web.statement_notification_batch WHERE id=$1`, id))
}
func (r *Repository) SetStatementCampaign(ctx context.Context, id, campaignID string) error {
	result, err := r.db.ExecContext(ctx, `UPDATE hhc_web.statement_notification_batch SET campaign_id=$2,updated_at=now() WHERE id=$1 AND (campaign_id IS NULL OR campaign_id=$2)`, id, campaignID)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return fmt.Errorf("statement campaign identity conflict")
	}
	return nil
}
func (r *Repository) CompleteStatementNotification(ctx context.Context, event publication.Event, b content.StatementNotificationBatch, now time.Time) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// A concurrent unpublish keeps cancellation intent even when queue completion arrives later.
	if _, err = tx.ExecContext(ctx, `UPDATE hhc_web.statement_notification_batch SET status=CASE WHEN cancelled_at IS NOT NULL THEN 'cancelled' ELSE 'queued' END,updated_at=$2 WHERE id=$1`, b.ID, now); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE hhc_web.outbox_event SET status='delivered',claimed_until=NULL,last_error='',updated_at=$2 WHERE id=$1`, event.ID, now); err != nil {
		return err
	}
	return tx.Commit()
}
