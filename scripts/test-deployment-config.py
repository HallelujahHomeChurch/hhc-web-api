import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from test_no_cloud import DEPLOYMENT_CONFIG as CONFIG


DIGEST = 'sha256:' + 'a' * 64


class DeploymentConfigTests(unittest.TestCase):
    def command(self, *args, config=None):
        return subprocess.run(
            [sys.executable, 'scripts/deployment_config.py', *args],
            env=dict(PATH=os.environ['PATH'], **(CONFIG if config is None else config)),
            capture_output=True, text=True,
        )

    def test_private_parameters_are_written_only_to_requested_file(self):
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory) / 'parameters.json'
            result = self.command('parameters', 'infra/main.bicep', str(target))
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(result.stdout, '')
            params = json.loads(target.read_text())['parameters']
            self.assertEqual(params['runtimeKeyVaultName'], {'value': CONFIG['RUNTIME_KEY_VAULT_NAME']})
            self.assertEqual(params['containerRegistryName'], {'value': CONFIG['ACR_NAME']})
            self.assertEqual(params['queueStorageAccountName'], {'value': CONFIG['QUEUE_STORAGE_ACCOUNT_NAME']})
            self.assertEqual(target.stat().st_mode & 0o777, 0o600)

    def test_missing_or_invalid_target_fails_without_writing_or_disclosing_values(self):
        for name, bad in [('RUNTIME_KEY_VAULT_NAME', ''), ('ACR_NAME', 'other/registry'),
                          ('QUEUE_STORAGE_ACCOUNT_NAME', 'invalid storage')]:
            with self.subTest(name=name), tempfile.TemporaryDirectory() as directory:
                target = Path(directory) / 'parameters.json'
                result = self.command('parameters', 'infra/main.bicep', str(target), config={**CONFIG, name: bad})
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse(target.exists())
                self.assertNotIn(bad or CONFIG['RUNTIME_KEY_VAULT_NAME'], result.stdout + result.stderr)

    def test_image_digest_roundtrip_preserves_exact_configured_target(self):
        for kind, repo in [('api', CONFIG['IMAGE_REPOSITORY']), ('extractor', CONFIG['EXTRACTOR_IMAGE_REPOSITORY'])]:
            image = CONFIG['ACR_LOGIN_SERVER'] + '/' + repo + '@' + DIGEST
            result = self.command('image-ref', kind, DIGEST)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(result.stdout.strip(), image)
            result = self.command('image-digest', kind, image)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(result.stdout.strip(), DIGEST)

    def test_wrong_registry_repository_role_and_mutable_tags_fail(self):
        for image in ['other.azurecr.io/example/hhc-web-api@' + DIGEST,
                      'example.azurecr.io/other/hhc-web-api@' + DIGEST,
                      'example.azurecr.io/example/hhc-web-bulletin-extractor@' + DIGEST,
                      'example.azurecr.io/example/hhc-web-api:latest']:
            result = self.command('image-digest', 'api', image)
            self.assertNotEqual(result.returncode, 0)
            self.assertNotIn(image, result.stdout + result.stderr)

    def test_shared_rollback_accepts_only_the_configured_registry_namespace(self):
        image = CONFIG['ACR_LOGIN_SERVER'] + '/example/api-gateway@' + DIGEST
        result = self.command('image-digest', 'any', image)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout.strip(), DIGEST)
        result = self.command('image-digest', 'any', image.replace('/example/', '/other/'))
        self.assertNotEqual(result.returncode, 0)

    def test_environment_identifiers_have_no_bicep_defaults(self):
        for name in ['containerAppEnvironmentName', 'containerRegistryName', 'runtimeKeyVaultName',
                     'migrationKeyVaultName', 'legacyKeyVaultName', 'queueStorageAccountName', 'azureOpenAIAccountName']:
            with self.subTest(name=name):
                text = Path('infra/main.bicep').read_text()
                self.assertRegex(text, r'(?m)^param ' + name + r' string\s*$')

    def test_resource_scope_requires_configured_subscription_and_group(self):
        result = self.command('scope')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout.strip(), '/subscriptions/' + CONFIG['AZURE_SUBSCRIPTION_ID'] + '/resourceGroups/' + CONFIG['RESOURCE_GROUP'])
        result = self.command('scope', config={**CONFIG, 'AZURE_SUBSCRIPTION_ID': ''})
        self.assertNotEqual(result.returncode, 0)

    def test_preview_targets_preserve_literal_registry_matching(self):
        result = self.command('targets')
        self.assertEqual(result.returncode, 0, result.stderr)
        targets = json.loads(result.stdout)
        self.assertEqual(targets['RUNTIME_KEY_VAULT_NAME'], CONFIG['RUNTIME_KEY_VAULT_NAME'])
        image = CONFIG['ACR_LOGIN_SERVER'] + '/' + CONFIG['IMAGE_REPOSITORY'] + '@' + DIGEST
        self.assertRegex(image, targets['apiImagePattern'])
        self.assertNotRegex(image.replace('.azurecr.', 'XazurecrX'), targets['apiImagePattern'])


if __name__ == '__main__':
    unittest.main()
