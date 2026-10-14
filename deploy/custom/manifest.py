#!/usr/bin/env python3
"""Record source pairing and the actual frontend checksum."""
import hashlib
import json
import subprocess
import sys
from pathlib import Path

ui_dir = Path(sys.argv[1])
ui = json.loads(Path("deploy/custom/ui.json").read_text(encoding="utf-8"))
manifest = {
    "backend_commit": subprocess.check_output(["git", "rev-parse", "HEAD"], text=True).strip(),
    "ui_repository": ui["repository"],
    "ui_commit": ui["commit"],
    "ui_version": ui["version"],
    "ui_sha256": hashlib.sha256((ui_dir / "management.html").read_bytes()).hexdigest(),
}
(ui_dir / "release.json").write_text(json.dumps(manifest, indent=2) + "\n", encoding="utf-8")
