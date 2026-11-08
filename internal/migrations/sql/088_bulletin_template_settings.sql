-- Private church site settings; changing them never changes a public projection.
DO $bulletin_settings$
BEGIN
-- Preserve databases that applied the original concurrent-release filename.
IF EXISTS (SELECT 1 FROM public.schema_migrations WHERE version='sql/087_bulletin_template_settings.sql') THEN
  RETURN;
END IF;
CREATE TABLE hhc_web.bulletin_template_settings (
  id boolean PRIMARY KEY DEFAULT true CHECK (id),
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  text_json jsonb NOT NULL CHECK (jsonb_typeof(text_json)='object')
);
INSERT INTO hhc_web.bulletin_template_settings(text_json) VALUES
  ('{"visionMission":"遍地華人興起、福音傳到地極","visionFellowship":"共同生活、愛與成全、恩膏傳承","visionCommitment":"宣教主導、靈恩神學、團隊事奉、門徒訓練"}');
CREATE TABLE hhc_web.bulletin_template_settings_revision (
  version bigint PRIMARY KEY CHECK (version > 0),
  text_json jsonb NOT NULL CHECK (jsonb_typeof(text_json)='object'),
  created_by text NOT NULL,
  created_at timestamptz NOT NULL
);
INSERT INTO hhc_web.bulletin_template_settings_revision(version,text_json,created_by,created_at)
  SELECT version,text_json,'migration',now() FROM hhc_web.bulletin_template_settings;
ALTER TABLE hhc_web.online_bulletin_extraction_job ADD COLUMN template_snapshot_json jsonb
  CHECK (template_snapshot_json IS NULL OR jsonb_typeof(template_snapshot_json)='object');
END;
$bulletin_settings$;
