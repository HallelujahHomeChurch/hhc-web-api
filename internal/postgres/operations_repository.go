package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/operations"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
)

type OperationsRepository struct{ db *sql.DB }

func NewOperationsRepository(db *sql.DB) *OperationsRepository { return &OperationsRepository{db: db} }

func (r *OperationsRepository) CreateChurchUnit(ctx context.Context, input operations.ChurchUnitInput, actor, requestID, idempotencyKey string, now time.Time) (operations.ChurchUnit, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return operations.ChurchUnit{}, err
	}
	defer tx.Rollback()
	id := platform.NewID()
	result, err := tx.ExecContext(ctx, `INSERT INTO hhc_web.church_unit(id,stable_key,name,description,parent_id,status,version,idempotency_key,created_by,updated_by,created_at,updated_at) VALUES($1,$2,$3,$4,NULLIF($5,'')::uuid,'active',1,$6,$7,$7,$8,$8) ON CONFLICT(idempotency_key) DO NOTHING`, id, input.Key, input.Name, input.Description, input.ParentID, idempotencyKey, actor, now)
	if err != nil {
		return operations.ChurchUnit{}, mapOperationsError(err)
	}
	created, _ := result.RowsAffected()
	var existingID, existingKey string
	if err := tx.QueryRowContext(ctx, `SELECT id::text,stable_key FROM hhc_web.church_unit WHERE idempotency_key=$1`, idempotencyKey).Scan(&existingID, &existingKey); err != nil {
		return operations.ChurchUnit{}, err
	}
	if existingKey != input.Key {
		return operations.ChurchUnit{}, operations.ErrConflict
	}
	value, err := loadChurchUnit(ctx, tx, existingID)
	if err == nil && created == 1 {
		err = r.insertAudit(ctx, tx, "church_unit", existingID, "create", actor, requestID, nil, value, now)
	}
	if err != nil {
		return operations.ChurchUnit{}, err
	}
	return value, tx.Commit()
}

func (r *OperationsRepository) GetChurchUnit(ctx context.Context, id string) (operations.ChurchUnit, error) {
	var value operations.ChurchUnit
	var parent sql.NullString
	err := r.db.QueryRowContext(ctx, `SELECT id::text,stable_key,name,description,parent_id::text,status,version FROM hhc_web.church_unit WHERE id=$1`, id).Scan(&value.ID, &value.Key, &value.Name, &value.Description, &parent, &value.Status, &value.Version)
	if parent.Valid {
		value.ParentID = parent.String
	}
	return value, mapOperationsNotFound(err)
}

func (r *OperationsRepository) ListChurchUnits(ctx context.Context, includeArchived bool) ([]operations.ChurchUnit, error) {
	query := `SELECT id::text,stable_key,name,description,parent_id::text,status,version FROM hhc_web.church_unit`
	if !includeArchived {
		query += ` WHERE status<>'archived'`
	}
	query += ` ORDER BY name,stable_key`
	rows, err := r.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []operations.ChurchUnit{}
	for rows.Next() {
		var value operations.ChurchUnit
		var parent sql.NullString
		if err := rows.Scan(&value.ID, &value.Key, &value.Name, &value.Description, &parent, &value.Status, &value.Version); err != nil {
			return nil, err
		}
		if parent.Valid {
			value.ParentID = parent.String
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (r *OperationsRepository) WouldCreateChurchUnitCycle(ctx context.Context, id, parentID string) (bool, error) {
	if id == parentID {
		return true, nil
	}
	var cycle bool
	err := r.db.QueryRowContext(ctx, `WITH RECURSIVE ancestors AS (SELECT id,parent_id FROM hhc_web.church_unit WHERE id=$1 UNION ALL SELECT u.id,u.parent_id FROM hhc_web.church_unit u JOIN ancestors a ON u.id=a.parent_id) SELECT EXISTS(SELECT 1 FROM ancestors WHERE id=$2)`, parentID, id).Scan(&cycle)
	return cycle, err
}

func (r *OperationsRepository) SaveChurchUnit(ctx context.Context, id string, expected int64, input operations.ChurchUnitInput, actor, requestID string, now time.Time) (operations.ChurchUnit, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return operations.ChurchUnit{}, err
	}
	defer tx.Rollback()
	before, err := lockChurchUnit(ctx, tx, id, expected)
	if err != nil {
		return operations.ChurchUnit{}, err
	}
	if before.Key != input.Key {
		return operations.ChurchUnit{}, operations.ErrInvalid
	}
	if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.church_unit SET name=$2,description=$3,parent_id=NULLIF($4,''),version=version+1,updated_by=$5,updated_at=$6 WHERE id=$1`, id, input.Name, input.Description, input.ParentID, actor, now); err != nil {
		return operations.ChurchUnit{}, mapOperationsError(err)
	}
	after, err := loadChurchUnit(ctx, tx, id)
	if err == nil {
		err = r.insertAudit(ctx, tx, "church_unit", id, "update", actor, requestID, before, after, now)
	}
	if err != nil {
		return operations.ChurchUnit{}, err
	}
	return after, tx.Commit()
}

func (r *OperationsRepository) SetChurchUnitStatus(ctx context.Context, id string, expected int64, status operations.Status, actor, requestID string, now time.Time) (operations.ChurchUnit, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return operations.ChurchUnit{}, err
	}
	defer tx.Rollback()
	before, err := lockChurchUnit(ctx, tx, id, expected)
	if err != nil {
		return operations.ChurchUnit{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.church_unit SET status=$2,version=version+1,updated_by=$3,updated_at=$4 WHERE id=$1`, id, status, actor, now); err != nil {
		return operations.ChurchUnit{}, err
	}
	after, err := loadChurchUnit(ctx, tx, id)
	if err == nil {
		err = r.insertAudit(ctx, tx, "church_unit", id, "status_"+string(status), actor, requestID, before, after, now)
	}
	if err != nil {
		return operations.ChurchUnit{}, err
	}
	return after, tx.Commit()
}

func (r *OperationsRepository) CreateResource(ctx context.Context, input operations.ResourceInput, actor, requestID, idempotencyKey string, now time.Time) (operations.Resource, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return operations.Resource{}, err
	}
	defer tx.Rollback()
	id := platform.NewID()
	result, err := tx.ExecContext(ctx, `INSERT INTO hhc_web.resource(id,stable_key,name,description,kind,church_unit_id,location_content_id,timezone,visibility,reservation_enabled,status,version,idempotency_key,created_by,updated_by,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,NULLIF($7,'')::uuid,$8,$9,false,'active',1,$10,$11,$11,$12,$12) ON CONFLICT(idempotency_key) DO NOTHING`, id, input.Key, input.Name, input.Description, input.Kind, input.ChurchUnitID, input.LocationContentID, input.Timezone, input.Visibility, idempotencyKey, actor, now)
	if err != nil {
		return operations.Resource{}, mapOperationsError(err)
	}
	created, _ := result.RowsAffected()
	var existingID, existingKey string
	if err := tx.QueryRowContext(ctx, `SELECT id::text,stable_key FROM hhc_web.resource WHERE idempotency_key=$1`, idempotencyKey).Scan(&existingID, &existingKey); err != nil {
		return operations.Resource{}, err
	}
	if existingKey != input.Key {
		return operations.Resource{}, operations.ErrConflict
	}
	value, err := loadResource(ctx, tx, existingID)
	if err == nil && created == 1 {
		err = r.insertAudit(ctx, tx, "resource", existingID, "create", actor, requestID, nil, value, now)
	}
	if err != nil {
		return operations.Resource{}, err
	}
	return value, tx.Commit()
}

func (r *OperationsRepository) GetResource(ctx context.Context, id string) (operations.Resource, error) {
	return loadResource(ctx, r.db, id)
}

func (r *OperationsRepository) ListResources(ctx context.Context, includeArchived bool) ([]operations.Resource, error) {
	query := `SELECT id::text FROM hhc_web.resource`
	if !includeArchived {
		query += ` WHERE status<>'archived'`
	}
	query += ` ORDER BY name,stable_key`
	rows, err := r.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	values := make([]operations.Resource, 0, len(ids))
	for _, id := range ids {
		value, err := loadResource(ctx, r.db, id)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, nil
}

func (r *OperationsRepository) SaveResource(ctx context.Context, id string, expected int64, input operations.ResourceInput, actor, requestID string, now time.Time) (operations.Resource, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return operations.Resource{}, err
	}
	defer tx.Rollback()
	before, err := lockResource(ctx, tx, id, expected)
	if err != nil {
		return operations.Resource{}, err
	}
	if before.Key != input.Key || before.Kind != input.Kind {
		return operations.Resource{}, operations.ErrInvalid
	}
	_, err = tx.ExecContext(ctx, `UPDATE hhc_web.resource SET name=$2,description=$3,church_unit_id=$4,location_content_id=NULLIF($5,'')::uuid,timezone=$6,visibility=$7,version=version+1,updated_by=$8,updated_at=$9 WHERE id=$1`, id, input.Name, input.Description, input.ChurchUnitID, input.LocationContentID, input.Timezone, input.Visibility, actor, now)
	if err != nil {
		return operations.Resource{}, mapOperationsError(err)
	}
	after, err := loadResource(ctx, tx, id)
	if err == nil {
		err = r.insertAudit(ctx, tx, "resource", id, "update", actor, requestID, before, after, now)
	}
	if err != nil {
		return operations.Resource{}, err
	}
	return after, tx.Commit()
}

func (r *OperationsRepository) SetResourceStatus(ctx context.Context, id string, expected int64, status operations.Status, actor, requestID string, now time.Time) (operations.Resource, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return operations.Resource{}, err
	}
	defer tx.Rollback()
	before, err := lockResource(ctx, tx, id, expected)
	if err != nil {
		return operations.Resource{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.resource SET status=$2,version=version+1,updated_by=$3,updated_at=$4 WHERE id=$1`, id, status, actor, now); err != nil {
		return operations.Resource{}, err
	}
	after, err := loadResource(ctx, tx, id)
	if err == nil {
		err = r.insertAudit(ctx, tx, "resource", id, "status_"+string(status), actor, requestID, before, after, now)
	}
	if err != nil {
		return operations.Resource{}, err
	}
	return after, tx.Commit()
}

func (r *OperationsRepository) CreateMeeting(ctx context.Context, input operations.MeetingInput, actor, requestID, idempotencyKey string, now time.Time) (operations.MeetingMutation, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return operations.MeetingMutation{}, err
	}
	defer tx.Rollback()
	id := platform.NewID()
	days := weekdayNumbers(input.Schedule.DaysOfWeek)
	result, err := tx.ExecContext(ctx, `INSERT INTO hhc_web.meeting(id,stable_key,name,description,church_unit_id,venue_resource_id,timezone,schedule_type,weekly_days,weekly_start_time,once_starts_at,duration_minutes,visibility,status,version,idempotency_key,created_by,updated_by,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,NULLIF($10,'')::time,$11,$12,$13,'active',1,$14,$15,$15,$16,$16) ON CONFLICT(idempotency_key) DO NOTHING`, id, input.Key, input.Name, input.Description, input.ChurchUnitID, input.VenueResourceID, input.Timezone, input.Schedule.Type, days, input.Schedule.StartTime, nullableTime(input.Schedule.StartsAt), input.DurationMinutes, input.Visibility, idempotencyKey, actor, now)
	if err != nil {
		return operations.MeetingMutation{}, mapOperationsError(err)
	}
	created, _ := result.RowsAffected()
	var existingID, existingKey string
	if err := tx.QueryRowContext(ctx, `SELECT id::text,stable_key FROM hhc_web.meeting WHERE idempotency_key=$1`, idempotencyKey).Scan(&existingID, &existingKey); err != nil {
		return operations.MeetingMutation{}, err
	}
	if existingKey != input.Key {
		return operations.MeetingMutation{}, operations.ErrConflict
	}
	value, err := loadMeeting(ctx, tx, existingID)
	if err == nil && created == 1 {
		err = r.insertAudit(ctx, tx, "meeting", existingID, "create", actor, requestID, nil, value, now)
	}
	if err != nil {
		return operations.MeetingMutation{}, err
	}
	if err := tx.Commit(); err != nil {
		return operations.MeetingMutation{}, err
	}
	return meetingMutation(value, nil, now), nil
}

func (r *OperationsRepository) ListMeetings(ctx context.Context, includeArchived bool) ([]operations.Meeting, error) {
	where := "true"
	if !includeArchived {
		where = `status<>'archived'`
	}
	values, err := r.listMeetings(ctx, where)
	if err != nil {
		return nil, err
	}
	sort.Slice(values, func(i, j int) bool {
		if values[i].Name == values[j].Name {
			return values[i].Key < values[j].Key
		}
		return values[i].Name < values[j].Name
	})
	return values, nil
}

func (r *OperationsRepository) GetMeeting(ctx context.Context, id string) (operations.Meeting, error) {
	return loadMeeting(ctx, r.db, id)
}

func (r *OperationsRepository) SaveMeeting(ctx context.Context, id string, expected int64, input operations.MeetingInput, actor, requestID string, now time.Time) (operations.MeetingMutation, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return operations.MeetingMutation{}, err
	}
	defer tx.Rollback()
	before, err := lockMeeting(ctx, tx, id, expected)
	if err != nil {
		return operations.MeetingMutation{}, err
	}
	if before.Key != input.Key {
		return operations.MeetingMutation{}, operations.ErrInvalid
	}
	_, err = tx.ExecContext(ctx, `UPDATE hhc_web.meeting SET name=$2,description=$3,church_unit_id=$4,venue_resource_id=$5,timezone=$6,schedule_type=$7,weekly_days=$8,weekly_start_time=NULLIF($9,'')::time,once_starts_at=$10,duration_minutes=$11,visibility=$12,version=version+1,updated_by=$13,updated_at=$14 WHERE id=$1`, id, input.Name, input.Description, input.ChurchUnitID, input.VenueResourceID, input.Timezone, input.Schedule.Type, weekdayNumbers(input.Schedule.DaysOfWeek), input.Schedule.StartTime, nullableTime(input.Schedule.StartsAt), input.DurationMinutes, input.Visibility, actor, now)
	if err != nil {
		return operations.MeetingMutation{}, mapOperationsError(err)
	}
	after, err := loadMeeting(ctx, tx, id)
	if err == nil {
		err = r.insertAudit(ctx, tx, "meeting", id, "update", actor, requestID, before, after, now)
	}
	if err != nil {
		return operations.MeetingMutation{}, err
	}
	overrides, err := loadOverrides(ctx, tx, id)
	if err != nil {
		return operations.MeetingMutation{}, err
	}
	if err := tx.Commit(); err != nil {
		return operations.MeetingMutation{}, err
	}
	return meetingMutation(after, overrides, now), nil
}

func (r *OperationsRepository) SetMeetingStatus(ctx context.Context, id string, expected int64, status operations.Status, actor, requestID string, now time.Time) (operations.Meeting, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return operations.Meeting{}, err
	}
	defer tx.Rollback()
	before, err := lockMeeting(ctx, tx, id, expected)
	if err != nil {
		return operations.Meeting{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.meeting SET status=$2,version=version+1,updated_by=$3,updated_at=$4 WHERE id=$1`, id, status, actor, now); err != nil {
		return operations.Meeting{}, err
	}
	after, err := loadMeeting(ctx, tx, id)
	if err == nil {
		err = r.insertAudit(ctx, tx, "meeting", id, "status_"+string(status), actor, requestID, before, after, now)
	}
	if err != nil {
		return operations.Meeting{}, err
	}
	return after, tx.Commit()
}

func (r *OperationsRepository) PutOverride(ctx context.Context, meetingID string, expected int64, input operations.OccurrenceOverrideInput, actor, requestID string, now time.Time) (operations.OccurrenceOverride, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return operations.OccurrenceOverride{}, err
	}
	defer tx.Rollback()
	before, err := lockMeeting(ctx, tx, meetingID, expected)
	if err != nil {
		return operations.OccurrenceOverride{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO hhc_web.meeting_occurrence_override(meeting_id,occurrence_date,cancelled,starts_at,duration_minutes,venue_resource_id,reason,version,created_by,updated_by,created_at,updated_at) VALUES($1,$2,$3,$4,$5,NULLIF($6,'')::uuid,$7,1,$8,$8,$9,$9) ON CONFLICT(meeting_id,occurrence_date) DO UPDATE SET cancelled=EXCLUDED.cancelled,starts_at=EXCLUDED.starts_at,duration_minutes=EXCLUDED.duration_minutes,venue_resource_id=EXCLUDED.venue_resource_id,reason=EXCLUDED.reason,version=hhc_web.meeting_occurrence_override.version+1,updated_by=EXCLUDED.updated_by,updated_at=EXCLUDED.updated_at`, meetingID, input.OccurrenceDate, input.Cancelled, input.StartsAt, input.DurationMinutes, stringPointer(input.VenueResourceID), input.Reason, actor, now)
	if err != nil {
		return operations.OccurrenceOverride{}, mapOperationsError(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.meeting SET version=version+1,updated_by=$2,updated_at=$3 WHERE id=$1`, meetingID, actor, now); err != nil {
		return operations.OccurrenceOverride{}, err
	}
	value, err := loadOverride(ctx, tx, meetingID, input.OccurrenceDate)
	if err == nil {
		err = r.insertAudit(ctx, tx, "meeting", meetingID, "put_override", actor, requestID, before, value, now)
	}
	if err != nil {
		return operations.OccurrenceOverride{}, err
	}
	return value, tx.Commit()
}

func (r *OperationsRepository) DeleteOverride(ctx context.Context, meetingID string, expected int64, occurrenceDate, actor, requestID string, now time.Time) (operations.Meeting, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return operations.Meeting{}, err
	}
	defer tx.Rollback()
	before, err := lockMeeting(ctx, tx, meetingID, expected)
	if err != nil {
		return operations.Meeting{}, err
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM hhc_web.meeting_occurrence_override WHERE meeting_id=$1 AND occurrence_date=$2`, meetingID, occurrenceDate)
	if err != nil {
		return operations.Meeting{}, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return operations.Meeting{}, operations.ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.meeting SET version=version+1,updated_by=$2,updated_at=$3 WHERE id=$1`, meetingID, actor, now); err != nil {
		return operations.Meeting{}, err
	}
	after, err := loadMeeting(ctx, tx, meetingID)
	if err == nil {
		err = r.insertAudit(ctx, tx, "meeting", meetingID, "delete_override", actor, requestID, before, after, now)
	}
	if err != nil {
		return operations.Meeting{}, err
	}
	return after, tx.Commit()
}

func (r *OperationsRepository) ListOverrides(ctx context.Context, meetingID string) ([]operations.OccurrenceOverride, error) {
	return loadOverrides(ctx, r.db, meetingID)
}

func (r *OperationsRepository) ReplaceMeetingBindings(ctx context.Context, meetingID string, expected int64, bindings []string, actor, requestID string, now time.Time) (operations.Meeting, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return operations.Meeting{}, err
	}
	defer tx.Rollback()
	before, err := lockMeeting(ctx, tx, meetingID, expected)
	if err != nil {
		return operations.Meeting{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM hhc_web.meeting_collection_binding WHERE meeting_id=$1`, meetingID); err != nil {
		return operations.Meeting{}, err
	}
	for _, collectionID := range bindings {
		if _, err := tx.ExecContext(ctx, `INSERT INTO hhc_web.meeting_collection_binding(meeting_id,collection_id,enabled,created_by,created_at) VALUES($1,$2,true,$3,$4)`, meetingID, collectionID, actor, now); err != nil {
			return operations.Meeting{}, mapOperationsError(err)
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.meeting SET version=version+1,updated_by=$2,updated_at=$3 WHERE id=$1`, meetingID, actor, now); err != nil {
		return operations.Meeting{}, err
	}
	after, err := loadMeeting(ctx, tx, meetingID)
	if err == nil {
		err = r.insertAudit(ctx, tx, "meeting", meetingID, "replace_bindings", actor, requestID, before, map[string]any{"meeting": after, "collectionIds": bindings}, now)
	}
	if err != nil {
		return operations.Meeting{}, err
	}
	return after, tx.Commit()
}

func (r *OperationsRepository) ListMeetingBindings(ctx context.Context, meetingID string) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT collection_id FROM hhc_web.meeting_collection_binding WHERE meeting_id=$1 AND enabled ORDER BY collection_id`, meetingID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []string{}
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (r *OperationsRepository) ListOccurrences(ctx context.Context, query operations.OccurrenceQuery) ([]operations.Occurrence, error) {
	where := "status='active'"
	if query.PublicOnly {
		where += " AND visibility='public'"
	}
	meetings, err := r.listMeetings(ctx, where)
	if err != nil {
		return nil, err
	}
	result := []operations.Occurrence{}
	// ponytail: one override query per meeting; batch only when production query volume makes this material.
	for _, meeting := range meetings {
		overrides, err := loadOverrides(ctx, r.db, meeting.ID)
		if err != nil {
			return nil, err
		}
		occurrences, err := operations.ResolveOccurrences(meeting, overrides, query.From, query.To)
		if err != nil {
			return nil, err
		}
		result = append(result, occurrences...)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].StartsAt.Equal(result[j].StartsAt) {
			return result[i].ID < result[j].ID
		}
		return result[i].StartsAt.Before(result[j].StartsAt)
	})
	return result, nil
}

func (r *OperationsRepository) ListMediaSyncWindows(ctx context.Context, from, to time.Time) ([]operations.MediaSyncWindow, error) {
	meetings, err := r.listMeetings(ctx, `status='active' AND EXISTS(SELECT 1 FROM hhc_web.meeting_collection_binding b WHERE b.meeting_id=meeting.id AND b.enabled)`)
	if err != nil {
		return nil, err
	}
	windows := []operations.MediaSyncWindow{}
	for _, meeting := range meetings {
		overrides, err := loadOverrides(ctx, r.db, meeting.ID)
		if err != nil {
			return nil, err
		}
		occurrences, err := operations.ResolveOccurrences(meeting, overrides, from, to)
		if err != nil {
			return nil, err
		}
		for _, occurrence := range occurrences {
			if occurrence.Status == operations.OccurrenceScheduled {
				windows = append(windows, operations.MediaSyncWindow{StartsAt: occurrence.StartsAt, EndsAt: occurrence.EndsAt})
			}
		}
	}
	return unionWindows(windows), nil
}

func (r *OperationsRepository) listMeetings(ctx context.Context, where string) ([]operations.Meeting, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id::text,stable_key,name,description,church_unit_id::text,venue_resource_id::text,timezone,schedule_type,COALESCE(array_to_json(weekly_days)::text,'null'),COALESCE(to_char(weekly_start_time,'HH24:MI'),''),once_starts_at,duration_minutes,visibility,status,version FROM hhc_web.meeting WHERE `+where)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []operations.Meeting{}
	for rows.Next() {
		value, err := scanMeeting(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func loadChurchUnit(ctx context.Context, query interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string) (operations.ChurchUnit, error) {
	var value operations.ChurchUnit
	var parent sql.NullString
	err := query.QueryRowContext(ctx, `SELECT id::text,stable_key,name,description,parent_id::text,status,version FROM hhc_web.church_unit WHERE id=$1`, id).Scan(&value.ID, &value.Key, &value.Name, &value.Description, &parent, &value.Status, &value.Version)
	if parent.Valid {
		value.ParentID = parent.String
	}
	return value, mapOperationsNotFound(err)
}

func lockChurchUnit(ctx context.Context, tx *sql.Tx, id string, expected int64) (operations.ChurchUnit, error) {
	var version int64
	if err := tx.QueryRowContext(ctx, `SELECT version FROM hhc_web.church_unit WHERE id=$1 FOR UPDATE`, id).Scan(&version); err != nil {
		return operations.ChurchUnit{}, mapOperationsNotFound(err)
	}
	if version != expected {
		return operations.ChurchUnit{}, operations.ErrPrecondition
	}
	return loadChurchUnit(ctx, tx, id)
}

func loadResource(ctx context.Context, query interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string) (operations.Resource, error) {
	var value operations.Resource
	var location sql.NullString
	err := query.QueryRowContext(ctx, `SELECT id::text,stable_key,name,description,kind,church_unit_id::text,location_content_id::text,timezone,visibility,reservation_enabled,status,version FROM hhc_web.resource WHERE id=$1`, id).Scan(&value.ID, &value.Key, &value.Name, &value.Description, &value.Kind, &value.ChurchUnitID, &location, &value.Timezone, &value.Visibility, &value.ReservationEnabled, &value.Status, &value.Version)
	if location.Valid {
		value.LocationContentID = location.String
	}
	return value, mapOperationsNotFound(err)
}

func lockResource(ctx context.Context, tx *sql.Tx, id string, expected int64) (operations.Resource, error) {
	var version int64
	if err := tx.QueryRowContext(ctx, `SELECT version FROM hhc_web.resource WHERE id=$1 FOR UPDATE`, id).Scan(&version); err != nil {
		return operations.Resource{}, mapOperationsNotFound(err)
	}
	if version != expected {
		return operations.Resource{}, operations.ErrPrecondition
	}
	return loadResource(ctx, tx, id)
}

func loadMeeting(ctx context.Context, query interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string) (operations.Meeting, error) {
	return scanMeeting(query.QueryRowContext(ctx, `SELECT id::text,stable_key,name,description,church_unit_id::text,venue_resource_id::text,timezone,schedule_type,COALESCE(array_to_json(weekly_days)::text,'null'),COALESCE(to_char(weekly_start_time,'HH24:MI'),''),once_starts_at,duration_minutes,visibility,status,version FROM hhc_web.meeting WHERE id=$1`, id))
}

type operationsScanner interface{ Scan(...any) error }

func scanMeeting(row operationsScanner) (operations.Meeting, error) {
	var value operations.Meeting
	var daysJSON string
	var once sql.NullTime
	err := row.Scan(&value.ID, &value.Key, &value.Name, &value.Description, &value.ChurchUnitID, &value.VenueResourceID, &value.Timezone, &value.Schedule.Type, &daysJSON, &value.Schedule.StartTime, &once, &value.DurationMinutes, &value.Visibility, &value.Status, &value.Version)
	if err != nil {
		return value, mapOperationsNotFound(err)
	}
	var days []int
	if err := json.Unmarshal([]byte(daysJSON), &days); err != nil {
		return value, err
	}
	for _, day := range days {
		value.Schedule.DaysOfWeek = append(value.Schedule.DaysOfWeek, time.Weekday(day))
	}
	if once.Valid {
		value.Schedule.StartsAt = once.Time
	}
	return value, nil
}

func lockMeeting(ctx context.Context, tx *sql.Tx, id string, expected int64) (operations.Meeting, error) {
	var version int64
	if err := tx.QueryRowContext(ctx, `SELECT version FROM hhc_web.meeting WHERE id=$1 FOR UPDATE`, id).Scan(&version); err != nil {
		return operations.Meeting{}, mapOperationsNotFound(err)
	}
	if version != expected {
		return operations.Meeting{}, operations.ErrPrecondition
	}
	return loadMeeting(ctx, tx, id)
}

func loadOverride(ctx context.Context, query interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, meetingID, date string) (operations.OccurrenceOverride, error) {
	return scanOverride(query.QueryRowContext(ctx, `SELECT meeting_id::text,occurrence_date::text,cancelled,starts_at,duration_minutes,venue_resource_id::text,reason,version FROM hhc_web.meeting_occurrence_override WHERE meeting_id=$1 AND occurrence_date=$2`, meetingID, date))
}

func scanOverride(row operationsScanner) (operations.OccurrenceOverride, error) {
	var value operations.OccurrenceOverride
	var starts sql.NullTime
	var duration sql.NullInt64
	var venue sql.NullString
	err := row.Scan(&value.MeetingID, &value.OccurrenceDate, &value.Cancelled, &starts, &duration, &venue, &value.Reason, &value.Version)
	if starts.Valid {
		value.StartsAt = &starts.Time
	}
	if duration.Valid {
		v := int(duration.Int64)
		value.DurationMinutes = &v
	}
	if venue.Valid {
		value.VenueResourceID = &venue.String
	}
	return value, mapOperationsNotFound(err)
}

func loadOverrides(ctx context.Context, query interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, meetingID string) ([]operations.OccurrenceOverride, error) {
	rows, err := query.QueryContext(ctx, `SELECT meeting_id::text,occurrence_date::text,cancelled,starts_at,duration_minutes,venue_resource_id::text,reason,version FROM hhc_web.meeting_occurrence_override WHERE meeting_id=$1 ORDER BY occurrence_date`, meetingID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []operations.OccurrenceOverride{}
	for rows.Next() {
		value, err := scanOverride(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func meetingMutation(meeting operations.Meeting, overrides []operations.OccurrenceOverride, now time.Time) operations.MeetingMutation {
	result := operations.MeetingMutation{Meeting: meeting}
	values, err := operations.ResolveOccurrences(meeting, overrides, now, now.AddDate(1, 0, 0))
	if err == nil && len(values) > 0 {
		result.NextOccurrence = &values[0]
	}
	return result
}

func (r *OperationsRepository) insertAudit(ctx context.Context, tx *sql.Tx, resourceType, resourceID, action, actor, requestID string, before, after any, now time.Time) error {
	beforeJSON, err := operationsJSON(before)
	if err != nil {
		return err
	}
	afterJSON, err := operationsJSON(after)
	if err != nil {
		return err
	}
	query := interface {
		ExecContext(context.Context, string, ...any) (sql.Result, error)
	}(r.db)
	if tx != nil {
		query = tx
	}
	_, err = query.ExecContext(ctx, `INSERT INTO hhc_web.operations_audit(id,resource_type,resource_id,action,actor,request_id,before_json,after_json,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, platform.NewID(), resourceType, resourceID, action, actor, requestID, beforeJSON, afterJSON, now)
	return err
}

func operationsJSON(value any) (any, error) {
	if value == nil {
		return nil, nil
	}
	return json.Marshal(value)
}

func weekdayNumbers(days []time.Weekday) []int16 {
	if len(days) == 0 {
		return nil
	}
	values := make([]int16, len(days))
	for index, day := range days {
		values[index] = int16(day)
	}
	return values
}

func nullableTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value
}

func stringPointer(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func unionWindows(values []operations.MediaSyncWindow) []operations.MediaSyncWindow {
	if len(values) == 0 {
		return []operations.MediaSyncWindow{}
	}
	sort.Slice(values, func(i, j int) bool { return values[i].StartsAt.Before(values[j].StartsAt) })
	result := []operations.MediaSyncWindow{values[0]}
	for _, value := range values[1:] {
		last := &result[len(result)-1]
		if !value.StartsAt.After(last.EndsAt) {
			if value.EndsAt.After(last.EndsAt) {
				last.EndsAt = value.EndsAt
			}
			continue
		}
		result = append(result, value)
	}
	return result
}

func mapOperationsNotFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return operations.ErrNotFound
	}
	return err
}

func mapOperationsError(err error) error {
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "duplicate key") {
		return operations.ErrConflict
	}
	return err
}

var _ operations.Repository = (*OperationsRepository)(nil)
