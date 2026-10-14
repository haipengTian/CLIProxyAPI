#!/usr/bin/env python3
"""Run the actual bundled image offline, with synthetic keys and a disposable volume."""

import argparse
import subprocess
import time
import uuid


def docker(*args):
    result = subprocess.run(["docker", *args], capture_output=True, text=True)
    if result.returncode:
        raise RuntimeError("Docker operation failed: " + args[0] + "\n" + result.stderr[-1500:])
    return result.stdout.strip()


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("image")
    args = parser.parse_args()
    suffix = uuid.uuid4().hex[:12]
    name = "cpa-smoke-" + suffix
    volume = name + "-config"
    seed = """
from pathlib import Path
import os, yaml
config = {
 'config-version': 8,
 'server': {'host':'0.0.0.0','port':8317},
 'management': {'allow-remote':True,'secret-key':'a'*64,'disable-control-panel':False,'disable-auto-update-panel':True},
 'access': {'api-keys':['b'*64]},
 'oauth': {'auth-dir':'/CLIProxyAPI/auths'},
 'observability': {'logs': {'logging-to-file': True, 'logs-max-total-size-mb':10}},
 'plugins': {'enabled':False,'dir':'/CLIProxyAPI/plugins'}
}
directory=Path('/CLIProxyAPI/config')
os.chown(directory,10001,10001)
directory.chmod(0o700)
path=directory/'config.yaml'
path.write_text(yaml.safe_dump(config))
path.chmod(0o600)
os.chown(path,10001,10001)
"""
    persisted = """
import json, urllib.request, urllib.error
base='http://127.0.0.1:8317/v8/management/config'
body=json.dumps({'routing':{'strategy':'fill-first'}}).encode()
request=urllib.request.Request(base, data=body, method='PATCH', headers={'Authorization':'Bearer '+'a'*64,'Content-Type':'application/json'})
with urllib.request.urlopen(request,timeout=10) as response: assert response.status == 200
"""
    readback = """
import json, urllib.request, yaml
from pathlib import Path
request=urllib.request.Request('http://127.0.0.1:8317/v8/management/config',headers={'Authorization':'Bearer '+'a'*64})
with urllib.request.urlopen(request,timeout=10) as response:
 assert json.load(response)['routing']['strategy']=='fill-first'
config=yaml.safe_load(Path('/CLIProxyAPI/config/config.yaml').read_text())
assert config['routing']['strategy']=='fill-first'
assert config['management']['secret-key'].startswith('$2')
assert Path('/CLIProxyAPI/config/config.yaml').stat().st_uid == 10001
"""
    error_logs = """
import json, time, urllib.request, urllib.error
from pathlib import Path
request=urllib.request.Request('http://127.0.0.1:8317/v1/chat/completions',
 data=json.dumps({'model':'deployment-smoke-missing-model','messages':[{'role':'user','content':'test'}]}).encode(),
 headers={'Authorization':'Bearer '+'b'*64,'Content-Type':'application/json'})
try:
 urllib.request.urlopen(request,timeout=10).close()
 raise AssertionError('missing model should fail')
except urllib.error.HTTPError as error:
 assert error.code >= 400 and error.code not in (401,403)
 error.close()
for _ in range(50):
 files=list(Path('/CLIProxyAPI/logs').glob('error-*.log'))
 if files: break
 time.sleep(0.1)
assert files, 'request error was not written to the mounted logs directory'
assert not Path('/CLIProxyAPI/config/logs').exists(), 'request logs leaked into configuration'
assert all(path.stat().st_uid == 10001 for path in files)
request=urllib.request.Request('http://127.0.0.1:8317/v8/management/observability/logs/errors',
 headers={'Authorization':'Bearer '+'a'*64})
with urllib.request.urlopen(request,timeout=10) as response:
 listed={item['name'] for item in json.load(response)['files']}
assert {path.name for path in files} <= listed, 'management cannot read request error logs'
"""

    def start():
        docker("run", "-d", "--name", name, "--network", "none", "--cap-drop", "ALL",
               "--security-opt", "no-new-privileges", "-v", volume + ":/CLIProxyAPI/config",
               args.image)
        for _ in range(45):
            result = subprocess.run(
                ["docker", "exec", name, "curl", "--fail", "--silent", "--noproxy", "*", "http://127.0.0.1:8317/healthz"],
                capture_output=True,
            )
            if result.returncode == 0:
                return
            time.sleep(1)
        raise RuntimeError("offline container did not become healthy")

    try:
        docker("volume", "create", volume)
        docker("run", "--rm", "--network", "none", "--user", "0", "--entrypoint", "python3",
               "-v", volume + ":/CLIProxyAPI/config", args.image, "-c", seed)
        start()
        assert docker("exec", name, "id", "-u") == "10001"
        for _ in range(6):
            docker("exec", name, "python3", "deploy/readiness.py")
        print("PASS: non-root bundled image serves frontend and authenticated API without network access")
        docker("exec", name, "python3", "-c", error_logs)
        print("PASS: request error logs use the mounted directory and are readable through management")
        docker("exec", name, "python3", "-c", persisted)
        docker("rm", "-f", name)
        start()
        docker("exec", name, "python3", "-c", readback)
        print("PASS: management writes and bcrypt key survive container recreation")
    finally:
        subprocess.run(["docker", "rm", "-f", name], capture_output=True)
        subprocess.run(["docker", "volume", "rm", volume], capture_output=True)


if __name__ == "__main__":
    main()
