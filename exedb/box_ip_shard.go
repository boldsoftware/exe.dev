package exedb

import (
	"context"
	"database/sql"
	"errors"
)

// ListIPShardsForUser returns all IP shards currently assigned to the provided user, sorted ascending.
func (q *Queries) ListIPShardsForUser(ctx context.Context, userID string) ([]int64, error) {
	rows, err := q.db.QueryContext(ctx, `
		SELECT ip_shard
		FROM box_ip_shard
		WHERE user_id = ?
		ORDER BY ip_shard ASC
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var shards []int64
	for rows.Next() {
		var shard int64
		if err := rows.Scan(&shard); err != nil {
			return nil, err
		}
		shards = append(shards, shard)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return shards, nil
}

// InsertBoxIPShard assigns the provided IP shard to the (box_id, user_id) tuple.
func (q *Queries) InsertBoxIPShard(ctx context.Context, boxID int64, userID string, ipShard int64) error {
	_, err := q.db.ExecContext(ctx, `
		INSERT INTO box_ip_shard (box_id, user_id, ip_shard)
		VALUES (?, ?, ?)
	`, boxID, userID, ipShard)
	return err
}

// GetBoxIPShard retrieves the IP shard assigned to the provided box.
func (q *Queries) GetBoxIPShard(ctx context.Context, boxID int64) (int64, error) {
	row := q.db.QueryRowContext(ctx, `
		SELECT ip_shard
		FROM box_ip_shard
		WHERE box_id = ?
	`, boxID)
	var shard int64
	if err := row.Scan(&shard); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, sql.ErrNoRows
		}
		return 0, err
	}
	return shard, nil
}
