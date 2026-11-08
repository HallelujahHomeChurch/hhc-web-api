ALTER TABLE hhc_web.member_recording_comment ADD COLUMN reply_to_id uuid REFERENCES hhc_web.member_recording_comment(id) ON DELETE SET NULL;
CREATE TABLE hhc_web.member_recording_comment_reaction (
 comment_id uuid NOT NULL REFERENCES hhc_web.member_recording_comment(id) ON DELETE CASCADE,
 account_id uuid NOT NULL,
 reaction text NOT NULL CHECK (reaction IN ('like','dislike')),
 updated_at timestamptz NOT NULL,
 PRIMARY KEY(comment_id,account_id)
);
CREATE INDEX recording_comment_reaction_account_idx ON hhc_web.member_recording_comment_reaction(account_id);
CREATE TABLE hhc_web.member_recording_comment_rank (
 id uuid PRIMARY KEY,
 recording_id uuid NOT NULL REFERENCES hhc_web.member_recording(id) ON DELETE CASCADE,
 account_id uuid NOT NULL,
 expires_at timestamptz NOT NULL,
 UNIQUE(recording_id,account_id)
);
CREATE INDEX recording_comment_rank_expiry_idx ON hhc_web.member_recording_comment_rank(expires_at);
CREATE INDEX recording_comment_rank_account_idx ON hhc_web.member_recording_comment_rank(account_id);
CREATE TABLE hhc_web.member_recording_comment_rank_item (
 snapshot_id uuid NOT NULL REFERENCES hhc_web.member_recording_comment_rank(id) ON DELETE CASCADE,
 ordinal bigint NOT NULL,
 comment_id uuid NOT NULL REFERENCES hhc_web.member_recording_comment(id) ON DELETE CASCADE,
 PRIMARY KEY(snapshot_id,ordinal)
);
