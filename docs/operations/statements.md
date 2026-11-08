# Church statements

Statements reuse news drafts, revisions and published projections. Popup intervals are half-open in absolute time; CMS entry uses Asia/Taipei. Draft intervals do not affect live projections. Publishing reservations and published intervals cannot overlap. Expiry retains the standalone article; unpublishing removes it without changing an accepted notification.

Notifications use existing newsletter email and all consented Web Push audiences. Publication completion atomically stores one immutable Engagement submission containing every requested channel and its approved copy. HHC retries only that submission with the same idempotency key and does not read delivery results. Editing, unpublishing, and deleting the source never resend, cancel, or delete the notification.

## Data inventory

New publications store the immutable command only in `hhc_web.outbox_event`; no recipient addresses or push endpoints are stored. `hhc_web.statement_notification_batch` is legacy rolling-release state: new code does not insert it, its content foreign key is removed so source deletion cannot erase a committed notification, and the legacy worker remains temporarily able to drain existing rows. Campaign consent, suppression, status, deletion, and delivery retention are owned by Engagement.

The first 2026-09-07 statement must be saved as a draft only per the user's latest instruction. Do not publish or request either notification channel until separately instructed.
