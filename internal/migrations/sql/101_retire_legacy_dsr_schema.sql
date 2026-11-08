-- Later contract release, after exact DSR cleanup and reader-release acceptance.
-- The production runner owns the transaction and atomic migration-ledger write.
-- Irreversible: restoring rows requires reviewed backups and compatible code.
SET LOCAL lock_timeout = '2s';
SET LOCAL statement_timeout = '30s';
SET LOCAL search_path = pg_catalog;
LOCK TABLE hhc_web.bulletin_watermark_restriction_withdrawal,
 hhc_web.bulletin_watermark_subject, hhc_web.bulletin_watermark_dsr_action
 IN ACCESS EXCLUSIVE MODE;
DO $contract$
DECLARE fingerprint text; retired_function oid;
BEGIN
 IF EXISTS(SELECT 1 FROM hhc_web.bulletin_watermark_restriction_withdrawal)
  OR EXISTS(SELECT 1 FROM hhc_web.bulletin_watermark_subject WHERE NOT erased)
  OR EXISTS(SELECT 1 FROM hhc_web.bulletin_watermark_dsr_action WHERE action<>'erase') THEN
  RAISE EXCEPTION 'legacy DSR restrictions and withdrawals must be empty';
 END IF;
 -- Include mixed-table metadata so column-owned dependencies cannot be silently
 -- removed by ALTER TABLE. Preserved receipt fields and indexes stay unchanged.
 WITH targets AS (
  SELECT oid,relname FROM pg_class WHERE relnamespace='hhc_web'::regnamespace
   AND relname=ANY(ARRAY['bulletin_watermark_restriction_withdrawal','bulletin_watermark_subject','bulletin_watermark_dsr_action'])
 ), objects AS (
  SELECT 'column:'||t.relname||':'||a.attname||':'||format_type(a.atttypid,a.atttypmod)||':'||a.attnotnull::text||':'||COALESCE(pg_get_expr(d.adbin,d.adrelid),'') value
   FROM targets t JOIN pg_attribute a ON a.attrelid=t.oid AND a.attnum>0 AND NOT a.attisdropped LEFT JOIN pg_attrdef d ON d.adrelid=t.oid AND d.adnum=a.attnum
  UNION ALL SELECT 'constraint:'||t.relname||':'||c.conname||':'||pg_get_constraintdef(c.oid) FROM targets t JOIN pg_constraint c ON c.conrelid=t.oid
  UNION ALL SELECT 'index:'||t.relname||':'||c.relname||':'||pg_get_indexdef(i.indexrelid) FROM targets t JOIN pg_index i ON i.indrelid=t.oid JOIN pg_class c ON c.oid=i.indexrelid
  UNION ALL SELECT 'trigger:'||t.relname||':'||g.tgname||':'||g.tgenabled::text||':'||pg_get_triggerdef(g.oid) FROM targets t JOIN pg_trigger g ON g.tgrelid=t.oid AND NOT g.tgisinternal
 )
 SELECT encode(sha256(convert_to(string_agg(value,E'\n' ORDER BY value),'UTF8')),'hex') FROM objects INTO fingerprint;
 IF fingerprint IS DISTINCT FROM '82f368f6d03413a7026dcd911512e704a77845b06a47d80ee1d0e4ea7f4fc726' THEN
  RAISE EXCEPTION 'legacy DSR schema dependency drift';
 END IF;
 -- Extended statistics can disappear automatically even without CASCADE.
 -- pg_depend also covers expression statistics whose stxkeys omit the column.
 IF EXISTS(SELECT 1 FROM pg_depend d
  WHERE d.classid='pg_statistic_ext'::regclass AND d.refclassid='pg_class'::regclass
   AND (d.refobjid='hhc_web.bulletin_watermark_restriction_withdrawal'::regclass
    OR (d.refobjid='hhc_web.bulletin_watermark_subject'::regclass
     AND d.refobjsubid=(SELECT attnum FROM pg_attribute WHERE attrelid=d.refobjid AND attname='origin_tracking_complete' AND NOT attisdropped)))) THEN
  RAISE EXCEPTION 'unreviewed statistics depend on legacy DSR objects';
 END IF;
 retired_function := to_regprocedure('hhc_web.invalidate_watermark_restriction_origin()');
 SELECT encode(sha256(convert_to(pg_get_functiondef(retired_function),'UTF8')),'hex') INTO fingerprint;
 IF fingerprint IS DISTINCT FROM '0f831e38b18b8cc7b48df9da6606f13fa9ba2657d62d59e822643e0c17932e3a' THEN
  RAISE EXCEPTION 'legacy DSR function definition drift';
 END IF;
 -- Exact schema-qualified no-argument identity only; overloads remain readers.
 IF EXISTS(SELECT 1 FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
  WHERE n.nspname !~ '^pg_' AND n.nspname<>'information_schema'
   AND p.oid<>retired_function
   AND p.prosrc ~* '(bulletin_watermark_restriction_withdrawal|origin_tracking_complete|invalidate_watermark_restriction_origin)') THEN
  RAISE EXCEPTION 'unknown routine references legacy DSR objects';
 END IF;
END $contract$;
DROP TRIGGER watermark_restriction_origin_insert ON hhc_web.bulletin_watermark_dsr_action;
DROP TRIGGER watermark_restriction_origin_delete ON hhc_web.bulletin_watermark_dsr_action;
DROP FUNCTION hhc_web.invalidate_watermark_restriction_origin();
ALTER TABLE hhc_web.bulletin_watermark_subject DROP COLUMN origin_tracking_complete;
DROP TABLE hhc_web.bulletin_watermark_restriction_withdrawal;
