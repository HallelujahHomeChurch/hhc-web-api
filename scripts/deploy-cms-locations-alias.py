#!/usr/bin/env python3
"""Verify and transition only the serving CMS locations alias configuration."""
from concurrent.futures import ThreadPoolExecutor
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile
from urllib.parse import urlsplit

from extractor_evidence import REPO, api, evidence
from deployment_config import image_ref, required, valid_image, write_parameters

APP = "hhc-web-api"
GROUP = required("RESOURCE_GROUP")
VAULT_URI = "https://" + required("RUNTIME_KEY_VAULT_NAME") + ".vault.azure.net/secrets/cms-gateway-locations-token/"
FLAGS = {
    "release": "RELEASE",
    "bulletinWatermarkPrivacyVersion": "BULLETIN_WATERMARK_PRIVACY_VERSION",
    "bulletinAutoExtractionEnabled": "BULLETIN_AUTO_EXTRACTION_ENABLED",
    "cmsTranslationEnabled": "CMS_TRANSLATION_ENABLED",
    "bulletinLLMExtractionEnabled": "BULLETIN_LLM_EXTRACTION_ENABLED",
    "azureOpenAIEndpoint": "AZURE_OPENAI_ENDPOINT",
    "azureOpenAIDeployment": "AZURE_OPENAI_DEPLOYMENT",
    "videoMediaBaseURL": "VIDEO_MEDIA_BASE_URL",
    "broadcastWritersEnabled": "BROADCAST_WRITERS_ENABLED",
    "studioWritersEnabled": "STUDIO_WRITERS_ENABLED",
    "streamKeyEncryptionID": "STREAM_KEY_ENCRYPTION_ID",
    "videoInteractionsEnabled": "VIDEO_INTERACTIONS_ENABLED",
    "videoAnalyticsEnabled": "VIDEO_ANALYTICS_ENABLED",
    "auditPrivateHTTPS": "AUDIT_PRIVATE_HTTPS",
    "assetPrivateHTTPS": "ASSET_PRIVATE_HTTPS",
}
REQUIRED = {
    "release", "bulletinWatermarkPrivacyVersion", "bulletinAutoExtractionEnabled",
    "cmsTranslationEnabled", "broadcastWritersEnabled", "studioWritersEnabled",
    "videoInteractionsEnabled", "videoAnalyticsEnabled",
}


def command(args, *, repo, env=None, stdin=None, required=True):
    result = subprocess.run(
        args, cwd=repo, env=env, input=stdin, capture_output=True, timeout=180,
    )
    if required and result.returncode:
        raise ValueError(f"{' '.join(args[:3])} failed ({result.returncode})")
    return result


def az(repo, *args):
    return command(["az", *args, "-o", "json", "--only-show-errors"], repo=repo).stdout


def capture(repo, revision, transition, version):
    with tempfile.TemporaryDirectory() as work:
        output = Path(work) / "state.env"
        command(
            ["./scripts/cms-locations-release.sh", "capture", revision], repo=repo,
            env=dict(os.environ, API_APP_NAME=APP, RESOURCE_GROUP=GROUP, GITHUB_ENV=str(output),
                     CMS_LOCATIONS_TRANSITION=transition,
                     CMS_LOCATIONS_EXPECTED_REVISION=revision if transition != "preserve" else "",
                     CMS_LOCATIONS_REQUESTED_VERSION=version),
        )
        return dict(line.split("=", 1) for line in output.read_text().splitlines())


def require_gateway_dapr(repo):
    active = json.loads(az(repo, "containerapp", "revision", "list", "-g", GROUP,
                           "-n", "api-gateway", "--all", "--query", "[?properties.active].name"))
    if not isinstance(active, list) or not active:
        raise ValueError("cannot establish active Gateway revisions")
    for revision in active:
        if not re.fullmatch(r"api-gateway--[a-z0-9-]+", revision):
            raise ValueError("unexpected Gateway revision identity")
        bindings = json.loads(az(
            repo, "containerapp", "revision", "show", "-g", GROUP, "-n", "api-gateway",
            "--revision", revision, "--query",
            "properties.template.containers[].env[?name=='CMS_GATEWAY_LOCATIONS_TOKEN'].name",
        ))
        if not isinstance(bindings, list) or not bindings or any(binding != [] for binding in bindings):
            raise ValueError("restore every active Gateway revision to Dapr before disabling CMS alias")


def properties(deltas, prefix=""):
    for delta in deltas or []:
        path = prefix + delta["path"]
        yield path, delta
        yield from properties(delta.get("children"), path + ".")


def clean(value):
    if isinstance(value, dict):
        return {key: clean(item) for key, item in value.items() if item is not None and item not in ([], {})}
    if isinstance(value, list):
        return [clean(item) for item in value]
    return value


def runtime_normalizations(change, scope, version, intent, suffix, client_id):
    """Reject every leaf outside the alias delta or exact owner normalizations."""
    before = change["before"]["properties"]
    after = change["after"]["properties"]
    old_container, = before["template"]["containers"]
    new_container, = after["template"]["containers"]
    vault_paths = {
        "database-url-v2": (required("RUNTIME_KEY_VAULT_NAME"), "database-url"),
        "azure-openai-api-key": (required("RUNTIME_KEY_VAULT_NAME"), "hhc-web-azure-openai-api-key"),
        "audit-token": (required("LEGACY_KEY_VAULT_NAME"), "audit-log-production-token-hhc-web-api"),
        "video-watermark-key": (required("RUNTIME_KEY_VAULT_NAME"), "member-video-watermark-key-hex"),
        "stream-key-encryption-keys": (required("RUNTIME_KEY_VAULT_NAME"), "studio-stream-key-encryption-keys"),
        "database-url": (required("LEGACY_KEY_VAULT_NAME"), "hhc-web-database-url"),
    }
    identity = f"{scope}/providers/Microsoft.ManagedIdentity/userAssignedIdentities/hhc-web-api-identity"
    for path, delta in properties(change.get("delta")):
        if delta.get("children"):
            continue
        kind, old, new = delta["propertyChangeType"], delta.get("before"), delta.get("after")
        if path == "properties.template.revisionSuffix":
            if kind in ("Create", "Modify") and new == suffix:
                continue
        elif path == "properties.configuration.registries.0.server":
            expr = f"[reference('{scope}/providers/Microsoft.ContainerRegistry/registries/{required('ACR_NAME')}', '2023-07-01').loginServer]"
            if kind == "Modify" and old == required("ACR_LOGIN_SERVER") and new == expr:
                continue
        elif match := re.fullmatch(r"properties\.configuration\.secrets\.([0-9]+)\.keyVaultUrl", path):
            index = int(match[1])
            old_secret = before["configuration"]["secrets"][index]
            new_secret = after["configuration"]["secrets"][index]
            name = old_secret["name"]
            if name != new_secret["name"] or kind != "Modify":
                return False
            if name == "cms-gateway-locations-token" and version:
                vault, item = required("RUNTIME_KEY_VAULT_NAME"), f"cms-gateway-locations-token/{version}"
                expr = (f"[format('{{0}}secrets/cms-gateway-locations-token/{{1}}', reference('{scope}/providers/"
                        f"Microsoft.KeyVault/vaults/{vault}', '2026-02-01').vaultUri, '{version}')]")
            else:
                if name not in vault_paths:
                    return False
                vault, item = vault_paths[name]
                expr = (f"[format('{{0}}secrets/{item}', reference('{scope}/providers/"
                        f"Microsoft.KeyVault/vaults/{vault}', '2026-02-01').vaultUri)]")
            if old == f"https://{vault}.vault.azure.net/secrets/{item}" and new == expr:
                continue
        elif match := re.fullmatch(r"properties\.template\.containers\.0\.env\.([0-9]+)\.value", path):
            index = int(match[1])
            expr = f"[reference('{identity}', '2023-01-31').clientId]"
            if (kind == "Modify" and old == client_id and new == expr
                    and old_container["env"][index]["name"] == new_container["env"][index]["name"] == "AZURE_CLIENT_ID"):
                continue
        elif path.startswith("properties.template.containers.0.probes."):
            old_probes = old_container["probes"]
            new_probes = new_container["probes"]
            if (kind == "Modify" and len(old_probes) == len(new_probes) == 3
                    and {probe["type"]: clean(probe) for probe in old_probes}
                    == {probe["type"]: clean(probe) for probe in new_probes}):
                continue
        elif intent == "enable" and kind == "Create" and re.fullmatch(
                r"properties\.(configuration\.secrets|template\.containers\.0\.env)\.[0-9]+", path):
            continue
        elif intent == "disable" and kind == "Delete" and re.fullmatch(
                r"properties\.template\.containers\.0\.env\.[0-9]+", path):
            continue
        return False
    return True


def preview(repo, shared, enabled, version, intent, suffix, client_id):
    raw = az(
        repo, "deployment", "group", "what-if", "-g", GROUP,
        "--template-file", "infra/main.bicep", "--validation-level", "Provider",
        "--result-format", "FullResourcePayloads", "--no-pretty-print", "--parameters",
        *shared, f"enableGatewayLocationsAlias={enabled}",
        f"gatewayLocationsSecretVersion={version}",
    )
    policy = command(
        ["./scripts/check-what-if.sh", "/dev/stdin", "preview", intent, version],
        repo=repo, stdin=raw, required=False,
    ).returncode == 0
    changes = []
    proof = None
    normalizations = True
    for change in json.loads(raw)["changes"]:
        if change["changeType"] in ("Ignore", "NoChange"):
            continue
        resource = change["resourceId"]
        changes.append({
            "resource": resource.rsplit("/", 1)[-1], "type": change["changeType"],
            "paths": sorted((path, delta["propertyChangeType"]) for path, delta in properties(change.get("delta"))),
        })
        scope = re.split(r"/providers/", resource, flags=re.IGNORECASE)[0]
        if resource.lower().endswith("/providers/microsoft.app/containerapps/hhc-web-api") and change["changeType"] == "Modify":
            normalizations = normalizations and runtime_normalizations(change, scope, version, intent, suffix, client_id)
        else:
            normalizations = False
        if resource.lower().endswith("/providers/microsoft.app/containerapps/hhc-web-api") and version:
            after = (change.get("after") or {}).get("properties", {})
            secrets = [ref for ref in after.get("configuration", {}).get("secrets", [])
                       if ref.get("name") == "cms-gateway-locations-token"]
            containers = after.get("template", {}).get("containers", [])
            bindings = [entry for container in containers for entry in container.get("env", [])
                        if entry.get("name") == "CMS_GATEWAY_LOCATIONS_TOKEN"]
            identity = f"{scope}/providers/Microsoft.ManagedIdentity/userAssignedIdentities/hhc-web-api-identity"
            uri_expression = (
                "[format('{0}secrets/cms-gateway-locations-token/{1}', "
                f"reference('{scope}/providers/Microsoft.KeyVault/vaults/{required('RUNTIME_KEY_VAULT_NAME')}', "
                f"'2026-02-01').vaultUri, '{version}')]"
            )
            proof = {
                "nameMatches": len(secrets) == 1,
                "pinnedUriMatches": len(secrets) == 1 and secrets[0].get("keyVaultUrl") in (VAULT_URI + version, uri_expression),
                "identityMatches": len(secrets) == 1 and (secrets[0].get("identity") or "").lower() == identity.lower(),
                "noInlineSecret": len(secrets) == 1 and secrets[0].get("value") is None,
                "bindingMatches": len(containers) == 1 and containers[0].get("name") == APP and (
                    not bindings if enabled == "false" else len(bindings) == 1
                    and bindings[0].get("secretRef") == "cms-gateway-locations-token"
                    and bindings[0].get("value") is None
                ),
            }
    return {"policyPassed": policy, "normalizationsPassed": normalizations, "aliasReferenceProof": proof, "changes": changes}


def main(operation):
    if (operation not in ("preview", "apply") or os.environ.get("GITHUB_ACTIONS") != "true"
            or os.environ.get("GITHUB_EVENT_NAME") != "workflow_dispatch"
            or os.environ.get("GITHUB_WORKFLOW_REF") != f"{REPO}/.github/workflows/locations-alias.yml@refs/heads/main"):
        raise ValueError("owning main-branch workflow required")
    repo = Path.cwd()
    revision = os.environ["CMS_LOCATIONS_EXPECTED_REVISION"]
    transition = os.environ["CMS_LOCATIONS_TRANSITION"]
    version = os.environ.get("CMS_LOCATIONS_REQUESTED_VERSION", "")
    source_run = os.environ["CMS_SOURCE_RELEASE_RUN"]
    if (transition not in ("enable", "disable") or not re.fullmatch(r"hhc-web-api--[a-z0-9-]+", revision)
            or not re.fullmatch(r"[1-9][0-9]*", source_run)):
        raise ValueError("invalid reviewed transition or release identity")
    run = api(REPO, "actions/runs/" + source_run)
    if str(run["id"]) != source_run:
        raise ValueError("release run identity differs")
    release = evidence(REPO, run, "extractor-release-evidence")
    command(["git", "merge-base", "--is-ancestor", release["head"], "HEAD"], repo=repo)
    command(["git", "diff", "--exit-code", release["head"], "HEAD", "--", "infra/main.bicep"], repo=repo)
    state = capture(repo, revision, transition, version)
    if transition == "disable":
        require_gateway_dapr(repo)
    previous = state["CMS_LOCATIONS_PREVIOUS_ENABLED"], state["CMS_LOCATIONS_PREVIOUS_VERSION"]
    desired = state["CMS_LOCATIONS_ENABLED"], state["CMS_LOCATIONS_VERSION"]
    names = json.dumps(list(FLAGS.values())).replace('"', "'")
    live = json.loads(az(
        repo, "containerapp", "revision", "show", "-g", GROUP, "-n", APP, "--revision", revision,
        "--query", "{image:properties.template.containers[0].image,cpu:properties.template.containers[0].resources.cpu,"
        "memory:properties.template.containers[0].resources.memory,env:properties.template.containers[0].env"
        f"[?contains({names},name)].{{name:name,value:value}}}}",
    ))
    if (live["image"] != image_ref(release["apiDigest"], 'api')
            or not valid_image(live["image"], "api")):
        raise ValueError("live image does not match authenticated normal release")
    env = {entry["name"]: entry["value"] for entry in live["env"]}
    flags = {parameter: env[name] for parameter, name in FLAGS.items() if name in env}
    if not REQUIRED <= flags.keys() or any(not isinstance(value, str) for value in flags.values()):
        raise ValueError("incomplete non-secret runtime settings")
    if flags["release"] != release["head"]:
        raise ValueError("live RELEASE does not match authenticated normal release")
    suffix = f'locations-{os.environ["GITHUB_RUN_ID"]}-{os.environ["GITHUB_RUN_ATTEMPT"]}'
    if not re.fullmatch(r"locations-[1-9][0-9]*-[1-9][0-9]*", suffix):
        raise ValueError("invalid deployment suffix")
    runtime = dict(os.environ, APP_NAME=APP, IMAGE_REF=live["image"], CANDIDATE_REVISION=revision,
                   RUNTIME_CPU=str(live["cpu"]), RUNTIME_MEMORY=live["memory"],
                   BULLETIN_AUTO_EXTRACTION_ENABLED=flags["bulletinAutoExtractionEnabled"])
    command(["python3", "scripts/verify-release-runtime.py"], repo=repo, env=dict(runtime, GITHUB_ENV=""))
    if flags["studioWritersEnabled"] == "true" and not flags.get("streamKeyEncryptionID"):
        raise ValueError("missing enabled studio runtime setting")
    if bool(flags.get("azureOpenAIEndpoint")) != bool(flags.get("azureOpenAIDeployment")):
        raise ValueError("incomplete Azure OpenAI runtime settings")
    for parameter in ("azureOpenAIEndpoint", "videoMediaBaseURL"):
        if flags.get(parameter):
            url = urlsplit(flags[parameter])
            if url.scheme != "https" or not url.hostname or url.username or url.password or url.query or url.fragment:
                raise ValueError("unexpected runtime origin format")
    secret_names = json.loads(az(repo, "containerapp", "show", "-g", GROUP, "-n", APP,
                                 "--query", "properties.configuration.secrets[].name"))
    private_params = Path(os.environ["RUNNER_TEMP"]) / "cms-private-parameters.json"
    write_parameters("infra/main.bicep", private_params)
    shared = ["@" + str(private_params),
        f"runtimeImage={live['image']}", f"migrationImage={live['image']}",
        f"runtimeCpu={live['cpu']}", f"runtimeMemory={live['memory']}",
        f"retainLegacyRuntimeSecret={str('database-url' in secret_names).lower()}",
        "deployRuntime=true", "deployRuntimeCompanions=false", "deployMigrationJob=false", "deployWatermarkJob=false",
        f"runtimeRevisionSuffix={suffix}",
        "deployWatermarkWorker=false", "deployBulletinExtractor=false", "provisionPermissions=false",
        *(f"{name}={value}" for name, value in flags.items()),
        f"cmsPrivateServiceVersions={state.get('CMS_PRIVATE_SERVICE_VERSIONS', '{}')}",
        f"cmsDaprEnabled={state.get('CMS_DAPR_ENABLED', 'true')}",
    ]
    client_id = json.loads(az(repo, "identity", "show", "-g", GROUP, "-n", "hhc-web-api-identity", "--query", "clientId"))
    if not re.fullmatch(r"[a-f0-9-]{36}", client_id):
        raise ValueError("could not verify CMS runtime identity")
    with ThreadPoolExecutor(max_workers=2) as pool:
        control = pool.submit(preview, repo, shared, *previous, "preserve", suffix, client_id)
        candidate = pool.submit(preview, repo, shared, *desired, transition, suffix, client_id)
        control, candidate = control.result(), candidate.result()
    final = capture(repo, revision, "preserve", "")
    if (final["CMS_LOCATIONS_PREVIOUS_ENABLED"], final["CMS_LOCATIONS_PREVIOUS_VERSION"]) != previous:
        raise ValueError("live alias state changed while previewing")
    def classified(result):
        return {(change["resource"], path, kind)
                for change in result["changes"] for path, kind in change["paths"]}

    candidate_only = classified(candidate) - classified(control)
    control_only = classified(control) - classified(candidate)
    expected = {("properties.template.containers.0.env.#", "Delete" if transition == "disable" else "Create")}
    if transition == "enable" and not previous[1]:
        expected.add(("properties.configuration.secrets.#", "Create"))
    observed = {(re.sub(r"\.[0-9]+$", ".#", path), kind)
                for resource, path, kind in candidate_only if resource == APP}
    matches = (not control_only and len(candidate_only) == len(expected) and observed == expected
               and all(change["resource"] == APP and change["type"] == "Modify"
                       for result in (control, candidate) for change in result["changes"])
               and all((candidate["aliasReferenceProof"] or {}).get(key) is True
                       for key in ("nameMatches", "pinnedUriMatches", "identityMatches", "noInlineSecret", "bindingMatches")))
    print(json.dumps({
        "revision": revision, "imageDigest": live["image"].rsplit("@", 1)[1], "sourceReleaseRun": source_run,
        "transition": transition, "matchesExpectedAliasDelta": matches,
        "candidateOnly": sorted(candidate_only), "controlOnly": sorted(control_only),
        "control": control, "candidate": candidate,
    }, indent=2))
    if not (matches and all(result["policyPassed"] and result["normalizationsPassed"] for result in (control, candidate))):
        raise ValueError("runtime-only Provider preview does not match the reviewed transition")
    if operation == "apply":
        command(["python3", "scripts/verify-release-runtime.py"], repo=repo, env=dict(runtime, GITHUB_ENV=""))
        deployment = "hhc-web-" + suffix
        with Path(os.environ["GITHUB_ENV"]).open("a") as output:
            for key, value in {**state, "PREVIOUS_READY_REVISION": revision,
                               "IMAGE_REF": live["image"], "CANDIDATE_REVISION": f"{APP}--{suffix}",
                               "RUNTIME_CPU": str(live["cpu"]), "RUNTIME_MEMORY": live["memory"],
                               "BULLETIN_AUTO_EXTRACTION_ENABLED": flags["bulletinAutoExtractionEnabled"],
                               "CMS_LOCATIONS_DEPLOYMENT": deployment}.items():
                output.write(f"{key}={value}\n")
        if transition == "disable":
            require_gateway_dapr(repo)
        command(["az", "deployment", "group", "create", "-g", GROUP, "--name", deployment,
                 "--template-file", "infra/main.bicep", "--parameters", *shared,
                 f"enableGatewayLocationsAlias={desired[0]}",
                 f"gatewayLocationsSecretVersion={desired[1]}",
                 "--only-show-errors", "--output", "none"], repo=repo)


if __name__ == "__main__":
    try:
        main(sys.argv[1] if len(sys.argv) == 2 else "")
    except (AssertionError, ValueError, KeyError, TypeError, OSError, subprocess.SubprocessError) as error:
        raise SystemExit(f"CMS locations release unavailable: {error}")
