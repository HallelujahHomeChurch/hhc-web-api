# Public Content Migration

Production always uses the embedded manifest. `MANIFEST_SHA` comes from approved preflight evidence; `--manifest` is a local-test override only.

Run `apply` only after approved inventory and plan preflight evidence, with the matching confirmation and `MANIFEST_SHA`.

```bash
/hhc-web-content-import --mode=inventory
/hhc-web-content-import --mode=plan
/hhc-web-content-import --mode=apply --confirmation=2026-08-28-public-content-foundation-v1 --expected-manifest-sha="$MANIFEST_SHA"
```

`apply` inserts drafts only. Conflicts stop the run. Roll back content by restoring a revision to draft and then publishing it; do not delete data or blobs.

## Five-locale review

| Page | zh-Hant | zh-Hans | en | ja | ko | Source paths |
| --- | --- | --- | --- | --- | --- | --- |
| Home | Approved | Approved | Approved | Approved | Approved | `src/app/[locale]/page.tsx`, `src/i18n/locales/{zh-Hant,zh-Hans,en,ja,ko}.json` |
| About | Approved | Approved | Approved | Approved | Approved | `src/app/[locale]/about/page.tsx`, `src/i18n/locales/{zh-Hant,zh-Hans,en,ja,ko}.json` |
| Privacy Policy | Approved | Approved | Approved | Approved | Approved | `src/app/[locale]/privacy-policy/page.tsx`, `src/i18n/locales/{zh-Hant,zh-Hans,en,ja,ko}.json` |
| Terms | Approved | Approved | Approved | Approved | Approved | `src/app/[locale]/terms-of-use/page.tsx`, `src/i18n/locales/{zh-Hant,zh-Hans,en,ja,ko}.json` |
| Locations | Approved | Approved | Approved | Approved | Approved | `src/app/[locale]/page.tsx`, `src/features/locations/mock-data.ts` |
