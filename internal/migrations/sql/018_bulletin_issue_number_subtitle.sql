ALTER TABLE hhc_web.bulletin_issue ADD COLUMN issue_number integer;
ALTER TABLE hhc_web.bulletin_issue ADD CONSTRAINT bulletin_issue_number_positive CHECK (issue_number IS NULL OR issue_number > 0);
CREATE UNIQUE INDEX bulletin_issue_number_unique_idx ON hhc_web.bulletin_issue(issue_number) WHERE issue_number IS NOT NULL;
CREATE INDEX bulletin_issue_status_number_idx ON hhc_web.bulletin_issue(status, issue_number DESC NULLS LAST, issue_date DESC);

ALTER TABLE hhc_web.bulletin_version ADD COLUMN subtitle text NOT NULL DEFAULT '';

WITH candidates AS (
  SELECT issue_id, max((regexp_match(pdf_file_name, '([0-9]+)期'))[1]::integer) AS issue_number
  FROM hhc_web.bulletin_version
  WHERE pdf_file_name ~ '[0-9]+期'
  GROUP BY issue_id
), inferred AS (
  SELECT issue_id, issue_number
  FROM candidates
  WHERE issue_number IN (
    SELECT issue_number FROM candidates GROUP BY issue_number HAVING count(*) = 1
  )
)
UPDATE hhc_web.bulletin_issue issue
SET issue_number = inferred.issue_number
FROM inferred
WHERE issue.id = inferred.issue_id
  AND issue.issue_number IS NULL;

UPDATE hhc_web.public_projection projection
SET payload_json = projection.payload_json || jsonb_build_object(
      'issueNumber', issue.issue_number,
      'subtitle', version.subtitle,
      'downloadFileName', concat(COALESCE(issue.issue_number::text, issue.issue_date::text), '-', version.title, '.pdf')
    ),
    updated_at = now()
FROM hhc_web.bulletin_issue issue
JOIN hhc_web.bulletin_version version ON version.issue_id = issue.id
WHERE projection.resource_type IN ('bulletin_issue', 'bulletin_latest')
  AND projection.resource_id = issue.id
  AND projection.locale = version.locale;

UPDATE hhc_web.public_projection
SET etag = '"' || md5(payload_json::text) || '"'
WHERE resource_type IN ('bulletin_issue', 'bulletin_latest');
