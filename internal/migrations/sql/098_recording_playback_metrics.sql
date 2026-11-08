CREATE TABLE hhc_web.member_recording_metrics (
 recording_id uuid PRIMARY KEY REFERENCES hhc_web.member_recording(id) ON DELETE CASCADE,
 live_views bigint NOT NULL DEFAULT 0,
 replay_views bigint NOT NULL DEFAULT 0,
 total_views bigint NOT NULL DEFAULT 0,
 watch_ms bigint NOT NULL DEFAULT 0,
 peak_concurrent bigint NOT NULL DEFAULT 0,
 tracking_since timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE hhc_web.member_recording_playback_session (
 recording_id uuid NOT NULL REFERENCES hhc_web.member_recording(id) ON DELETE CASCADE,
 account_id uuid NOT NULL,
 session_id uuid NOT NULL,
 sequence bigint NOT NULL,
 state text NOT NULL CHECK(state IN ('playing','paused','waiting','ended')),
 position double precision NOT NULL,
 last_seen timestamptz NOT NULL,
 PRIMARY KEY(recording_id,account_id,session_id)
);
CREATE INDEX recording_playback_session_seen_idx ON hhc_web.member_recording_playback_session(last_seen);
CREATE INDEX recording_playback_session_account_idx ON hhc_web.member_recording_playback_session(account_id);
CREATE TABLE hhc_web.member_recording_playback_visit (
 recording_id uuid NOT NULL REFERENCES hhc_web.member_recording(id) ON DELETE CASCADE,
 account_id uuid NOT NULL,
 visit_id uuid NOT NULL,
 last_active timestamptz NOT NULL,
 credited_until timestamptz NOT NULL,
 live_ms bigint NOT NULL DEFAULT 0,
 replay_ms bigint NOT NULL DEFAULT 0,
 live_qualified boolean NOT NULL DEFAULT false,
 replay_qualified boolean NOT NULL DEFAULT false,
 total_qualified boolean NOT NULL DEFAULT false,
 PRIMARY KEY(recording_id,account_id)
);
CREATE INDEX recording_playback_visit_account_idx ON hhc_web.member_recording_playback_visit(account_id);
CREATE TABLE hhc_web.member_recording_viewer (
 recording_id uuid NOT NULL REFERENCES hhc_web.member_recording(id) ON DELETE CASCADE,
 account_id uuid NOT NULL,
 first_viewed_at timestamptz NOT NULL,
 PRIMARY KEY(recording_id,account_id)
);
CREATE INDEX recording_viewer_account_idx ON hhc_web.member_recording_viewer(account_id);
CREATE TABLE hhc_web.member_recording_metric_minute (
 recording_id uuid NOT NULL REFERENCES hhc_web.member_recording(id) ON DELETE CASCADE,
 minute timestamptz NOT NULL,
 peak_concurrent bigint NOT NULL,
 total_views bigint NOT NULL,
 watch_ms bigint NOT NULL,
 PRIMARY KEY(recording_id,minute)
);
