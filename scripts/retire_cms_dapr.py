#!/usr/bin/env python3
"""Retire the CMS sidecar on the accepted immutable API image."""
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile
import time

from extractor_evidence import REPO, api, evidence
from deployment_config import image_ref, required, valid_image

APP = "hhc-web-api"
GROUP = required("RESOURCE_GROUP")
OUTBOUND = {
    "accountOutbound": ("account-cms-private-token", "ACCOUNT_CMS_PRIVATE_TOKEN",
                        "ACCOUNT_CMS_PRIVATE_SECRET_VERSION", required("ACCOUNT_KEY_VAULT_NAME")),
    "engagementOutbound": ("engagement-cms-private-token", "ENGAGEMENT_CMS_PRIVATE_TOKEN",
                           "ENGAGEMENT_CMS_PRIVATE_TOKEN_VERSION", required("ENGAGEMENT_KEY_VAULT_NAME")),
    "operationsOutbound": ("operations-cms-private-token", "OPERATIONS_CMS_PRIVATE_TOKEN",
                           "OPERATIONS_CMS_PRIVATE_TOKEN_VERSION", required("LEGACY_KEY_VAULT_NAME")),
}


def az(*args, query=None):
    command = ["az", "containerapp", *args, "-g", GROUP, "-n", APP, "--only-show-errors"]
    if query is None:
        subprocess.run([*command, "-o", "none"], check=True, timeout=120)
        return
    return json.loads(subprocess.check_output([*command, "--query", query, "-o", "json"], timeout=60))


def app_state():
    return az("show", query="{latest:properties.latestRevisionName,ready:properties.latestReadyRevisionName,dapr:properties.configuration.dapr.enabled}")


def revision_state(revision):
    return az("revision", "show", "--revision", revision, query=(
        "{image:properties.template.containers[0].image,cpu:properties.template.containers[0].resources.cpu,"
        "memory:properties.template.containers[0].resources.memory,env:properties.template.containers[0].env"
        "[?name=='RELEASE' || name=='BULLETIN_AUTO_EXTRACTION_ENABLED' || name=='CMS_DAPR_ENABLED' || "
        "name=='AUDIT_PRIVATE_HTTPS' || name=='ASSET_PRIVATE_HTTPS'].{name:name,value:value}}"
    ))


def capture(revision, flags, *, staged=False):
    with tempfile.TemporaryDirectory() as work:
        path = Path(work) / "state"
        subprocess.run(
            ["./scripts/cms-locations-release.sh", "stage-retirement" if staged else "capture", revision],
            env=dict(os.environ, GITHUB_ENV=str(path), CMS_DAPR_ENABLED_REQUESTED="false",
                     CMS_DAPR_OFF_EXPECTED_CMS_REVISION=revision,
                     CMS_AUDIT_PRIVATE_HTTPS=flags["AUDIT_PRIVATE_HTTPS"],
                     CMS_ASSET_PRIVATE_HTTPS=flags["ASSET_PRIVATE_HTTPS"]),
            check=True, timeout=120,
        )
        return dict(line.split("=", 1) for line in path.read_text().splitlines())


def verify_runtime(runtime, revision):
    return subprocess.run(
        ["python3", "scripts/verify-release-runtime.py"],
        env=dict(runtime, CANDIDATE_REVISION=revision),
        check=False, timeout=420,
    ).returncode


def stage_outbound(state):
    previous = json.loads(state["CMS_PRIVATE_SERVICE_PREVIOUS_VERSIONS"])
    desired = json.loads(state["CMS_PRIVATE_SERVICE_VERSIONS"])
    missing = desired.keys() - previous.keys()
    if missing - OUTBOUND.keys() or any(desired.get(name) != version for name, version in previous.items()):
        raise ValueError("unexpected CMS outbound credential change")
    if not missing:
        return []
    runtime = az("show", query="{identities:keys(identity.userAssignedIdentities),"
                 "secrets:properties.configuration.secrets[].{name:name,url:keyVaultUrl,identity:identity}}")
    identities = [identity for identity in runtime["identities"] if identity.lower().endswith(
        f"/resourcegroups/{GROUP.lower()}/providers/microsoft.managedidentity/userassignedidentities/hhc-web-api-identity")]
    if len(identities) != 1:
        raise ValueError("expected exact CMS runtime identity")
    identity = identities[0]
    secrets = []
    bindings = []
    for name in sorted(missing):
        alias, env, _, vault = OUTBOUND[name]
        uri = f"https://{vault}.vault.azure.net/secrets/{alias}/{desired[name]}"
        existing = [item for item in runtime["secrets"] if item["name"] == alias]
        if existing and (len(existing) != 1 or existing[0]["url"] != uri
                         or (existing[0]["identity"] or "").lower() != identity.lower()):
            raise ValueError(f"{alias} conflicts with an existing secret reference")
        if not existing:
            secrets.append(f"{alias}=keyvaultref:{uri},identityref:{identity}")
        bindings.append(f"{env}=secretref:{alias}")
    if secrets:
        az("secret", "set", "--secrets", *secrets)
    return bindings



def recovery_candidate(recovery_run, source_head):
    if not re.fullmatch(r"[1-9][0-9]*", recovery_run) or recovery_run == os.environ["GITHUB_RUN_ID"]:
        raise ValueError("invalid recovery run identity")
    prior = api(REPO, f"actions/runs/{recovery_run}")
    if (str(prior["id"]) != recovery_run or prior["repository"]["full_name"] != REPO
            or prior["path"] != ".github/workflows/locations-alias.yml"
            or prior["event"] != "workflow_dispatch" or prior["head_branch"] != "main"
            or prior["status"] != "completed" or prior["conclusion"] != "failure"
            or not re.fullmatch(r"[0-9a-f]{40}", prior["head_sha"])
            or not re.fullmatch(r"[1-9][0-9]*", str(prior["run_attempt"]))):
        raise ValueError("recovery run is not a failed main owner retirement")
    for ancestor, descendant in ((source_head, prior["head_sha"]),
                                 (prior["head_sha"], os.environ["GITHUB_SHA"])):
        subprocess.run(["git", "merge-base", "--is-ancestor", ancestor, descendant],
                       check=True, timeout=30)
    return f"{APP}--dapr-off-{recovery_run}-{prior['run_attempt']}"


def main():
    if (os.environ["GITHUB_REF"] != "refs/heads/main" or os.environ["GITHUB_REPOSITORY"] != REPO
            or os.environ["CONFIRMATION"] != "retire-hhc-web-api-dapr"
            or os.environ["APPLY_REQUESTED"] != "true"
            or os.environ["GITHUB_WORKFLOW_REF"] != f"{REPO}/.github/workflows/locations-alias.yml@refs/heads/main"
            or os.environ["RESOURCE_GROUP"] != GROUP or os.environ["API_APP_NAME"] != APP):
        raise ValueError("invalid retirement dispatch")
    source_run, run_id, attempt = (os.environ[name] for name in
                                  ("DAPR_SOURCE_RELEASE_RUN", "GITHUB_RUN_ID", "GITHUB_RUN_ATTEMPT"))
    original = os.environ["CMS_DAPR_OFF_EXPECTED_CMS_REVISION"]
    if not all(re.fullmatch(r"[1-9][0-9]*", value) for value in (source_run, run_id, attempt)):
        raise ValueError("expected exact run identities")
    if not re.fullmatch(r"hhc-web-api--[a-z0-9-]+", original):
        raise ValueError("expected exact original ready revision")
    run = api(REPO, f"actions/runs/{source_run}")
    if str(run["id"]) != source_run:
        raise ValueError("release run identity differs")
    release = evidence(REPO, run, "extractor-release-evidence")
    subprocess.run(["git", "merge-base", "--is-ancestor", release["head"], os.environ["GITHUB_SHA"]],
                   check=True, timeout=30)
    source = revision_state(original)
    image = source["image"]
    flags = {item["name"]: item["value"] for item in source["env"]}
    if (not valid_image(image, "api")
            or image != image_ref(release["apiDigest"], 'api') or flags["RELEASE"] != release["head"]
            or any(flags[name] != "true" for name in ("CMS_DAPR_ENABLED", "AUDIT_PRIVATE_HTTPS", "ASSET_PRIVATE_HTTPS"))):
        raise ValueError("original revision is outside the accepted direct runtime")

    runtime = dict(os.environ, APP_NAME=APP, IMAGE_REF=image, GITHUB_ENV="",
                   RUNTIME_CPU=str(source["cpu"]), RUNTIME_MEMORY=source["memory"],
                   BULLETIN_AUTO_EXTRACTION_ENABLED=flags["BULLETIN_AUTO_EXTRACTION_ENABLED"])
    recovery_run = os.environ.get("DAPR_RECOVERY_RUN", "")
    recovered = recovery_candidate(recovery_run, release["head"]) if recovery_run else None
    app = app_state()
    current = app["ready"]
    if app["latest"] != current or type(app["dapr"]) is not bool:
        raise ValueError("CMS application has an unsettled revision")
    if az("revision", "list", query="[?properties.active].name") != [current]:
        raise ValueError("unexpected active CMS revision")

    def rollback(candidate):
        current_state = app_state()
        if current_state["latest"] != candidate or type(current_state["dapr"]) is not bool:
            raise ValueError("unable to verify exact recovery target")
        if current_state["dapr"] is False:
            az("dapr", "enable", "--dapr-app-id", APP, "--dapr-app-port", "8082",
               "--dapr-app-protocol", "http", "--dapr-log-level", "warn")
        suffix = f"dapr-rollback-{run_id}-{attempt}"
        az("revision", "copy", "--from-revision", original, "--image", image, "--revision-suffix", suffix)
        if verify_runtime(runtime, f"{APP}--{suffix}"):
            raise ValueError("rollback revision could not be verified")
        raise ValueError("confirmed unhealthy candidate was rolled back")

    if recovered is not None and current != recovered:
        raise ValueError("ready revision differs from the exact recovery candidate")
    if current == original and app["dapr"] is True:
        if verify_runtime(runtime, original):
            raise ValueError("original runtime is unavailable")
        state = capture(original, flags)
        if state["CMS_DAPR_ENABLED"] != "false":
            raise ValueError("sidecar retirement did not pass preflight")
        bindings = stage_outbound(state)
        suffix = f"dapr-off-{run_id}-{attempt}"
        candidate = f"{APP}--{suffix}"
        az("revision", "copy", "--from-revision", original, "--image", image, "--revision-suffix", suffix,
           "--set-env-vars", "CMS_DAPR_ENABLED=false", *bindings)
    elif current == recovered or re.fullmatch(f"{APP}--dapr-off-{run_id}-[1-9][0-9]*", current):
        candidate = current
        state = None
    else:
        raise ValueError("ready revision is outside this retirement")
    result = verify_runtime(runtime, candidate)
    if result == 10:
        rollback(candidate)
    if result:
        raise ValueError("candidate verification unavailable")
    candidate_state = revision_state(candidate)
    expected_flags = {**flags, "CMS_DAPR_ENABLED": "false"}
    if (candidate_state["image"] != image or candidate_state["cpu"] != source["cpu"]
            or candidate_state["memory"] != source["memory"] or {item["name"]: item["value"] for item in candidate_state["env"]}
            != expected_flags):
        raise ValueError("candidate differs from the accepted direct runtime")

    candidate_pins = capture(candidate, flags, staged=True)
    pins = json.loads(candidate_pins["CMS_PRIVATE_SERVICE_VERSIONS"])
    for name, (_, _, version_env, _) in OUTBOUND.items():
        requested = os.environ.get(version_env)
        if requested and pins.get(name) != requested:
            raise ValueError(f"candidate does not use the reviewed {name} pin")
    if state and (candidate_pins["CMS_PRIVATE_SERVICE_VERSIONS"] != state["CMS_PRIVATE_SERVICE_VERSIONS"]
                  or candidate_pins["CMS_LOCATIONS_VERSION"] != state["CMS_LOCATIONS_VERSION"]):
        raise ValueError("private runtime pins changed during retirement")

    current_state = app_state()
    if current_state["latest"] != candidate or current_state["ready"] != candidate or type(current_state["dapr"]) is not bool:
        raise ValueError("candidate is no longer the exact serving revision")
    if current_state["dapr"]:
        for _ in range(24):
            if az("replica", "list", "--revision", original, query="length(@)") == 0:
                break
            time.sleep(5)
        else:
            raise ValueError("original CMS revision has not drained")
        az("dapr", "disable")
    result = verify_runtime(runtime, candidate)
    if result == 10:
        rollback(candidate)
    if result:
        raise ValueError("retired runtime verification unavailable")
    subprocess.run(["./scripts/cms-locations-release.sh", "verify", candidate],
                   env=runtime | candidate_pins | {"CMS_DAPR_ENABLED": "false"}, check=True, timeout=90)
    subprocess.run(["./scripts/smoke-release.sh"], env=dict(runtime, SMOKE_MODE="forward"),
                   check=True, timeout=180)
    print("Verified CMS sidecar retirement on the accepted API image.")


if __name__ == "__main__":
    try:
        main()
    except (AssertionError, KeyError, OSError, TypeError, ValueError, subprocess.SubprocessError) as error:
        raise SystemExit(f"CMS sidecar retirement stopped: {error}")
