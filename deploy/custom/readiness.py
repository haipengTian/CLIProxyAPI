#!/usr/bin/env python3
"""Check a bundled deployment once, without logging credentials or responses."""

import argparse
import hashlib
import json
import re
import sys
import urllib.error
import urllib.request
from pathlib import Path

import yaml


def mapping(value, name):
    if not isinstance(value, dict):
        raise ValueError(f"invalid {name} structure")
    return value


def validate_config(path):
    try:
        config = mapping(yaml.safe_load(path.read_text(encoding="utf-8")), "configuration")
    except (OSError, yaml.YAMLError) as error:
        raise ValueError("cannot read deployment configuration") from error
    server = mapping(config.get("server"), "server")
    management = mapping(config.get("management"), "management")
    access = mapping(config.get("access"), "access")
    if config.get("config-version") != 8 or server.get("port") != 8317:
        raise ValueError("expected v8 configuration on container port 8317")
    if server.get("host") != "0.0.0.0":
        raise ValueError("container listener must use 0.0.0.0")
    if management.get("allow-remote") is not True:
        raise ValueError("management access through Docker must be enabled")
    if management.get("disable-control-panel", False) is not False:
        raise ValueError("bundled control panel must be enabled")
    if management.get("disable-auto-update-panel") is not True:
        raise ValueError("control panel automatic updates must be disabled")
    secret = management.get("secret-key")
    if not isinstance(secret, str) or not (
        re.fullmatch(r"[a-f0-9]{64}", secret)
        or re.fullmatch(r"\$2[aby]\$\d{2}\$[./A-Za-z0-9]{53}", secret)
    ):
        raise ValueError("management key must be generated or bcrypt hashed")
    keys = access.get("api-keys")
    if not isinstance(keys, list) or not keys:
        raise ValueError("at least one client API key is required")
    for key in keys:
        if not isinstance(key, str) or not re.fullmatch(r"[!-~]{24,}", key) or key.startswith("your-api-key"):
            raise ValueError("client keys must be strong and cannot use template values")
        if key == secret:
            raise ValueError("management and client API keys must be different")
    return keys


def validate_static(directory):
    try:
        manifest = json.loads((directory / "release.json").read_text(encoding="utf-8"))
        expected = manifest["ui_sha256"]
        actual = hashlib.sha256((directory / "management.html").read_bytes()).hexdigest()
    except (OSError, ValueError, KeyError, TypeError) as error:
        raise ValueError("bundled frontend or release manifest is missing or invalid") from error
    if expected != actual:
        raise ValueError("bundled frontend checksum does not match the release manifest")
    return expected


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def http_response(opener, base, path, key=None):
    headers = {"Authorization": "Bearer " + key} if key else {}
    try:
        request = urllib.request.Request(base + path, headers=headers)
        with opener.open(request, timeout=10) as response:
            return response.status, response.read(50 * 1024 * 1024)
    except urllib.error.HTTPError as error:
        error.close()
        return error.code, b""
    except (OSError, urllib.error.URLError, ValueError, UnicodeError) as error:
        raise ValueError("local HTTP request failed") from error


def verify_http(base, keys, panel_digest):
    # Checks are local; ignore any external HTTP proxy environment settings.
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
    checks = [
        ("/healthz", None, 200),
        ("/management.html", None, 200),
        ("/v1/models", None, 401),
        ("/v1/models", keys[0], 200),
    ]
    for path, key, expected in checks:
        status, body = http_response(opener, base, path, key)
        if status != expected:
            raise ValueError(f"deployment HTTP gate failed for {path}")
        if path == "/management.html" and hashlib.sha256(body).hexdigest() != panel_digest:
            raise ValueError("served frontend checksum does not match the release manifest")


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--config", type=Path, default=Path("/CLIProxyAPI/config/config.yaml"))
    parser.add_argument("--static", type=Path, default=Path("/CLIProxyAPI/static"))
    parser.add_argument("--base", default="http://127.0.0.1:8317")
    args = parser.parse_args()
    try:
        keys = validate_config(args.config)
        panel_digest = validate_static(args.static)
        verify_http(args.base, keys, panel_digest)
    except ValueError as error:
        print(f"Deployment not ready: {error}", file=sys.stderr)
        return 1
    print("Bundled frontend, persistent configuration and API authentication verified.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
