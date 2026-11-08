CREATE TABLE hhc_web.account_identity_projection (
  account_user_id uuid PRIMARY KEY,
  display_name text NOT NULL DEFAULT '',
  email text NOT NULL DEFAULT '',
  available boolean NOT NULL DEFAULT false,
  refreshed_at timestamptz NOT NULL
);

CREATE INDEX account_identity_projection_refresh_idx
  ON hhc_web.account_identity_projection(refreshed_at,account_user_id);
