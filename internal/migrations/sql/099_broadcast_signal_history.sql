-- Observations begin at deployment; do not invent past disconnect or recovery times.
CREATE TABLE hhc_web.member_recording_broadcast_signal (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 recording_id uuid NOT NULL REFERENCES hhc_web.member_recording(id) ON DELETE CASCADE,
 phase text NOT NULL,
 signal text NOT NULL,
 observed_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX broadcast_signal_recording_idx ON hhc_web.member_recording_broadcast_signal(recording_id,id DESC);
CREATE FUNCTION hhc_web.record_broadcast_signal() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP = 'UPDATE' AND
    OLD.payload_json #>> '{View,phase}' IS NOT DISTINCT FROM NEW.payload_json #>> '{View,phase}' AND
    OLD.payload_json #>> '{View,health,state}' IS NOT DISTINCT FROM NEW.payload_json #>> '{View,health,state}' THEN
  RETURN NEW;
 END IF;
 INSERT INTO hhc_web.member_recording_broadcast_signal(recording_id,phase,signal)
 VALUES (NEW.recording_id,COALESCE(NEW.payload_json #>> '{View,phase}','unknown'),COALESCE(NEW.payload_json #>> '{View,health,state}','unknown'));
 DELETE FROM hhc_web.member_recording_broadcast_signal
 WHERE recording_id=NEW.recording_id AND id IN (
  SELECT id FROM hhc_web.member_recording_broadcast_signal WHERE recording_id=NEW.recording_id ORDER BY id DESC OFFSET 200
 );
 RETURN NEW;
END;
$$;
CREATE TRIGGER broadcast_signal_observation AFTER INSERT OR UPDATE OF payload_json
 ON hhc_web.member_recording_broadcast FOR EACH ROW EXECUTE FUNCTION hhc_web.record_broadcast_signal();
