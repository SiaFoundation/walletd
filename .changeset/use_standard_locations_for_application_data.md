---
default: major
---

# Use standard locations for application data

Uses standard locations for application data instead of the current directory. This brings `walletd` in line with other system services and makes it easier to manage application data.

#### Linux, FreeBSD, OpenBSD
- Configuration: `/etc/walletd/walletd.yml`
- Data directory: `/var/lib/walletd`

#### macOS
- Configuration: `~/Library/Application Support/walletd.yml`
- Data directory: `~/Library/Application Support/walletd`

#### Windows
- Configuration: `%APPDATA%\SiaFoundation\walletd.yml`
- Data directory: `%APPDATA%\SiaFoundation\walletd`

#### Docker
- Configuration: `/data/walletd.yml`
- Data directory: `/data`
