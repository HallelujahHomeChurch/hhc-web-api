UPDATE hhc_web.cms_audit_event
SET payload_json = payload_json - 'reason'
WHERE action = 'bulletin_watermark.lookup' AND payload_json ? 'reason';

ALTER TABLE hhc_web.bulletin_watermark_investigation DROP COLUMN reason;
ALTER TABLE hhc_web.bulletin_watermark_investigation DROP COLUMN page;
