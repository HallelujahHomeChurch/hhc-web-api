#!/usr/bin/env python3
import test_no_cloud
from contextlib import ExitStack
import json
import os
from pathlib import Path
import subprocess
import unittest
from unittest.mock import ANY, patch

import retire_cms_dapr as retire

ORIGINAL = "hhc-web-api--accepted"
CANDIDATE = "hhc-web-api--dapr-off-2345-1"
IMAGE = "example.azurecr.io/example/hhc-web-api@sha256:" + "a" * 64
HEAD = "b" * 40
ENV = {**test_no_cloud.DEPLOYMENT_CONFIG,
    "GITHUB_REF": "refs/heads/main", "GITHUB_REPOSITORY": retire.REPO,
    "CONFIRMATION": "retire-hhc-web-api-dapr", "APPLY_REQUESTED": "true",
    "GITHUB_WORKFLOW_REF": f"{retire.REPO}/.github/workflows/locations-alias.yml@refs/heads/main",
    "RESOURCE_GROUP": "example-group", "API_APP_NAME": "hhc-web-api", "DAPR_SOURCE_RELEASE_RUN": "1234",
    "GITHUB_RUN_ID": "2345", "GITHUB_RUN_ATTEMPT": "1", "GITHUB_SHA": HEAD,
    "CMS_DAPR_OFF_EXPECTED_CMS_REVISION": ORIGINAL,
}
PINS = {"CMS_DAPR_ENABLED": "false", "CMS_LOCATIONS_VERSION": "c" * 32,
        "CMS_PRIVATE_SERVICE_PREVIOUS_VERSIONS": '{"gateway":"dddddddddddddddddddddddddddddddd"}',
        "CMS_PRIVATE_SERVICE_VERSIONS": '{"gateway":"dddddddddddddddddddddddddddddddd"}'}
IDENTITY = ("/subscriptions/11111111-1111-1111-1111-111111111111/resourcegroups/example-group/"
            "providers/Microsoft.ManagedIdentity/userAssignedIdentities/hhc-web-api-identity")
ENGAGEMENT_PIN = "e" * 32


class RetirementTests(unittest.TestCase):
    def setup_fixture(self, scenario):
        state = {"latest": ORIGINAL, "ready": ORIGINAL, "dapr": True}
        changes = []
        if scenario in ("resumed", "recovery"):
            state.update(latest=CANDIDATE, ready=CANDIDATE, dapr=scenario == "recovery")

        def az(*args, query=None):
            if args == ("show",):
                self.assertEqual(scenario, "deferred")
                return {"identities": [IDENTITY], "secrets": []}
            if args == ("revision", "list"):
                return [state["ready"]]
            if args[:2] == ("secret", "set"):
                self.assertEqual(args[2:], ("--secrets", "engagement-cms-private-token=keyvaultref:"
                    f"https://example-engagement-kv.vault.azure.net/secrets/engagement-cms-private-token/{ENGAGEMENT_PIN},"
                    f"identityref:{IDENTITY}"))
                changes.append("secret")
                return None
            if args[:2] == ("replica", "list"):
                self.assertEqual(args[3], ORIGINAL)
                return 0
            if args[:2] == ("revision", "copy"):
                self.assertEqual(args[args.index("--image") + 1], IMAGE)
                self.assertEqual(args[args.index("--from-revision") + 1], ORIGINAL)
                if scenario == "deferred":
                    self.assertIn("ENGAGEMENT_CMS_PRIVATE_TOKEN=secretref:engagement-cms-private-token", args)
                name = "hhc-web-api--" + args[args.index("--revision-suffix") + 1]
                state.update(latest=name, ready=name)
                changes.append("revision")
                return None
            if args[:2] == ("dapr", "disable"):
                self.assertEqual(state["ready"], CANDIDATE)
                state["dapr"] = False
                changes.append("disable")
                return None
            if args[:2] == ("dapr", "enable"):
                state["dapr"] = True
                changes.append("enable")
                return None
            raise AssertionError(f"unexpected command {args}")

        def capture(revision, flags, *, staged=False):
            self.assertEqual(flags["CMS_DAPR_ENABLED"], "true")
            self.assertEqual(revision, CANDIDATE if staged else ORIGINAL)
            if scenario == "stale":
                raise ValueError("stale caller binding")
            if scenario == "deferred":
                desired = json.dumps({"gateway": "d" * 32, "engagementOutbound": ENGAGEMENT_PIN})
                return {**PINS, "CMS_PRIVATE_SERVICE_VERSIONS": desired,
                        "CMS_PRIVATE_SERVICE_PREVIOUS_VERSIONS": desired if staged else PINS["CMS_PRIVATE_SERVICE_VERSIONS"]}
            return PINS

        def verify(runtime, revision):
            self.assertEqual(runtime["IMAGE_REF"], IMAGE)
            if scenario == "unknown" and revision == CANDIDATE:
                return 20
            if scenario == "unhealthy" and revision == CANDIDATE and not state["dapr"]:
                return 10
            return 0

        def revision_state(revision):
            flags = {
                "RELEASE": HEAD, "BULLETIN_AUTO_EXTRACTION_ENABLED": "true",
                "CMS_DAPR_ENABLED": "true" if revision == ORIGINAL else "false",
                "AUDIT_PRIVATE_HTTPS": "true", "ASSET_PRIVATE_HTTPS": "true",
            }
            return {"image": IMAGE, "cpu": "0.5", "memory": "1Gi",
                    "env": [{"name": name, "value": value} for name, value in flags.items()]}

        context = ExitStack()
        self.addCleanup(context.close)
        for name, func in {
            "az": az, "capture": capture, "verify_runtime": verify, "revision_state": revision_state,
            "app_state": lambda: state.copy(), "api": lambda *_: {"id": 1234},
            "evidence": lambda *_: {"head": HEAD, "apiDigest": IMAGE.split("@")[1]},
        }.items():
            context.enter_context(patch.object(retire, name, side_effect=func))
        context.enter_context(patch.dict(os.environ, ENV, clear=True))
        self.command = context.enter_context(patch.object(retire.subprocess, "run", return_value=subprocess.CompletedProcess([], 0)))
        return state, changes

    def test_retirement_dispatch_uses_existing_configuration_workflow(self):
        workflow = Path(".github/workflows/locations-alias.yml").read_text()
        alias = workflow.split("\n  locations_alias:\n", 1)[1].split("    runs-on:", 1)[0]
        self.assertIn("inputs.transition != 'retire-dapr'", alias)
        job = workflow.split("\n  retire_cms_dapr:\n", 1)[1]
        self.assertIn("python3 scripts/retire_cms_dapr.py", job)
        for unrelated in ("docker build", "az acr build", "deployment group create", "containerapp job"):
            self.assertNotIn(unrelated, job)
        self.assertNotIn("retire_cms_dapr", Path(".github/workflows/release.yml").read_text().replace("retire_cms_dapr.py", ""))

    def test_same_image_retirement(self):
        state, changes = self.setup_fixture("normal")
        retire.main()
        self.assertEqual(changes, ["revision", "disable"])
        self.assertFalse(state["dapr"])
        self.command.assert_any_call(["./scripts/smoke-release.sh"], env=ANY, check=True, timeout=180)

    def test_retry_already_retired_same_run(self):
        _, changes = self.setup_fixture("resumed")
        retire.main()
        self.assertEqual(changes, [])

    def test_new_owner_run_recovers_exact_failed_candidate(self):
        state, changes = self.setup_fixture("recovery")
        prior = {"id": 2345, "repository": {"full_name": retire.REPO},
                 "path": ".github/workflows/locations-alias.yml", "event": "workflow_dispatch",
                 "head_branch": "main", "head_sha": HEAD, "run_attempt": 1,
                 "status": "completed", "conclusion": "failure"}
        with patch.dict(os.environ, GITHUB_RUN_ID="3456", DAPR_RECOVERY_RUN="2345"), \
                patch.object(retire, "api", side_effect=lambda _, path: prior if path.endswith("2345") else {"id": 1234}):
            retire.main()
        self.assertEqual(changes, ["disable"])
        self.assertFalse(state["dapr"])
        self.command.assert_any_call(["git", "merge-base", "--is-ancestor", HEAD, HEAD], check=True, timeout=30)

    def test_recovery_rejects_wrong_live_candidate_before_mutation(self):
        _, changes = self.setup_fixture("normal")
        with patch.dict(os.environ, DAPR_RECOVERY_RUN="123"), \
                patch.object(retire, "recovery_candidate", return_value=CANDIDATE):
            with self.assertRaisesRegex(ValueError, "exact recovery candidate"):
                retire.main()
        self.assertEqual(changes, [])

    def test_candidate_resources_must_match_original(self):
        _, changes = self.setup_fixture("resumed")
        original_reader = retire.revision_state.side_effect
        def changed(revision):
            result = original_reader(revision)
            if revision == CANDIDATE:
                result["cpu"] = "1.0"
            return result
        with patch.object(retire, "revision_state", side_effect=changed):
            with self.assertRaisesRegex(ValueError, "accepted direct runtime"):
                retire.main()
        self.assertEqual(changes, [])

    def test_recovery_run_requires_authenticated_failed_main_owner(self):
        valid = {"id": 123, "repository": {"full_name": retire.REPO},
                 "path": ".github/workflows/locations-alias.yml", "event": "workflow_dispatch",
                 "head_branch": "main", "head_sha": HEAD, "run_attempt": 2,
                 "status": "completed", "conclusion": "failure"}
        with patch.dict(os.environ, ENV, clear=True):
            for key, value in (("id", 124), ("repository", {"full_name": "other/repo"}),
                               ("path", ".github/workflows/release.yml"), ("event", "push"),
                               ("head_branch", "feature"), ("conclusion", "success"),
                               ("status", "in_progress"), ("head_sha", "invalid"), ("run_attempt", 0)):
                with self.subTest(key=key), patch.object(retire, "api", return_value=valid | {key: value}), \
                        patch.object(retire.subprocess, "run") as command:
                    with self.assertRaises(ValueError):
                        retire.recovery_candidate("123", HEAD)
                    command.assert_not_called()
            for failed_check in (0, 1):
                results = [subprocess.CompletedProcess([], 0)] * failed_check + [subprocess.CalledProcessError(1, "git")]
                with patch.object(retire, "api", return_value=valid), \
                        patch.object(retire.subprocess, "run", side_effect=results):
                    with self.assertRaises(subprocess.CalledProcessError):
                        retire.recovery_candidate("123", HEAD)

    def test_deferred_outbound_uses_accepted_image(self):
        state, changes = self.setup_fixture("deferred")
        with patch.dict(os.environ, ENGAGEMENT_CMS_PRIVATE_TOKEN_VERSION=ENGAGEMENT_PIN):
            retire.main()
        self.assertEqual(changes, ["secret", "revision", "disable"])
        self.assertFalse(state["dapr"])

    def test_stale_binding_does_not_mutate(self):
        _, changes = self.setup_fixture("stale")
        with self.assertRaisesRegex(ValueError, "stale caller"):
            retire.main()
        self.assertEqual(changes, [])

    def test_unknown_candidate_keeps_sidecar(self):
        state, changes = self.setup_fixture("unknown")
        with self.assertRaisesRegex(ValueError, "unavailable"):
            retire.main()
        self.assertTrue(state["dapr"])
        self.assertEqual(changes, ["revision"])

    def test_confirmed_negative_restores_prior_runtime(self):
        state, changes = self.setup_fixture("unhealthy")
        with self.assertRaisesRegex(ValueError, "rolled back"):
            retire.main()
        self.assertTrue(state["dapr"])
        self.assertEqual(changes, ["revision", "disable", "enable", "revision"])


if __name__ == "__main__":
    unittest.main()
