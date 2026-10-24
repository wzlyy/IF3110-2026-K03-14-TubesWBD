package admin

import (
	"context"
	"database/sql"
)

// withTransaction keeps permission changes and their session effects atomic.
func withTransaction(ctx context.Context, db *sql.DB, change func(*sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := change(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func requireAffected(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// Locking the parent group also serializes changes when no grant or member exists yet.
func lockGroup(ctx context.Context, tx *sql.Tx, groupID string) error {
	var id string
	return tx.QueryRowContext(ctx, "SELECT id FROM groups WHERE id = $1 FOR UPDATE", groupID).Scan(&id)
}

func groupMemberIDs(ctx context.Context, tx *sql.Tx, groupID string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, "SELECT user_id FROM user_groups WHERE group_id = $1 ORDER BY user_id", groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return ids, nil
}
