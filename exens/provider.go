package exens

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"

	"exe.dev/exedb"
	"exe.dev/publicips"
	"exe.dev/sqlite"
)

// PopulateIPShards populates the ip_shards table from EC2 metadata.
// This should be called at boot after discovering public IPs.
func PopulateIPShards(ctx context.Context, db *sqlite.DB, log *slog.Logger, publicIPs map[netip.Addr]publicips.PublicIP) error {
	// Check if we already have shard records
	var count int64
	err := db.Rx(ctx, func(ctx context.Context, rx *sqlite.Rx) error {
		queries := exedb.New(rx.Conn())
		var err error
		count, err = queries.CountIPShards(ctx)
		return err
	})
	if err != nil {
		return fmt.Errorf("exens: count ip_shards: %w", err)
	}

	if count > 0 {
		log.DebugContext(ctx, "ip_shards already populated", "count", count)
		return nil
	}

	log.InfoContext(ctx, "populating ip_shards from EC2 metadata", "ips", len(publicIPs))

	var populated int
	for _, info := range publicIPs {
		if !publicips.ShardIsValid(info.Shard) {
			// Skip the base domain (shard 0); only numbered shards go in ip_shards.
			continue
		}
		err := db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
			queries := exedb.New(tx.Conn())
			return queries.UpsertIPShard(ctx, exedb.UpsertIPShardParams{
				Shard:    int64(info.Shard),
				PublicIp: info.IP.String(),
			})
		})
		if err != nil {
			return fmt.Errorf("exens: upsert ip_shard %d: %w", info.Shard, err)
		}
		log.DebugContext(ctx, "added ip_shard", "shard", info.Shard, "ip", info.IP)
		populated++
	}

	log.InfoContext(ctx, "populated ip_shards", "count", populated)
	return nil
}

// UpsertIPShard updates a single shard's public IP.
// Used when IP changes are detected.
func UpsertIPShard(ctx context.Context, db *sqlite.DB, shard int, publicIP string) error {
	return db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		queries := exedb.New(tx.Conn())
		return queries.UpsertIPShard(ctx, exedb.UpsertIPShardParams{
			Shard:    int64(shard),
			PublicIp: publicIP,
		})
	})
}
