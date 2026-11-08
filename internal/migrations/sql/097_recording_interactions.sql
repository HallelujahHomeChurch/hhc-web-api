CREATE TABLE hhc_web.member_recording_interaction_settings (
 recording_id uuid PRIMARY KEY REFERENCES hhc_web.member_recording(id) ON DELETE CASCADE,
 comments_enabled boolean NOT NULL DEFAULT true,
 revision bigint NOT NULL DEFAULT 1 CHECK(revision>0)
);
CREATE TABLE hhc_web.member_recording_like (
 recording_id uuid NOT NULL REFERENCES hhc_web.member_recording(id) ON DELETE CASCADE,
 account_id uuid NOT NULL,
 created_at timestamptz NOT NULL,
 PRIMARY KEY(recording_id,account_id)
);
CREATE INDEX recording_like_account_idx ON hhc_web.member_recording_like(account_id);
CREATE TABLE hhc_web.member_recording_comment (
 id uuid PRIMARY KEY,
 recording_id uuid NOT NULL REFERENCES hhc_web.member_recording(id) ON DELETE CASCADE,
 parent_id uuid REFERENCES hhc_web.member_recording_comment(id) ON DELETE CASCADE,
 account_id uuid,
 mutation_id uuid,
 request_hash text,
 body text NOT NULL CHECK(char_length(body)<=2000),
 hidden boolean NOT NULL DEFAULT false,
 deleted boolean NOT NULL DEFAULT false,
 revision bigint NOT NULL DEFAULT 1 CHECK(revision>0),
 created_at timestamptz NOT NULL,
 edited_at timestamptz,
 UNIQUE(recording_id,account_id,mutation_id)
);
CREATE INDEX recording_comment_page_idx ON hhc_web.member_recording_comment(recording_id,parent_id,created_at,id);
CREATE INDEX recording_comment_account_idx ON hhc_web.member_recording_comment(account_id);
CREATE TABLE hhc_web.member_recording_comment_report (
 comment_id uuid NOT NULL REFERENCES hhc_web.member_recording_comment(id) ON DELETE CASCADE,
 account_id uuid NOT NULL,
 reason text NOT NULL CHECK(char_length(reason) BETWEEN 1 AND 500),
 created_at timestamptz NOT NULL,
 resolved_at timestamptz,
 PRIMARY KEY(comment_id,account_id)
);
CREATE INDEX recording_comment_report_account_idx ON hhc_web.member_recording_comment_report(account_id);
