-- Reservations outlive recording deletion so published identifiers are never reused.
CREATE TABLE hhc_web.member_recording_public_id (
  recording_id uuid PRIMARY KEY,
  public_id text COLLATE "C" NOT NULL UNIQUE
    CHECK (public_id ~ '^[A-Za-z0-9_-]{11}$')
);
