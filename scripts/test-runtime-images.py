#!/usr/bin/env python3
"""Check workload identity and keep worker dependencies out of the API image."""
import json
import subprocess
import sys
import tarfile


def check_config(config, role, entrypoint):
    assert config['Labels'].get('org.hhc.runtime') == role, 'wrong workload image'
    assert config['Entrypoint'] == [entrypoint], 'wrong workload entrypoint'
    assert config['User'] in ('nonroot:nonroot', '65532:65532'), 'runtime must be nonroot'


def verify(api, worker):
    for image, role, entrypoint in [(api, 'api', '/hhc-web-api'), (worker, 'watermark-worker', '/hhc-web-bulletin-worker')]:
        info = json.loads(subprocess.check_output(['docker', 'image', 'inspect', image]))[0]
        assert info['Os'] == 'linux' and info['Architecture'] == 'amd64', 'wrong release platform'
        check_config(info['Config'], role, entrypoint)
        startup = subprocess.run(['docker', 'run', '--rm', '--platform', 'linux/amd64', '--network', 'none',
                                  '--memory', '128m', '--cpus', '1', image], capture_output=True, text=True, timeout=20)
        assert startup.returncode == 1 and 'DATABASE_URL is required' in startup.stdout + startup.stderr, 'runtime executable did not reject missing configuration'
        print(f'{role}: {info["Size"]} bytes; executable configuration guard passed')
    container = subprocess.check_output(['docker', 'create', '--platform', 'linux/amd64', api], text=True).strip()
    try:
        process = subprocess.Popen(['docker', 'export', container], stdout=subprocess.PIPE)
        try:
            with tarfile.open(fileobj=process.stdout, mode='r|') as archive:
                names = {item.name.removeprefix('./') for item in archive}
            assert process.wait() == 0, 'API filesystem export failed'
        finally:
            process.stdout.close()
            if process.poll() is None:
                process.kill()
                process.wait()
        assert {'hhc-web-api', 'hhc-web-migrate', 'hhc-web-content-import', 'hhc-web-home-v2-migrate'} <= names
        assert not any(name.startswith(('opt/watermark', 'usr/local/lib/python', 'usr/local/bin/python')) or name in ('usr/bin/pdftoppm', 'hhc-web-bulletin-worker') for name in names), 'worker dependency in API image'
    finally:
        subprocess.run(['docker', 'rm', container], check=True, stdout=subprocess.DEVNULL)


if __name__ == '__main__':
    if len(sys.argv) != 3:
        raise SystemExit('usage: test-runtime-images.py API_IMAGE WORKER_IMAGE')
    verify(*sys.argv[1:])
