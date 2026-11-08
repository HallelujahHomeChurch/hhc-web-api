CREATE INDEX CONCURRENTLY IF NOT EXISTS member_recording_comment_direct_children_idx
ON hhc_web.member_recording_comment
  (recording_id, (COALESCE(reply_to_id,parent_id)), created_at, id)
WHERE parent_id IS NOT NULL;
