CREATE TABLE hhc_web.statement_notification_batch (
 id UUID PRIMARY KEY,
 content_id UUID NOT NULL REFERENCES hhc_web.content_entry(id) ON DELETE CASCADE,
 published_version BIGINT NOT NULL,
 request_id UUID NOT NULL,
 channel TEXT NOT NULL CHECK(channel IN ('email','web_push')),
 campaign_id UUID,
 status TEXT NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','queued','cancelled','failed')),
 actor_id TEXT NOT NULL,
 preview_json JSONB NOT NULL,
 cancelled_at TIMESTAMPTZ,
 created_at TIMESTAMPTZ NOT NULL,
 updated_at TIMESTAMPTZ NOT NULL,
 UNIQUE(content_id,published_version,request_id,channel)
);
