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

// query_ReleaseWebPost undoes a reservation made by TryReserveWebPost: it decrements post_count
// again and restores the previous last post time, but only while lastpost_unix still holds the
// value that reservation wrote (so a newer post is never rolled back).
const query_ReleaseWebPost = `UPDATE users SET post_count = MAX(post_count - 1, 0), lastpost_unix = ? WHERE id = ? AND lastpost_unix = ?`

// ReleaseWebPost gives back a reservation made by TryReserveWebPost(userID, reservedAt, ...)
// when the article could not be queued, so the user is neither charged a post nor blocked by
// the back-off. prevLastPost is the user's lastpost_unix from before the reservation.
// It returns false when the reservation was already replaced by a newer post.
func (db *Database) ReleaseWebPost(userID int64, reservedAt int64, prevLastPost int64) (bool, error) {
	res, err := RetryableExec(db.mainDB, query_ReleaseWebPost, prevLastPost, userID, reservedAt)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}
