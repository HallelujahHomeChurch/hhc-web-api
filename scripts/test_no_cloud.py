"""Fail closed across Python subprocess boundaries in local unit tests."""
import atexit
import os
from pathlib import Path
import tempfile

DEPLOYMENT_CONFIG = {
    'RESOURCE_GROUP': 'example-group',
    'AZURE_SUBSCRIPTION_ID': '11111111-1111-4111-8111-111111111111',
    'ACR_NAME': 'example',
    'ACR_LOGIN_SERVER': 'example.azurecr.io',
    'IMAGE_REPOSITORY': 'example/hhc-web-api',
    'EXTRACTOR_IMAGE_REPOSITORY': 'example/hhc-web-bulletin-extractor',
    'CONTAINER_APP_ENVIRONMENT_NAME': 'example-env',
    'RUNTIME_KEY_VAULT_NAME': 'example-runtime-kv',
    'MIGRATION_KEY_VAULT_NAME': 'example-migrate-kv',
    'LEGACY_KEY_VAULT_NAME': 'example-legacy-kv',
    'ACCOUNT_KEY_VAULT_NAME': 'example-account-kv',
    'ENGAGEMENT_KEY_VAULT_NAME': 'example-engagement-kv',
    'QUEUE_STORAGE_ACCOUNT_NAME': 'examplestorage',
    'AZURE_OPENAI_ACCOUNT_NAME': 'example-openai',
}
os.environ.update(DEPLOYMENT_CONFIG)
_folder = tempfile.TemporaryDirectory(prefix='hhc-no-cloud-')
atexit.register(_folder.cleanup)
_root = Path(_folder.name)
(_root / 'config').mkdir()
(_root / 'az').write_text('#!/bin/sh\necho "Unexpected Azure invocation blocked by test harness" >&2\nexit 97\n')
(_root / 'az').chmod(0o755)
os.environ['PATH'] = str(_root) + os.pathsep + os.environ['PATH']
os.environ['AZ_BIN'] = str(_root / 'az')
os.environ['AZURE_CONFIG_DIR'] = str(_root / 'config')

if __name__ == '__main__':
    import shlex
    for name, value in DEPLOYMENT_CONFIG.items():
        print('export ' + name + '=' + shlex.quote(value))
