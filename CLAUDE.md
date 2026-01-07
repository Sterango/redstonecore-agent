# RedstoneCore Agent

## Versioning Scheme

Use semantic versioning with patch numbers 0-9:
- `1.3.0` → `1.3.1` → ... → `1.3.9` → `1.4.0`
- Increment the patch number (last digit) for minor fixes/features
- When patch reaches 9, bump the minor version and reset patch to 0
- Example: Current is `1.3.0`, next would be `1.3.1`, not `1.4.0`

## Releasing a New Version

To release a new version:

1. Update the version in `Dockerfile` (line ~20):
   ```dockerfile
   -ldflags="-w -s -X github.com/sterango/redstonecore-agent/internal/version.Version=X.Y.Z -X github.com/sterango/redstonecore-agent/internal/version.BuildTime=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
   ```

2. Update `AGENT_LATEST_VERSION` in the web app `.env` (`/mnt/Drive1/palahace/websites/redstonecore.net/.env`):
   ```
   AGENT_LATEST_VERSION=X.Y.Z
   ```

3. Commit and push both repos - GitHub Action will build and push to GHCR

4. Users can update via UI "Update Now" button or `./rsc update`

## How Self-Update Works

The agent self-update process:
1. Web panel sends `update_agent` command via WebSocket
2. Agent pulls latest image: `docker pull ghcr.io/sterango/redstonecore-agent:latest`
3. Agent restarts itself: `docker compose -p redstonecore -f /docker-compose.yml up -d --force-recreate`

Requirements for self-update:
- Container must run as `user: root` (for Docker socket access)
- Docker socket mounted: `/var/run/docker.sock:/var/run/docker.sock`
- Compose file mounted: `./docker-compose.yml:/docker-compose.yml:ro`

## Server Types

Supported server types and their download sources:
- **vanilla** - Mojang official launcher manifest
- **paper** - PaperMC API
- **fabric** - Fabric installer API
- **forge** - Forge Maven repository
- **neoforge** - NeoForge Maven repository
- **velocity** - PaperMC API (proxy)
- **bungeecord** - Waterfall via PaperMC API (proxy)
- **spigot** - BuildTools (compiled, cached in `/data/.spigot-cache`)
