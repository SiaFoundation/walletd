package sqlite

import (
	"database/sql"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"go.sia.tech/coreutils/syncer"
	"go.uber.org/zap"
)

func scanPeerInfo(s scanner) (pi syncer.PeerInfo, err error) {
	err = s.Scan(&pi.Address, decode(&pi.FirstSeen), decode(&pi.LastConnect), &pi.SyncedBlocks, &pi.SyncDuration)
	return
}

func getPeerInfo(tx *txn, peer string) (syncer.PeerInfo, error) {
	const query = `SELECT peer_address, first_seen, last_connect, synced_blocks, sync_duration FROM syncer_peers WHERE peer_address=$1`
	return scanPeerInfo(tx.QueryRow(query, peer))
}

func (s *Store) updatePeerInfo(tx *txn, peer string, info syncer.PeerInfo) error {
	const query = `UPDATE syncer_peers SET first_seen=$1, last_connect=$2, synced_blocks=$3, sync_duration=$4 WHERE peer_address=$5 RETURNING peer_address`
	err := tx.QueryRow(query, encode(info.FirstSeen), encode(info.LastConnect), info.SyncedBlocks, info.SyncDuration, peer).Scan(&peer)
	return err
}

// AddPeer adds the given peer to the store.
func (s *Store) AddPeer(peer string) error {
	return s.transaction(func(tx *txn) error {
		const query = `INSERT INTO syncer_peers (peer_address, first_seen, last_connect, synced_blocks, sync_duration) VALUES ($1, $2, 0, 0, 0) ON CONFLICT (peer_address) DO NOTHING`
		_, err := tx.Exec(query, peer, encode(time.Now()))
		return err
	})
}

// Peers returns the addresses of all known peers.
func (s *Store) Peers() (peers []syncer.PeerInfo, _ error) {
	err := s.transaction(func(tx *txn) error {
		const query = `SELECT peer_address, first_seen, last_connect, synced_blocks, sync_duration FROM syncer_peers`
		rows, err := tx.Query(query)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			peer, err := scanPeerInfo(rows)
			if err != nil {
				return fmt.Errorf("failed to scan peer info: %w", err)
			}
			peers = append(peers, peer)
		}
		return rows.Err()
	})
	return peers, err
}

// UpdatePeerInfo updates the info for the given peer.
func (s *Store) UpdatePeerInfo(peer string, fn func(*syncer.PeerInfo)) error {
	return s.transaction(func(tx *txn) error {
		info, err := getPeerInfo(tx, peer)
		if err != nil {
			return fmt.Errorf("failed to get peer info: %w", err)
		}
		fn(&info)
		return s.updatePeerInfo(tx, peer, info)
	})
}

// PeerInfo returns the info for the given peer.
func (s *Store) PeerInfo(peer string) (syncer.PeerInfo, error) {
	var info syncer.PeerInfo
	var err error
	err = s.transaction(func(tx *txn) error {
		info, err = getPeerInfo(tx, peer)
		return err
	})
	if errors.Is(err, sql.ErrNoRows) {
		return syncer.PeerInfo{}, syncer.ErrPeerNotFound
	} else if err != nil {
		return syncer.PeerInfo{}, err
	}
	return info, nil
}

// normalizePeer normalizes a peer address to a CIDR subnet.
func normalizePeer(peer string) (string, error) {
	host, _, err := net.SplitHostPort(peer)
	if err != nil {
		host = peer
	}
	if strings.IndexByte(host, '/') != -1 {
		_, subnet, err := net.ParseCIDR(host)
		if err != nil {
			return "", fmt.Errorf("failed to parse CIDR: %w", err)
		}
		return subnet.String(), nil
	}

	ip := net.ParseIP(host)
	if ip == nil {
		return "", errors.New("invalid IP address")
	}

	var maskLen int
	if ip.To4() != nil {
		maskLen = 32
	} else {
		maskLen = 128
	}

	_, normalized, err := net.ParseCIDR(fmt.Sprintf("%s/%d", ip.String(), maskLen))
	if err != nil {
		panic("failed to parse CIDR")
	}
	return normalized.String(), nil
}

// Ban temporarily bans one or more IPs. The addr should either be a single
// IP with port (e.g. 1.2.3.4:5678) or a CIDR subnet (e.g. 1.2.3.4/16).
func (s *Store) Ban(peer string, duration time.Duration, reason string) error {
	address, err := normalizePeer(peer)
	if err != nil {
		return err
	}
	return s.transaction(func(tx *txn) error {
		const query = `INSERT INTO syncer_bans (net_cidr, expiration, reason) VALUES ($1, $2, $3) ON CONFLICT (net_cidr) DO UPDATE SET expiration=EXCLUDED.expiration, reason=EXCLUDED.reason`
		_, err := tx.Exec(query, address, encode(time.Now().Add(duration)), reason)
		return err
	})
}

// Banned returns true if the peer is banned.
func (s *Store) Banned(peer string) (banned bool, _ error) {
	// normalize the peer into a CIDR subnet
	peer, err := normalizePeer(peer)
	if err != nil {
		return false, fmt.Errorf("failed to normalize peer: %w", err)
	}

	_, subnet, err := net.ParseCIDR(peer)
	if err != nil {
		return false, fmt.Errorf("failed to parse CIDR: %w", err)
	}

	// check all subnets from the given subnet to the max subnet length
	var maxMaskLen int
	if subnet.IP.To4() != nil {
		maxMaskLen = 32
	} else {
		maxMaskLen = 128
	}

	checkSubnets := make([]string, 0, maxMaskLen)
	for i := maxMaskLen; i > 0; i-- {
		_, subnet, err := net.ParseCIDR(subnet.IP.String() + "/" + strconv.Itoa(i))
		if err != nil {
			panic("failed to parse CIDR")
		}
		checkSubnets = append(checkSubnets, subnet.String())
	}

	err = s.transaction(func(tx *txn) error {
		query := `SELECT net_cidr, expiration FROM syncer_bans WHERE net_cidr IN (` + queryPlaceHolders(len(checkSubnets)) + `) ORDER BY expiration DESC LIMIT 1`

		var subnet string
		var expiration time.Time
		err := tx.QueryRow(query, queryArgs(checkSubnets)...).Scan(&subnet, decode(&expiration))
		banned = time.Now().Before(expiration) // will return false for any sql errors, including ErrNoRows
		if err == nil && banned {
			s.log.Debug("found ban", zap.String("subnet", subnet), zap.Time("expiration", expiration))
		}
		return err
	})
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("failed to check ban status: %w", err)
	}
	return banned, nil
}
