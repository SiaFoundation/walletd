## 0.9.0 (2025-01-21)

### Breaking Changes

#### Support V2 Hardfork

The V2 hardfork is scheduled to modernize Sia's consensus protocol, which has been untouched since Sia's mainnet launch back in 2014, and improve accessibility of the storage network. To ensure a smooth transition from V1, it will be executed in two phases. Additional documentation on upgrading will be released in the near future.

##### V2 Highlights
- Drastically reduces blockchain size on disk
- Improves UTXO spend policies - including HTLC support for Atomic Swaps
- More efficient contract renewals - reducing lock up requirements for hosts and renters
- Improved transfer speeds - enables hot storage

##### Phase 1 - Allow Height
- **Activation Height:** `513400` (March 10th, 2025)
- **New Features:** V2 transactions, contracts, and RHP4
- **V1 Support:** Both V1 and V2 will be supported during this phase
- **Purpose:** This period gives time for integrators to transition from V1 to V2
- **Requirements:** Users will need to update to support the hardfork before this block height

##### Phase 2 - Require Height
- **Activation Height:** `526000` (June 6th, 2025)
- **New Features:** The consensus database can be trimmed to only store the Merkle proofs
- **V1 Support:** V1 will be disabled, including RHP2 and RHP3. Only V2 transactions will be accepted
- **Requirements:** Developers will need to update their apps to support V2 transactions and RHP4 before this block height

#### Use standard locations for application data

Uses standard locations for application data instead of the current directory. This brings `walletd` in line with other system services and makes it easier to manage application data.

##### Linux, FreeBSD, OpenBSD
- Configuration: `/etc/walletd/walletd.yml`
- Data directory: `/var/lib/walletd`

##### macOS
- Configuration: `~/Library/Application Support/walletd.yml`
- Data directory: `~/Library/Application Support/walletd`

##### Windows
- Configuration: `%APPDATA%\SiaFoundation\walletd.yml`
- Data directory: `%APPDATA%\SiaFoundation\walletd`

##### Docker
- Configuration: `/data/walletd.yml`
- Data directory: `/data`

### Features

- Log startup errors to stderr

#### Add transaction construction API

Adds two new endpoints to construct transactions. This combines and simplifies the existing fund flow for simple send transactions.

See API docs for request and response bodies

### Fixes

- Added a test for migrations to ensure consistency between database schemas

## 0.8.0

This is the first stable release for the walletd app -- the new reference wallet for users and exchanges

### Breaking changes

- SiaFund support
- Ledger hardware wallet support
- Multi-wallet support
- Full index mode for exchanges and wallet integrators
- Redesigned events list
- Redesigned transaction flow
