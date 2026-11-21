# Solis

Solis is a lightweight, efficient, and reliable home asset tracking server designed specifically for minimal hardware like the Raspberry Pi Zero (ARMv6, 512MB RAM).

## Features

- **Hierarchical Asset Tracking:** Keep track of your home equipment, tools, and inventory with drag-and-drop structural movement.
- **Location & Tag Management:** Group assets by physical rooms or logical categories.
- **Shortlink Generation:** Automatically generates collision-resistant shortlinks (e.g. `s/item1`) for quick QR code access.
- **Photo & Receipt Uploads:** Store and securely serve important asset documentation.
- **Offline-First UI:** Self-contained static assets, no external CDNs required.
- **CGO-Free SQLite:** Cross-compiles beautifully to ARMv6 architectures without painful C/C++ toolchains.

---

## 🚀 Installation & Deployment Guide

This guide walks you through setting up Solis for development, local execution, and production deployment (including Systemd, Docker, and Caddy reverse proxies).

### 📋 Prerequisites

Before you begin, ensure you have the following installed:
* **Go 1.22** or higher (to compile from source)
* **Git** (to clone the repository)
* **Docker & Docker Compose** (optional, for containerized deployments)

---

### 🛠️ 1. Build and Run from Source

#### Step 1: Clone the Repository
```bash
git clone https://github.com/luciochen233/Solis.git
cd Solis
```

#### Step 2: Configure the Application
Copy the template configuration file:
```bash
cp config.toml.example config.toml
```
Open `config.toml` in your favorite editor to customize your database path, port, and security credentials (especially the admin password!).

#### Step 3: Compile and Build
Because Solis uses `modernc.org/sqlite` (a fully Go-native CGO-free SQLite implementation), you can compile it without any GCC toolchain required.

* **For Windows (Local Dev):**
  ```powershell
  $env:CGO_ENABLED="0"; go build -o solis.exe main.go
  ```

* **For Linux/macOS (Local Dev):**
  ```bash
  CGO_ENABLED=0 go build -o solis main.go
  ```

* **Cross-compile for Raspberry Pi Zero (ARMv6):**
  ```bash
  # In PowerShell:
  $env:GOOS="linux"; $env:GOARCH="arm"; $env:GOARM="6"; $env:CGO_ENABLED="0"
  go build -ldflags="-s -w" -o solis main.go

  # In Linux/macOS Bash:
  CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=6 go build -ldflags="-s -w" -o solis main.go
  ```

#### Step 4: Run the Server
Simply start the executable:
```bash
# Windows
.\solis.exe

# Linux/macOS
./solis
```
Navigate to `http://localhost:8889` in your web browser. Use the default credentials `admin` / `admin` to sign in.

---

### 🐳 2. Running with Docker

Solis is containerized for simple and isolated deployments.

#### Run with Docker CLI
To quickly run the pre-configured Solis container, execute:
```bash
# Build the image locally
docker build -t solis .

# Run the container with directory volume mapping
docker run -d \
  -p 8889:8889 \
  -v $(pwd)/data:/app/data \
  --name solis-server \
  solis
```

#### Run with Docker Compose
Create a `docker-compose.yml` file in the project root:
```yaml
version: '3.8'

services:
  solis:
    build: .
    container_name: solis
    ports:
      - "8889:8889"
    volumes:
      - ./data:/app/data
    restart: unless-stopped
```
Launch the stack in detached mode:
```bash
docker compose up -d
```

---

### ⚙️ 3. Production Configuration (`config.toml`)

Customize your application by editing the generated `config.toml` file:

```toml
[server]
host = ""                            # Listen address ("" = all interfaces; "127.0.0.1" behind a local reverse proxy)
port = 8889                          # Server port to bind to
base_url = "http://localhost:8889"    # Public URL (used for generating QR codes/shortlinks)
read_timeout = "5s"
write_timeout = "10s"
language = "en"                      # UI Language ("en" or "zh")
trust_proxy = false                  # true behind a reverse proxy (required there for per-IP rate limits)
proxy_hops = 1                       # trusted proxies in front: 1 = nginx/Caddy, 2 = Cloudflare -> nginx
max_concurrent = 64                  # in-flight request cap (503 beyond it)
public_pages = true                  # false = item/location pages require a login (see Security notes)

[admin]
username = "admin"
# Generate a new bcrypt hash using: ./solis --hash-password "your-password"
password_hash = "$2a$10$WXBoF7ZB7fIRl5smdGH9seqlNbXWiCS7ZbGNk0ONb9xjS8499gBpu"
session_hours = 24

[mcp]
enabled = false                         # Enable the MCP endpoint at POST /mcp
api_key = ""                            # Send as Authorization: Bearer <api_key> or X-API-Key

[database]
path = "./data/solis.db"             # Path to your SQLite DB file

[slugs]
length = 4                           # Number of characters in shortlinks

[upload]
dir = "./data/uploads"               # Upload target folder
max_size_mb = 50                     # Maximum file upload size in MB
```

> [!WARNING]
> Always change the default `password_hash` in `config.toml` before exposing your server to the internet or local networks. Solis refuses to start with the default password unless `base_url` points at localhost.

### Security notes

- **Guest pages.** With `public_pages = true`, anyone who has (or guesses) an item URL sees that item's name, status and description, and a location URL shows the location's name and description — nothing else, and removed items are hidden. Item slugs are short and derived from the item ID, so they can be enumerated; set `public_pages = false` to send visitors who are not logged in to the login page instead (they return to the scanned page after signing in).
- **Behind a reverse proxy** set `host = "127.0.0.1"`, `trust_proxy = true` and the right `proxy_hops`. With `trust_proxy = false` behind a proxy every visitor appears as the proxy's address, so the per-IP login/MCP throttles collapse into one bucket that a single client can exhaust for everyone.
- **CSRF.** State-changing requests need the per-page token *and* must come from this origin (`Sec-Fetch-Site` / `Origin` check), which also stops sibling subdomains. Non-browser clients (curl, MCP) are unaffected.
- **MCP.** Prefer the API key. Basic auth with the admin password is accepted too, which makes `/mcp` a place where that password can be guessed (failed attempts are throttled per IP and logged) — keep that in mind if the admin UI itself sits behind an extra gate such as Cloudflare Access.
- **Shared hardening code.** `internal/httpguard` is kept byte-identical with the copy in [Glimmer](https://github.com/luciochen233/Glimmer); change it in one repository and copy it to the other.
- A hardened systemd unit is provided in `deploy/solis.service`.

### MCP Access

Solis can expose a Model Context Protocol endpoint at `POST /mcp`. Enable it in `config.toml`:

```toml
[mcp]
enabled = true
api_key = "replace-with-a-long-random-token"
```

MCP clients can authenticate with `Authorization: Bearer <api_key>` or `X-API-Key: <api_key>`. Basic auth using the configured admin username and password is also accepted. The MCP endpoint exposes tools for listing, creating, updating, removing, archiving, restoring, and permanently deleting items, locations, and tags.

---

### 🖥️ 4. Running as a Systemd Service (Linux/Raspberry Pi)

To ensure Solis automatically starts up when your Raspberry Pi boots, set it up as a systemd background service.

1. **Move Binary & Config**:
   ```bash
   sudo cp solis /usr/local/bin/
   sudo mkdir -p /etc/solis
   sudo cp config.toml /etc/solis/config.toml
   ```

2. **Create a System User**:
   ```bash
   sudo useradd -r -s /bin/false solis
   sudo mkdir -p /var/lib/solis/data
   sudo chown -R solis:solis /var/lib/solis
   ```

3. **Create the Systemd Service File**:
   Create `/etc/systemd/system/solis.service`:
   ```ini
   [Unit]
   Description=Solis Home Asset Tracking Server
   After=network.target

   [Service]
   Type=simple
   User=solis
   Group=solis
   WorkingDirectory=/var/lib/solis
   ExecStart=/usr/local/bin/solis --config /etc/solis/config.toml
   Restart=on-failure
   RestartSec=5

   [Install]
   WantedBy=multi-user.target
   ```

4. **Enable & Start Service**:
   ```bash
   sudo systemctl daemon-reload
   sudo systemctl enable solis.service
   sudo systemctl start solis.service
   ```
   Check status:
   ```bash
   sudo systemctl status solis.service
   ```

---

### 🔒 5. Reverse Proxy Setup (Caddy)

If you are exposing Solis to your local network or a public domain, it is highly recommended to use Caddy as a reverse proxy to handle compression and SSL automatically.

A complete `Caddyfile` is provided in the repository root. To configure it:
1. Replace `:80` or `solis.local` in `Caddyfile` with your domain.
2. Ensure Caddy is installed on your server.
3. Start Caddy using the configuration:
   ```bash
   caddy run --config Caddyfile
   ```

---

## 📂 Project Structure

- `main.go` - Application entry point and configuration loading.
- `internal/db/` - SQLite database drivers, schema creations, and CRUD operations.
- `internal/server/` - Routing handlers, HTTP middleware, session managers, and custom static resources.
- `internal/httpguard/` - HTTP hardening shared verbatim with Glimmer (client IP, rate limits, cross-origin guard, body caps).
- `deploy/solis.service` - Hardened systemd unit.
- `internal/server/templates/` - Modern HTMX template files.
- `internal/slug/` - Clean and collision-resistant slug generators.
- `config.toml` - Core configuration specifications.
- `data/` - (Git-ignored) Local SQLite database persistence and asset upload catalog.
