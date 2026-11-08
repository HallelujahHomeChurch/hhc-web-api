CREATE TABLE hhc_web.account_cleanup_operation (
  idempotency_key text PRIMARY KEY,
  subject_ref text NOT NULL,
  record_count bigint,
  remaining_count bigint,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
