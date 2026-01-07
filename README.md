# RedstoneCore Agent

Self-hosted Minecraft server management agent that connects to the RedstoneCore cloud console.

## Quick Start

### 1. Get a License Key

Visit [redstonecore.net](https://redstonecore.net) to create an account and get a license.

### 2. Deploy with Docker

```bash
# Download the docker-compose file
curl -O https://redstonecore.net/downloads/docker-compose.yml

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

Log in to [redstonecore.net](https://redstonecore.net) to:
- Create and manage servers
- View real-time console output
- Start, stop, and restart servers
- Install modpacks from CurseForge
- Manage plugins and mods
- Monitor server performance
- Access files via web file manager or SFTP

## Configuration

### Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `RSC_LICENSE_KEY` | Your license key (required) | - |
| `RSC_CLOUD_URL` | Cloud console URL | `https://redstonecore.net` |
| `RSC_SFTP_RELAY_URL` | SFTP relay WebSocket URL | `wss://redstonecore.net:8022/sftp` |
| `TZ` | Timezone | UTC |

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

## SFTP Access

Access your server files via SFTP without opening any ports on your machine. The agent connects outbound to the RedstoneCore relay server.

### Connect with any SFTP client (FileZilla, WinSCP, etc.)

| Setting | Value |
|---------|-------|
| Host | `redstonecore.net` |
| Port | `2224` |
| Username | Your server's UUID (shown in web console) |
| Password | Your account password or instance API token |

Example with command line:
```bash
sftp -P 2224 5b2f65b5-aace-438d-9347-d9acf555fe48@redstonecore.net
```

### How it works

1. The agent maintains a WebSocket connection to the SFTP relay
2. You connect to the relay with your SFTP client
3. The relay authenticates you and forwards commands to your agent
4. No ports need to be opened on your machine - all connections are outbound

## Firewall / Port Forwarding

The agent uses `network_mode: host` so your Minecraft servers will be accessible on their configured ports directly.

For example, if you configure a server on port 25565:
- Players connect to: `your-server-ip:25565`

Make sure your firewall allows incoming connections on your Minecraft ports.

## Updating

### Via Web Dashboard (Recommended)

When a new version is available, you'll see an **"Update Now"** button on your instance page in the RedstoneCore dashboard. Click it to automatically pull and restart with the latest version.

### Via Command Line

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

- Documentation: [redstonecore.net/docs](https://redstonecore.net/docs)
- Issues: [GitHub Issues](https://github.com/redstonecore/agent/issues)

## License

Proprietary - RedstoneCore
