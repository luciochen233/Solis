# Solis

Solis is a lightweight, efficient, and reliable home asset tracking server designed specifically for minimal hardware like the Raspberry Pi Zero (ARMv6, 512MB RAM).

## Features

- **Hierarchical Asset Tracking:** Keep track of your home equipment, tools, and inventory.
- **Location & Tag Management:** Group assets by physical rooms or logical categories.
- **Shortlink Generation:** Automatically generates collision-resistant shortlinks (e.g. `s/item1`) for quick QR code access.
- **Photo & Receipt Uploads:** Store and securely serve important asset documentation.
- **Offline-First UI:** Self-contained static assets, no external CDNs required.
- **CGO-Free SQLite:** Cross-compiles beautifully to ARMv6 architectures without painful C/C++ toolchains.

## Building

Because Solis uses `modernc.org/sqlite`, it can be cross-compiled cleanly with CGO disabled:

```bash
# Build for Windows (or local testing)
$env:CGO_ENABLED="0"; go build -o solis.exe main.go

# Cross-compile for Raspberry Pi Zero (ARMv6)
$env:GOOS="linux"; $env:GOARCH="arm"; $env:GOARM="6"; $env:CGO_ENABLED="0"; go build -o solis main.go
```

## Running

1. Copy `config.toml.example` to `config.toml`.
2. Edit `config.toml` to customize the server port, admin credentials, and data directory.
3. Start the server:
   ```bash
   ./solis.exe
   ```
4. Access the web interface at `http://localhost:8889` (or your configured port). Default login is `admin` / `admin`.

## Project Structure

- `main.go` - Application entry point
- `internal/db/` - SQLite schema, queries, and migrations
- `internal/server/` - Web server, HTMX templates, and routing
- `internal/slug/` - URL shortener hashing algorithm
- `config.toml` - Core application configuration
- `data/` - (Ignored by git) Default location for the SQLite database and file uploads
