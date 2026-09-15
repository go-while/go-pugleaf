package database

// query_TryReserveWebPost increments the post counter only when the back-off since the last post has passed.
const query_TryReserveWebPost = `UPDATE users SET post_count = post_count + 1, lastpost_unix = ? WHERE id = ? AND COALESCE(lastpost_unix, 0) <= ?`

// TryReserveWebPost atomically checks the web posting back-off for userID and, when it has
// passed, records now as the last post time and increments post_count.
// It returns false when the user posted within the last backoffSeconds (or does not exist).
func (db *Database) TryReserveWebPost(userID int64, now int64, backoffSeconds int64) (bool, error) {
	res, err := RetryableExec(db.mainDB, query_TryReserveWebPost, now, userID, now-backoffSeconds)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}
