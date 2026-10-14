# Personal bundled deployment

This build pairs this backend with the full frontend commit in `ui.json`. It hosts the single-file panel at `/management.html`, keeps both licenses, records source commits and the HTML SHA-256 in `static/release.json`, and disables panel auto-updates through the deployment configuration.

The runtime uses Debian, UID/GID 10001, four writable data directories and file storage. `MANAGEMENT_STATIC_PATH` identifies the immutable bundled panel; `WRITABLE_PATH` makes both application and request/error logs resolve to `/CLIProxyAPI/logs`. The writable configuration directory allows management edits and the initial bcrypt key rewrite. Plugins are disabled by default. No database is required.

## Verification

Use Go 1.26.4, Python 3 with PyYAML, Node 24 and Bun 1.3.14. The paired frontend must pass `bun run verify`; the backend must pass `go test ./...` and build. Deployment unit tests run with:

```bash
python3 -m unittest discover -s deploy/custom/tests -v
```

Prepare `.custom-build/ui/management.html` from the pinned frontend production build and copy its LICENSE, then:

```bash
python3 deploy/custom/manifest.py .custom-build/ui
docker build --build-context custom_ui=.custom-build/ui -f deploy/custom/Dockerfile -t cliproxyapi-custom:verify .
python3 deploy/custom/tests/smoke.py cliproxyapi-custom:verify
```

The smoke uses the real image CMD with network disabled and synthetic keys. It verifies the served panel checksum, non-root execution, repeated readiness without management lockout, management writes and configuration/key persistence after recreation. These checks cannot validate upstream credentials or production streaming.

Docker health probes only `/healthz`. The one-shot readiness gate validates configuration, bundle integrity and client API authentication. It deliberately does not issue anonymous management requests: the backend counts failed management authentication and bans an IP after repeated attempts. Redirects are refused to prevent forwarding credentials, and error output never includes keys or response bodies.

## Release

`custom-deployment.yml` verifies code and the offline image before publishing. It uploads the exact tested Docker image, then loads/tags/pushes those same bytes to GHCR; the publish job does not rebuild. Only `vX.Y.Z-custom.N` tags reachable from remote main publish. Inherited official publishing jobs are restricted to the official repository.

Before releasing, push the frontend pinned commit so CI can retrieve it, review/merge the backend and server-ops changes, confirm all checks succeeded, then create the personal release tag. The workflow uses `GITHUB_TOKEN` with packages permission. The paired release records source commits, UI checksum, image digest and tested amd64 architecture. Confirm package visibility and anonymous pull before server installation. Server scripts bind only localhost and pin the release digest.

Current local verification and test portability fixes are documented in the paired server-ops `cliproxyapi/verification.md`. There is no published custom image yet.
