# RedstoneCore Agent

Self-hosted Minecraft server management agent that connects to the RedstoneCore cloud console.

## Quick Start

### 1. Get a License Key

Visit [redstonecore.sterango.com](https://redstonecore.sterango.com) to create an account and get a license.

### 2. Deploy with Docker

```bash
# Download the docker-compose file
curl -O https://redstonecore.sterango.com/downloads/docker-compose.yml

# Create your environment file with your license key
echo "RSC_LICENSE_KEY=RSC-XXXX-XXXX-XXXX-XXXX" > .env

# Create data directories
mkdir -p data config

# Start the agent
docker compose up -d

# View logs
docker compose logs -f
```

### 3. Manage via Cloud Console

Log in to [redstonecore.sterango.com](https://redstonecore.sterango.com) to:
- Create and manage servers
- View real-time console output
- Start, stop, and restart servers
- Install modpacks from Modrinth
- Manage plugins and mods
- Monitor server performance

## Configuration

### Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `RSC_LICENSE_KEY` | Your license key (required) | - |
| `TZ` | Timezone | UTC |

### Optional: config.yaml

You can pre-configure servers by creating `config/config.yaml`:

```yaml
servers:
  - name: "Survival"
    type: "paper"
    minecraft_version: "1.21"
    port: 25565
    max_players: 20
    ram: 4096
    auto_start: true

  - name: "Creative"
    type: "paper"
    minecraft_version: "1.21"
    port: 25566
    max_players: 10
    ram: 2048
```

Or simply create servers from the web console - no config file needed!

### Server Types

| Type | Description |
|------|-------------|
| `vanilla` | Official Minecraft server |
| `paper` | PaperMC (recommended for plugins) |
| `spigot` | SpigotMC |
| `forge` | Minecraft Forge (mods) |
| `neoforge` | NeoForge (mods, 1.20.1+) |
| `fabric` | Fabric (mods) |
| `bungeecord` | BungeeCord proxy |
| `velocity` | Velocity proxy |

## Directory Structure

```
./
├── data/
│   ├── servers/
│   │   └── ServerName/
│   │       ├── server.jar (or run.sh for Forge/NeoForge)
│   │       ├── world/
│   │       ├── plugins/
│   │       ├── mods/
│   │       └── ...
│   └── cache/
│       └── modpacks/
├── config/
│   ├── config.yaml (optional)
│   └── .credentials (auto-generated)
├── docker-compose.yml
└── .env
```

## Requirements

- Docker and Docker Compose
- Linux host (recommended) or Windows with WSL2
- Ports open for Minecraft (default: 25565+)
- Minimum 2GB RAM per server

## Firewall / Port Forwarding

The agent uses `network_mode: host` so your Minecraft servers will be accessible on their configured ports directly.

For example, if you configure a server on port 25565:
- Players connect to: `your-server-ip:25565`

Make sure your firewall allows incoming connections on your Minecraft ports.

## Updating

```bash
# Pull the latest image
docker compose pull

# Restart with the new image
docker compose up -d
```

## Troubleshooting

### Agent won't connect to cloud

1. Verify your license key is correct in `.env`
2. Ensure outbound HTTPS (port 443) is allowed
3. Check logs: `docker compose logs -f`

### Server won't start

1. Check the console for errors in the web panel
2. Ensure you have enough RAM allocated
3. Verify the port isn't already in use: `netstat -tlnp | grep 25565`

### Modpack installation fails

1. Ensure the server is stopped before installing
2. Check agent logs for download errors
3. Some modpacks may require more RAM - increase the limit in docker-compose.yml

### View agent logs

```bash
docker compose logs -f
```

## Support

- Documentation: [redstonecore.sterango.com/docs](https://redstonecore.sterango.com/docs)
- Issues: [GitHub Issues](https://github.com/redstonecore/agent/issues)

## License

Proprietary - RedstoneCore
