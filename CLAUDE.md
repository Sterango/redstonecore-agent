# RedstoneCore Agent

## Releasing a New Version

To release a new version:

1. Update the version in `Dockerfile`:
   ```dockerfile
   -ldflags="-w -s -X main.version=X.Y.Z -X main.buildTime=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
   ```

2. Update `AGENT_LATEST_VERSION` in the web app `.env`:
   ```
   AGENT_LATEST_VERSION=X.Y.Z
   ```

3. Commit and push - GitHub Action will build and push to GHCR

4. Users can update via UI "Update Now" button or `./rsc update`
