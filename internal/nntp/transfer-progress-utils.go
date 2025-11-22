package nntp

import (
	"fmt"
	"log"
	"time"
)

// PrintRecentTransfers prints recent transfer results to the console
func (tpdb *TransferProgressDB) PrintRecentTransfers(limit int) error {
	results, err := tpdb.GetRecentTransfers(limit)
	if err != nil {
		return fmt.Errorf("failed to get recent transfers: %v", err)
	}

	if len(results) == 0 {
		log.Printf("No transfer records found for remote '%s' (id=%d)", tpdb.remoteName, tpdb.remoteID)
		return nil
	}

	log.Printf("=== Recent transfers for remote '%s' (showing last %d) ===", tpdb.remoteName, len(results))
	log.Printf("%-30s %-20s %8s %8s %8s %8s %8s %8s %8s %8s",
		"Newsgroup", "Timestamp", "Sent", "Unwanted", "Checked", "Rejected", "Retry", "Skipped", "TXErr", "ConnErr")
	log.Printf("%s", "-------------------------------------------------------------------------------------------------------------------")

	for _, r := range results {
		log.Printf("%-30s %-20s %8d %8d %8d %8d %8d %8d %8d %8d",
			truncateString(r.Newsgroup, 30),
			r.Timestamp.Format("2006-01-02 15:04:05"),
			r.Sent,
			r.Unwanted,
			r.Checked,
			r.Rejected,
			r.Retry,
			r.Skipped,
			r.TXErrors,
			r.ConnErrors,
		)
	}

	return nil
}

// GetTransferStatsByNewsgroup returns aggregated statistics for a specific newsgroup
func (tpdb *TransferProgressDB) GetTransferStatsByNewsgroup(newsgroup string) (*TransferResult, error) {
	tpdb.mu.RLock()
	defer tpdb.mu.RUnlock()

	query := `
		SELECT 
			remote_id,
			newsgroup,
			MAX(timestamp) as last_transfer,
			SUM(sent) as total_sent,
			SUM(unwanted) as total_unwanted,
			SUM(checked) as total_checked,
			SUM(rejected) as total_rejected,
			SUM(retry) as total_retry,
			SUM(skipped) as total_skipped,
			SUM(tx_errors) as total_tx_errors,
			SUM(conn_errors) as total_conn_errors
		FROM transfers
		WHERE remote_id = ? AND newsgroup = ?
		GROUP BY remote_id, newsgroup
	`

	var r TransferResult
	var timestampStr string
	err := tpdb.db.QueryRow(query, tpdb.remoteID, newsgroup).Scan(
		&r.RemoteID,
		&r.Newsgroup,
		&timestampStr,
		&r.Sent,
		&r.Unwanted,
		&r.Checked,
		&r.Rejected,
		&r.Retry,
		&r.Skipped,
		&r.TXErrors,
		&r.ConnErrors,
	)
	if err != nil {
		return nil, err
	}

	// Parse timestamp
	r.Timestamp, err = time.Parse("2006-01-02 15:04:05", timestampStr)
	if err != nil {
		return nil, err
	}

	return &r, nil
}

// GetAllTransferStats returns aggregated statistics across all newsgroups
func (tpdb *TransferProgressDB) GetAllTransferStats() (*TransferResult, error) {
	tpdb.mu.RLock()
	defer tpdb.mu.RUnlock()

	query := `
		SELECT 
			remote_id,
			MAX(timestamp) as last_transfer,
			SUM(sent) as total_sent,
			SUM(unwanted) as total_unwanted,
			SUM(checked) as total_checked,
			SUM(rejected) as total_rejected,
			SUM(retry) as total_retry,
			SUM(skipped) as total_skipped,
			SUM(tx_errors) as total_tx_errors,
			SUM(conn_errors) as total_conn_errors
		FROM transfers
		WHERE remote_id = ?
		GROUP BY remote_id
	`

	var r TransferResult
	var timestampStr string
	err := tpdb.db.QueryRow(query, tpdb.remoteID).Scan(
		&r.RemoteID,
		&timestampStr,
		&r.Sent,
		&r.Unwanted,
		&r.Checked,
		&r.Rejected,
		&r.Retry,
		&r.Skipped,
		&r.TXErrors,
		&r.ConnErrors,
	)
	if err != nil {
		return nil, err
	}

	// Parse timestamp
	r.Timestamp, err = time.Parse("2006-01-02 15:04:05", timestampStr)
	if err != nil {
		return nil, err
	}

	r.Newsgroup = "ALL"
	return &r, nil
}

// truncateString truncates a string to the specified length
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}
