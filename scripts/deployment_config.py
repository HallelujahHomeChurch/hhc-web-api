"""Private deployment targets; absent or mismatched configuration fails closed."""
import json
import os
from pathlib import Path
import re
import sys


PARAMETERS = {
    'containerAppEnvironmentName': 'CONTAINER_APP_ENVIRONMENT_NAME',
    'containerRegistryName': 'ACR_NAME',
    'containerRegistryLoginServer': 'ACR_LOGIN_SERVER',
    'runtimeKeyVaultName': 'RUNTIME_KEY_VAULT_NAME',
    'migrationKeyVaultName': 'MIGRATION_KEY_VAULT_NAME',
    'legacyKeyVaultName': 'LEGACY_KEY_VAULT_NAME',
    'accountKeyVaultName': 'ACCOUNT_KEY_VAULT_NAME',
    'engagementKeyVaultName': 'ENGAGEMENT_KEY_VAULT_NAME',
    'queueStorageAccountName': 'QUEUE_STORAGE_ACCOUNT_NAME',
    'azureOpenAIAccountName': 'AZURE_OPENAI_ACCOUNT_NAME',
}
TEMPLATES = {
    'main.bicep': set(PARAMETERS) - {'containerRegistryLoginServer'},
    'bulletin-extraction-queue.bicep': {'queueStorageAccountName'},
    'bulletin-worker-identity.bicep': {'containerRegistryName', 'runtimeKeyVaultName', 'queueStorageAccountName'},
    'private-service-secret-access.bicep': {'runtimeKeyVaultName'},
    'bulletin-v4.bicep': {'containerAppEnvironmentName', 'containerRegistryLoginServer'},
}
DIGEST = re.compile(r'^sha256:[a-f0-9]{64}$')


def required(name):
    value = os.environ.get(name, '')
    pattern = r'[a-zA-Z0-9][a-zA-Z0-9_.()-]{0,89}'
    if name == 'AZURE_SUBSCRIPTION_ID':
        pattern = r'[a-fA-F0-9]{8}(?:-[a-fA-F0-9]{4}){3}-[a-fA-F0-9]{12}'
    elif name == 'ACR_NAME':
        pattern = r'[a-z0-9]{5,50}'
    elif name == 'ACR_LOGIN_SERVER':
        pattern = r'[a-z0-9]{5,50}\.azurecr\.io'
    elif name.endswith('KEY_VAULT_NAME'):
        pattern = r'[a-zA-Z][a-zA-Z0-9-]{1,22}[a-zA-Z0-9]'
    elif name == 'QUEUE_STORAGE_ACCOUNT_NAME':
        pattern = r'[a-z0-9]{3,24}'
    elif name.endswith('IMAGE_REPOSITORY'):
        pattern = r'[a-z0-9]+(?:[._/-][a-z0-9]+)*'
    if not re.fullmatch(pattern, value):
        raise ValueError('Missing or invalid deployment setting: ' + name)
    return value


def image_repository(kind):
    if kind not in ('api', 'extractor'):
        raise ValueError('Unknown image role')
    return required('IMAGE_REPOSITORY' if kind == 'api' else 'EXTRACTOR_IMAGE_REPOSITORY')


def image_ref(digest, kind):
    if not DIGEST.fullmatch(digest):
        raise ValueError('Expected an immutable image digest')
    server = required('ACR_LOGIN_SERVER')
    if server != required('ACR_NAME') + '.azurecr.io':
        raise ValueError('Registry name and login server differ')
    return server + '/' + image_repository(kind) + '@' + digest


def valid_image(image, kind=None):
    digest = image.rsplit('@', 1)[-1]
    if not DIGEST.fullmatch(digest):
        return False
    if kind is not None:
        return image == image_ref(digest, kind)
    # Shared rollback also owns Gateway images within the configured namespace.
    prefix = image_ref(digest, 'api').rsplit('/', 1)[0]
    return bool(re.fullmatch(re.escape(prefix) + r'/[a-z0-9-]+@sha256:[a-f0-9]{64}', image))


def image_digest(image, kind=None):
    if not valid_image(image, kind):
        raise ValueError('Image does not match the configured immutable target')
    return image.rsplit('@', 1)[1]


def resource_scope():
    return '/subscriptions/' + required('AZURE_SUBSCRIPTION_ID') + '/resourceGroups/' + required('RESOURCE_GROUP')


def targets():
    names = set(PARAMETERS.values()) | {'RESOURCE_GROUP', 'AZURE_SUBSCRIPTION_ID', 'IMAGE_REPOSITORY', 'EXTRACTOR_IMAGE_REPOSITORY'}
    result = {name: required(name) for name in names}
    for role in ('api', 'extractor'):
        prefix = image_ref('sha256:' + '0' * 64, role).split('@')[0]
        result[role + 'ImagePattern'] = '^' + re.escape(prefix) + r'@sha256:[a-f0-9]{64}$'
    return result


def write_parameters(template, target):
    names = TEMPLATES.get(Path(template).name)
    if names is None:
        raise ValueError('Unknown deployment template')
    values = {name: {'value': required(PARAMETERS[name])} for name in sorted(names)}
    # Validate everything before creating a private, non-following output file.
    fd = os.open(target, os.O_WRONLY | os.O_CREAT | os.O_TRUNC | os.O_NOFOLLOW, 0o600)
    with os.fdopen(fd, 'w') as stream:
        os.fchmod(stream.fileno(), 0o600)
        json.dump({'parameters': values}, stream)


def main(args):
    if len(args) == 3 and args[0] == 'parameters':
        write_parameters(args[1], args[2])
    elif len(args) == 3 and args[0] == 'image-ref':
        print(image_ref(args[2], args[1]))
    elif len(args) == 3 and args[0] == 'image-digest':
        print(image_digest(args[2], None if args[1] == 'any' else args[1]))
    elif args == ['scope']:
        print(resource_scope())
    elif args == ['targets']:
        print(json.dumps(targets()))
    else:
        raise ValueError('Unknown deployment configuration operation')


if __name__ == '__main__':
    try:
        main(sys.argv[1:])
    except (ValueError, OSError) as error:
        # Do not echo configured values or complete command arguments.
        print(str(error) if isinstance(error, ValueError) else 'Cannot write deployment configuration', file=sys.stderr)
        sys.exit(1)
