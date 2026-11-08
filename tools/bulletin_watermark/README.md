# Offline bulletin watermark engine

The offline CPU engine is integrated with CMS member PDF issuance and asynchronous
Admin screenshot investigations. `worker.py` is the private packaged entry point;
`engine.py` handles image jobs and `pdf_engine.py` preserves original PDF objects
while adding the low-alpha fingerprint overlay. No visible-code renderer or
unmarked member-download fallback is available. Release, privacy activation and
live acceptance remain gated; see [measured acceptance](../../docs/bulletin-watermark-acceptance.md).

## Reproducible runtime

The runtime requires CPython **3.12.14** on **macOS arm64** or **Linux amd64**.
Runtime identities are separate; identical pixels across platforms are not assumed.
The macOS wheel lock is `requirements.lock.txt`; the Linux CPU lock is
`requirements-linux.lock.txt`. Both include transitive dependency hashes.

| Component | macOS arm64 | Linux amd64 |
| --- | --- | --- |
| NumPy | 2.5.3 | 2.5.3 |
| Pillow | 13.0.0 | 13.0.0 |
| opencv-python-headless | 5.0.0.93 | 5.0.0.93 |
| torch | 2.14.0 | 2.14.0+cpu |
| torchvision | 0.29.0 | 0.29.0+cpu |
| TrustMark | 0.9.2 | 0.9.2 |
| pypdf | 6.21.4 | 6.21.4 |
| ReportLab | 4.4.9 | 4.4.9 |
| Poppler | 26.05.0 | 25.03.0 |

Base engine identity is
`trustmark-q1-bch5-cpu-ecc1-<macos-arm64|linux-amd64>-py3.12.14-runtime1`.
The current PDF identity appends
`-pdf-rgba3-background-1080-poppler<VERSION>-rl4.4.9-pypdf6.14.2`.
Always use the identity returned by the trusted helper; never accept a request's
arbitrary pipeline label. Detection has a separate identity,
`sift-ecc-white250-radius7-globalfit-corr090-gap015-v2`, returned only in private helper results;
changing this detector does not change issued encoder pixels or PDF identities.

The installed pypdf runtime is pinned to **6.21.4** and checked before each PDF
job. The rendering compatibility identity retains its historical `pypdf6.14.2`
suffix so new and historical receipts remain in the same candidate scope. That
suffix is an opaque output-compatibility identifier, not the installed dependency
version. Exact renderer, model, source, dimensions and reconstructed page-hash
checks remain unchanged; old receipts are never relabeled.

`testdata/pypdf-6.14.2-pages.json` freezes Linux source and marked page hashes for
all eight pages of the synthetic `matrix.pdf` and `tagged.pdf` fixtures, using
pypdf 6.14.2, Poppler 25.03.0 and the deterministic test encoder. The packaged PDF
suite reconstructs both payloads with 6.21.4 and verifies the historical hashes.
This establishes page-rendering compatibility for those fixtures, not identical
PDF serialization for arbitrary documents. Change the rendering identity only
when output compatibility changes; a dependency upgrade alone does not require it.

The Dockerfile builds the Linux runtime, installs pinned Poppler, provisions the
three model files with SHA256-checked build-time downloads, and packages the
helper at `/hhc-web-bulletin-watermark`. Runtime processing is offline. A connected
image build is not the same as allowing helper jobs to download models.

Prepare a wheelhouse on a trusted connected build machine, then install offline:

```sh
python3.12 -m venv /private/runtime
/private/runtime/bin/python -m pip install --no-index --require-hashes \
  --find-links /private/wheelhouse -r tools/bulletin_watermark/requirements.lock.txt
/private/runtime/bin/python -m pip check
```

All dependencies are pinned, including transitive dependencies. Ordinary wheels
are from PyPI. TrustMark 0.9.2 is supplied as an sdist on PyPI. Its SHA256 is
`bcaa3bc38ead1c0558df81932672f34e306efaed31389da57dbed100e236b28a`.
Build its wheel using setuptools 84.0.0 and:

```sh
SOURCE_DATE_EPOCH=315532800 python -m pip wheel --no-deps --no-build-isolation \
  trustmark-0.9.2.tar.gz --wheel-dir /private/wheelhouse
```

Expected wheel SHA256:
`a6cf804760e9ce160025c3da08a810ba04671a36d48419f7567ebcc4caa4846c`.
The build was repeated twice with identical wheel bytes. All 21 Python sources
in the sdist matched the previously evaluated upstream checkout
`2ecb73ad28d1a3f66ac9dc19e1b667711f14314f` byte for byte. TrustMark is MIT licensed;
retain its upstream license in redistributed dependency artifacts.

Provision these files from the reviewed Adobe TrustMark Q model release into a
trusted directory. Keep the directory `0555`, files `0444`, owned by the trusted
provisioner, outside the job directory. Set `HHC_WATERMARK_MODEL_DIR` to it.
No request can choose a model location or URL. The loader replaces upstream's
download method; missing, writable or mismatched files fail, never download.

| File | SHA256 |
| --- | --- |
| trustmark_Q.yaml | 43f37103f92efa8bd6b1c5902bb537cc12a981dc699fca19d9bb7de8c62d03d9 |
| encoder_Q.ckpt | dc382c3f6b4fd568b27d6fbb763d6ffc2d2587126d84afe9d5ee95b4c5d99826 |
| decoder_Q.ckpt | e3d9cea5406a26590735719f8f15cb10802b11852ae69047eaf4cf17214df781 |

`model_sha256` is SHA256 of sorted ASCII `filename sha256\n` records for these
three files. Expected aggregate:
`1c057fb5ba226593f7f11a0ca4af084a299b3faed14c1607e04dae6fee533ae0`.
No WAM, remote inference or research-script imports are used.

## Job contract

Run `python tools/bulletin_watermark/engine.py --job-dir /private/job`.
The caller owns the job directory (`0700`) and all inputs; do not allow concurrent
mutators or untrusted hard links/parent-directory writers. This path validator
is not a filesystem sandbox. The scale-to-zero worker supplies one shared render/detect slot per replica, a
180-second deadline per helper invocation, process-group cancellation and cleanup.
The packaged worker limits CPU time to 300 seconds, file size to 32 MiB, open file
descriptors to 128 and disables core dumps. Container CPU/memory limits remain the
OS containment boundary. Direct engine/PDF CLI jobs have no overall self-imposed
deadline; evaluation gives every child a bounded timeout.

`request.json` and every input are inside the job directory. Absolute paths,
traversal, symlinks and URLs are rejected. PNG/JPEG content is decoded and checked:
10 MiB per file, 20 million pixels per image, 128 million aggregate candidate
pixels, 2–64 unique candidates. Only 32-candidate sets have been evaluated.
A query above a limit fails in full; no candidates are silently omitted.

Encode request (payload must be **exactly 61 binary characters**):

```json
{"operation":"encode","source":"source.png","payload":"0000000000000000000000000000000000000000000000000000000000000","output":"marked.png"}
```

Output is PNG regardless of the chosen filename. An existing output is rejected;
a completed temporary file is atomically published without overwriting it.
Encoded PNGs above 10 MiB fail before publication, so successful outputs remain
valid candidate inputs.
Success JSON has `status: "encoded"`, `source_sha256`, `output_sha256`,
`engine_version`, and `model_sha256`. No payload is echoed. Retain the original
source bytes, separately protected payload and this metadata to reconstruct the
same candidate. CMS stores this fingerprint separately from the opaque receipt
reference; no reference code is drawn into the PDF. CMS owns the account mapping,
365-day metadata retention and 24-hour maximum raw screenshot retention. The helper
has no database or account API access.

Detect request:

```json
{
  "operation": "detect", "source": "source.png", "query": "screenshot.jpg",
  "candidates": [
    {"id":"synthetic-a","image":"a.png","source_sha256":"<source hash>","output_sha256":"<a hash>","engine_version":"<encode version>","model_sha256":"<model hash>"},
    {"id":"synthetic-b","image":"b.png","source_sha256":"<source hash>","output_sha256":"<b hash>","engine_version":"<encode version>","model_sha256":"<model hash>"}
  ]
}
```

Copy metadata from each successful encode. Every candidate must match the source
hash, image hash, dimensions, engine version and model version. Hashes bind local
artifacts, **not authenticity or candidate-set completeness**: the caller must
supply the complete applicable set. The tool cannot discover omitted receipts.
No model weights need to load for detection; candidate metadata still must match
the pinned model identity.

Results: `{"status":"matched","candidate_id":"synthetic-a"}` or
`{"status":"inconclusive"}`. An input/system error returns
`{"status":"failed","error_code":"..."}` and exits nonzero. No candidate
ranking, near match or uncalibrated probability is emitted. Matching identifies
an eligible issued copy, not proof that an account holder personally leaked it.

The detector uses SIFT/RANSAC then affine ECC. Its existing MSE path uses Gaussian
sigma 1.2, bounded exposure fit and original-only interpolation/JPEG selection;
acceptance requires baseline MSE > .01, best MSE <= 1, improvement >= .8 and
runner-up gap >= .1. An independent normalized residual-correlation path uses the
same reconstructed candidates and accepts only score >= .90 with a best/runner-up
gap >= .15. Either unique result may match; conflicting results are inconclusive.
These are experimental thresholds, not calibrated confidence percentages.

## Checks and local evaluation

Lightweight boundary tests need only the standard library and run in normal CI:

```sh
python3 -m unittest discover -s tools/bulletin_watermark -p 'test_*.py'
```

On the validated runtime with preprovisioned models, enable synthetic end-to-end
cold-process encoding, positive/unknown/missing-candidate/original and corrupt
image checks:

```sh
HHC_WATERMARK_INTEGRATION=1 HHC_WATERMARK_MODEL_DIR=/private/models \
  /private/runtime/bin/python -m unittest discover -s tools/bulletin_watermark -p 'test_*.py'
```

The direct-decoder gate uses the pinned model and a frozen manifest. It performs
an exact retained-payload lookup after BCH/ECC decode; unknown payloads stay
inconclusive and payload values are never printed in the report:

```sh
/opt/watermark/bin/python tools/bulletin_watermark/evaluate_decoder.py \
  --manifest tools/bulletin_watermark/testdata/decoder/manifest.json \
  --report docs/bulletin-watermark-decoder-evaluation.md
```

The checked-in report covers 16 deterministic transformations. It recorded three
correct matches, thirteen inconclusive results and zero false attributions at the
pinned `0.9` quality threshold. This makes decoder identity
`trustmark-q1-bch5-logitmean-rot4-v1` technically eligible for a separately
authorized default-off integration; it is not a general accuracy claim.

Keep all private documents, images and payloads outside the Git checkout. A local
evaluation manifest contains every requested case, relative job directories,
ground truth and per-child timeout:

```json
{"timeout_seconds":300,"cases":[{"id":"case-001","job_dir":"case-001","expected_candidate_id":null}]}
```

Run `python tools/bulletin_watermark/engine.py --evaluate /private/evaluate.json`.
The JSON report retains every case, outcome, duration, correctness and false
attribution. `null` ground truth denotes a negative case. For unchanged-method
regression, compare each result against the old per-case prediction separately;
a known positive may intentionally remain inconclusive in both versions.
Failure/timeout is retained as `failed` and makes the process exit nonzero.
The command does not tune thresholds or treat aggregate accuracy as acceptance.

## PDF jobs and metadata

Run `python tools/bulletin_watermark/pdf_engine.py --job-dir /private/job`, or use
the packaged worker for the same job. Provision `HHC_WATERMARK_PDFTOPPM` as a trusted
absolute executable path; requests cannot choose it. The helper checks the pinned
Poppler version and uses the pinned ReportLab font directory plus a job-local
Fontconfig cache, rather than request-supplied font paths.

```json
{"operation":"render_pdf","source":"source.pdf","output":"marked.pdf","payload":"0000000000000000000000000000000000000000000000000000000000000"}
```

Input is a regular local PDF, at most 20 MiB and 1–60 pages; output is at most
32 MiB. Encrypted, signature-bearing, unsupported geometry and malformed/truncated
PDFs fail. The fully written output is atomically published without overwriting an
existing file. Failures clean temporary files; they do not return an original PDF.

Success has `status: "encoded_pdf"`, PDF-byte `source_sha256` and `output_sha256`,
base `engine_version`, separate `pdf_engine_version`, `model_sha256`, and `pages`.
Each page entry has `page` (one-based), `width`, `height`, `source_sha256` and
`output_sha256`. These page hashes cover canonical **Pillow-serialized RGB PNG
bytes**, not raw RGB bytes or PDF bytes. The canonical render is CropBox-aware,
rotation-aware and 1080 pixels wide; Poppler's private PPM transport does not alter
the PNG hash contract. Persist all page metadata with the issuance receipt.

```json
{"operation":"templates","source":"source.pdf","query":"query.png","candidates":[{"id":"synthetic-a","payload":"0000000000000000000000000000000000000000000000000000000000000"},{"id":"synthetic-b","payload":"1000000000000000000000000000000000000000000000000000000000000"}]}
```

Templates first locate one unique PDF page from the supplied screenshot, then
reconstruct each candidate's marked page and render it back through the same
pipeline. Ambiguous page matches return `status: "inconclusive"`. A successful
response has `status: "templates"`, `source: "source.png"`,
`pdf_source_sha256`, `page`, `width`, `height`, base/PDF/model versions and
`candidates`. Each candidate has `id`, relative `image`, source/output PNG hashes,
base engine version and model hash, ready for `detect`. The helper loads the model
once for this batch. Outputs are all-or-nothing; existing destinations reject.

CMS verifies the inferred page and every reconstruction against stored receipt metadata before
calling `detect`. It supplies the complete applicable 2–64 receipt set bound to
issue/locale/revision/source Asset/hash/PDF pipeline/model. Missing sources, changed
versions, unavailable candidates or overflow fail the job; there is no truncation
or fallback to a different scope. Hash checks alone cannot establish set completeness
for callers using the standalone CLI.

Synthetic PDF tests use the pinned libraries and Poppler but need no model:

```sh
HHC_WATERMARK_PDF_TESTS=1 HHC_WATERMARK_PDFTOPPM=/private/bin/pdftoppm \
  /private/runtime/bin/python -m unittest discover -s tools/bulletin_watermark -p 'test_*.py'
```

## Evidence and remaining gates

Native macOS checks covered four private originals / 74 pages. The current
background-only compositor also passed 47 packaged tests with two
platform-specific skips. A real 20-page issue 1735 render under emulated Linux
amd64 produced a 4.5 MiB PDF in 28.4 seconds. Every source pixel below RGB 240
remained byte-identical at the 120 dpi visual comparison, so text, rules and
images received no residual modification in that check. These are local
measurements, not deployed service capacity acceptance.

The formal browser holdout matched **35/48 positives**: native PNG 12/12, native
JPEG70 10/12, 600-pixel PNG 8/12 and 600-pixel JPEG70 5/12. **144 negatives had zero
wrong attributions.** Misses remain visible in the record. The background-mask
production integration matches all 192 frozen macOS decisions. Linux browser
holdout verification remains pending; no universal crop,
compression, mobile-reader or LINE recovery claim follows from these figures.

A separate three-document browser-capture regression froze correlation thresholds
on issues 1733/1736 before one holdout run on 1735. Correlation alone identified
46/48 known captures; combined with MSE it identified 48/48. All 144 original,
unknown and missing-candidate controls remained unattributed, with no detector
conflicts. This supplemental set does not replace the formal native/600-pixel
holdout above.

Privacy publication/activation, live and Admin UI smoke, visual/readability review,
release/CI and the unresolved Trivy Lightning false-positive gate remain open.
See [acceptance](../../docs/bulletin-watermark-acceptance.md) and
[operations](../../docs/bulletin-watermark-operations.md). No visible fallback will
be added to compensate for inconclusive detection.
