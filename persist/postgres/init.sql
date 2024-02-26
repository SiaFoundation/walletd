CREATE EXTENSION IF NOT EXISTS btree_gist;

CREATE TABLE chain_indices (
	id BIGSERIAL PRIMARY KEY,
	block_id BYTEA UNIQUE NOT NULL,
	height INTEGER UNIQUE NOT NULL
);

CREATE TABLE sia_addresses (
	id BIGSERIAL PRIMARY KEY,
	sia_address BYTEA UNIQUE NOT NULL,
	siacoin_balance NUMERIC NOT NULL,
	immature_siacoin_balance NUMERIC NOT NULL,
	siafund_balance INTEGER NOT NULL
);

CREATE TABLE siacoin_elements (
	id BYTEA PRIMARY KEY,
	siacoin_value NUMERIC NOT NULL,
	merkle_proof TEXT[] NOT NULL,
	leaf_index INTEGER NOT NULL,
	maturity_height INTEGER NOT NULL, /* stored as int64 for easier querying */
	address_id INTEGER NOT NULL REFERENCES sia_addresses (id)
);
CREATE INDEX siacoin_elements_address_id ON siacoin_elements (address_id);
CREATE INDEX siacoin_elements_maturity_height ON siacoin_elements (maturity_height);

CREATE TABLE siafund_elements (
	id BYTEA PRIMARY KEY,
	claim_start NUMERIC NOT NULL,
	merkle_proof TEXT[] NOT NULL,
	leaf_index INTEGER NOT NULL,
	siafund_value INTEGER NOT NULL,
	address_id INTEGER NOT NULL REFERENCES sia_addresses (id)
);
CREATE INDEX siafund_elements_address_id ON siafund_elements (address_id);

CREATE TABLE wallets (
	id BIGSERIAL PRIMARY KEY,
	friendly_name TEXT NOT NULL,
	description TEXT NOT NULL,
	date_created TIMESTAMP WITH TIME ZONE NOT NULL,
	last_updated TIMESTAMP WITH TIME ZONE NOT NULL,
	extra_data JSONB
);

CREATE TABLE wallet_addresses (
	wallet_id INTEGER NOT NULL REFERENCES wallets (id),
	address_id INTEGER NOT NULL REFERENCES sia_addresses (id),
	description TEXT NOT NULL,
	spend_policy BYTEA,
	extra_data JSONB,
	UNIQUE (wallet_id, address_id)
);
CREATE INDEX wallet_addresses_wallet_id ON wallet_addresses (wallet_id);
CREATE INDEX wallet_addresses_address_id ON wallet_addresses (address_id);

CREATE TABLE events (
	id BIGSERIAL PRIMARY KEY,
	index_id INTEGER NOT NULL REFERENCES chain_indices (id) ON DELETE CASCADE,
	maturity_height INTEGER NOT NULL,
	date_created TIMESTAMP WITH TIME ZONE NOT NULL,
	event_id BYTEA NOT NULL,
	event_type TEXT NOT NULL,
	event_data JSONB NOT NULL
);

CREATE TABLE event_addresses (
	event_id INTEGER NOT NULL REFERENCES events (id) ON DELETE CASCADE,
	address_id INTEGER NOT NULL REFERENCES sia_addresses (id),
	PRIMARY KEY (event_id, address_id)
);
CREATE INDEX event_addresses_event_id_idx ON event_addresses (event_id);
CREATE INDEX event_addresses_address_id_idx ON event_addresses (address_id);

CREATE TABLE syncer_peers (
	peer_address TEXT PRIMARY KEY NOT NULL,
	first_seen TIMESTAMP WITH TIME ZONE NOT NULL,
	last_connect TIMESTAMP WITH TIME ZONE NOT NULL,
	synced_blocks INTEGER NOT NULL,
	sync_duration INTEGER NOT NULL
);

CREATE TABLE syncer_bans (
	net_cidr INET PRIMARY KEY NOT NULL,
	expiration TIMESTAMP WITH TIME ZONE NOT NULL,
	reason TEXT NOT NULL
);
CREATE INDEX syncer_bans_net_cidr ON syncer_bans USING gist (net_cidr inet_ops);
CREATE INDEX syncer_bans_expiration_index ON syncer_bans (expiration);

CREATE TABLE global_settings (
	id INTEGER PRIMARY KEY NOT NULL DEFAULT 0 CHECK (id = 0), -- enforce a single row
	db_version INTEGER NOT NULL, -- used for migrations
	last_indexed_tip BYTEA -- the last chain index that was processed
);
