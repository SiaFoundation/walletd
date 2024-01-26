CREATE TABLE chain_indices (
	id INTEGER PRIMARY KEY,
	block_id BLOB UNIQUE NOT NULL,
	height INTEGER UNIQUE NOT NULL
);

CREATE TABLE sia_addresses (
	id INTEGER PRIMARY KEY,
	sia_address BLOB UNIQUE NOT NULL,
	siacoin_balance BLOB NOT NULL,
	immature_siacoin_balance BLOB NOT NULL,
	siafund_balance INTEGER NOT NULL
);

CREATE TABLE siacoin_elements (
	id BLOB PRIMARY KEY,
	siacoin_value BLOB NOT NULL,
	merkle_proof BLOB NOT NULL,
	leaf_index INTEGER NOT NULL,
	maturity_height INTEGER NOT NULL, /* stored as int64 for easier querying */
	address_id INTEGER NOT NULL REFERENCES sia_addresses (id)
);
CREATE INDEX siacoin_elements_address_id ON siacoin_elements (address_id);

CREATE TABLE siafund_elements (
	id BLOB PRIMARY KEY,
	claim_start BLOB NOT NULL,
	merkle_proof BLOB NOT NULL,
	leaf_index INTEGER NOT NULL,
	siafund_value INTEGER NOT NULL,
	address_id INTEGER NOT NULL REFERENCES sia_addresses (id)
);
CREATE INDEX siafund_elements_address_id ON siafund_elements (address_id);

CREATE TABLE wallets (
	id TEXT PRIMARY KEY NOT NULL,
	extra_data BLOB NOT NULL
);

CREATE TABLE wallet_addresses (
	wallet_id TEXT NOT NULL REFERENCES wallets (id),
	address_id INTEGER NOT NULL REFERENCES sia_addresses (id),
	extra_data BLOB NOT NULL,
	UNIQUE (wallet_id, address_id)
);
CREATE INDEX wallet_addresses_address_id ON wallet_addresses (address_id);

CREATE TABLE events (
	id INTEGER PRIMARY KEY,
	date_created INTEGER NOT NULL,
	index_id BLOB NOT NULL REFERENCES chain_indices (id) ON DELETE CASCADE,
	event_type TEXT NOT NULL,
	event_data TEXT NOT NULL
);

CREATE TABLE event_addresses (
	id INTEGER PRIMARY KEY,
	event_id INTEGER NOT NULL REFERENCES events (id) ON DELETE CASCADE,
	address_id INTEGER NOT NULL REFERENCES sia_addresses (id),
	block_height INTEGER NOT NULL, /* prevents extra join when querying for events */
	UNIQUE (event_id, address_id)
);
CREATE INDEX event_addresses_event_id_idx ON event_addresses (event_id);
CREATE INDEX event_addresses_address_id_idx ON event_addresses (address_id);
CREATE INDEX event_addresses_event_id_address_id_block_height ON event_addresses(event_id, address_id, block_height DESC);

CREATE TABLE syncer_peers (
	peer_address TEXT PRIMARY KEY NOT NULL,
	first_seen INTEGER NOT NULL,
	last_connect INTEGER NOT NULL,
	synced_blocks INTEGER NOT NULL,
	sync_duration INTEGER NOT NULL
);

CREATE TABLE syncer_bans (
	net_cidr TEXT PRIMARY KEY NOT NULL,
	expiration INTEGER NOT NULL,
	reason TEXT NOT NULL
);
CREATE INDEX syncer_bans_expiration_index ON syncer_bans (expiration);

CREATE TABLE global_settings (
	id INTEGER PRIMARY KEY NOT NULL DEFAULT 0 CHECK (id = 0), -- enforce a single row
	db_version INTEGER NOT NULL, -- used for migrations
	last_indexed_tip BLOB -- the last chain index that was processed
);
