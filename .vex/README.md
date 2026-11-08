# Lightning 2.6.6 advisory correction

`lightning-2.6.6.openvex.json` records one fixed-version fact: **CVE-2026-58659**
is fixed in **`pkg:pypi/lightning@2.6.6`**. It does not cover other versions,
`pytorch-lightning`, other packages or other vulnerabilities. No ignore file,
severity change or scan bypass is used.

The [PyPA advisory](https://raw.githubusercontent.com/pypa/advisory-database/main/vulns/lightning/PYSEC-2026-3624.yaml)
explicitly lists 2.6.6 as fixed (modified 2026-09-10). The
[upstream fix](https://github.com/Lightning-AI/pytorch-lightning/commit/d710d689510d50e800f53b3cd773cbca20b1f86f)
adds the trusted-instantiator allowlist. Our installed 2.6.6 also rejects non-string
instantiators and class paths that do not resolve to already imported subclasses.
Trivy's observed database finding instead listed `2022.6.15` as the fixed version.

The runtime lock pins the Lightning wheel to SHA256
`2bcbd6ee840071cd076c7dc9953d7ac1040df92f1c5fa76583aae644f037ab85`.
The installed `lightning/pytorch/core/saving.py` inspected in local image
`sha256:f8e9ba7016bbc28cf5c77969286222ac8a9ef2092a1909de5eafc9a3ae14d5ad`
has SHA256 `da958b64ddedb2d589ac729df25dce647dbb395e6ed604948bd7b7e1b69a76b0`.
The same file hash was observed in the pinned macOS runtime.

`RuntimeTests.test_lightning_fixed_checkpoint_guards` checks the exact installed
version and locked wheel hash, calls the actual `_load_state` function with unsafe
instantiator/class-path inputs, asserts rejection before importing their targets,
and verifies a plain checkpoint still loads. The locked-runtime test suite runs
before the local image scan. Release reruns this test against the immutable pulled
image before scanning it, so an incompatible package does not inherit this finding
correction merely because a VEX file exists.

The pinned scanner is Trivy 0.74.0. Its
[OpenVEX implementation](https://github.com/aquasecurity/trivy/blob/v0.74.0/pkg/vex/openvex.go#L64)
supports `fixed` statements. [PURL matching documentation](https://trivy.dev/docs/dev/supply-chain/vex/file/)
explains why the explicit package version is required: omitting it matches every
version. [OpenVEX](https://github.com/openvex/spec/blob/main/OPENVEX-SPEC.md#status-labels)
defines `fixed` for versions containing a fix; no temporary risk acceptance or
custom expiry mechanism is implied here.

Image scans mount only this reviewed directory read-only and add
`--vex /vex/lightning-2.6.6.openvex.json --show-suppressed`. The full scan, existing
`HIGH,CRITICAL`, `--ignore-unfixed` and `--exit-code 1` controls remain. All other
findings retain their normal policy. The immutable-image JSON report includes
modified findings for traceability; source dependency scans remain unchanged.

Local validation against the exact image above returned:

| Statement | Exit code | Remaining fixable High/Critical findings |
| --- | ---: | ---: |
| No VEX | 1 | 1: CVE-2026-58659 / lightning 2.6.6 |
| Exact fixed statement | 0 | 0; one `fixed` modified finding retained |
| Wrong version (`2.6.5`) | 1 | The original finding remains |
| Wrong CVE | 1 | The original finding remains |

The pinned scanner stores the retained finding under
`Results[].ExperimentalModifiedFindings`. Keep the raw and VEX-applied scan
evidence outside Git. When Trivy's advisory data
correctly classifies 2.6.6, remove the redundant VEX statement and its scan flags
through normal review after verifying the raw image scan passes. Retain the
checkpoint guard regression test. Changes to the package pin require fresh review;
do not broaden this statement to another version automatically.
