package database

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/go-while/go-pugleaf/internal/models"
)

// Cron Job Database Functions

const query_GetAllCronJobs = `SELECT id, name, command, interval_minutes, start_hour_minute, enabled, last_run, run_count, created_at, updated_at FROM cron_jobs ORDER BY id ASC`

// GetAllCronJobs retrieves all cron jobs
func (db *Database) GetAllCronJobs() ([]*models.CronJob, error) {
	rows, err := RetryableQuery(db.mainDB, query_GetAllCronJobs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var cronJobs []*models.CronJob
	for rows.Next() {
		var cronJob models.CronJob
		var lastRun sql.NullTime

		if err := rows.Scan(&cronJob.ID, &cronJob.Name, &cronJob.Command, &cronJob.IntervalMinutes,
			&cronJob.StartHourMinute, &cronJob.Enabled, &lastRun, &cronJob.RunCount, &cronJob.CreatedAt, &cronJob.UpdatedAt); err != nil {
			return nil, err
		}

		if lastRun.Valid {
			cronJob.LastRun = &lastRun.Time
		}

		cronJobs = append(cronJobs, &cronJob)
	}
	return cronJobs, nil
}

const query_GetCronJobByID = `SELECT id, name, command, interval_minutes, start_hour_minute, enabled, last_run, run_count, created_at, updated_at FROM cron_jobs WHERE id = ?`

// GetCronJobByID retrieves a cron job by ID
func (db *Database) GetCronJobByID(id int64) (*models.CronJob, error) {
	var cronJob models.CronJob
	var lastRun sql.NullTime

	err := RetryableQueryRowScan(db.mainDB, query_GetCronJobByID, []interface{}{id},
		&cronJob.ID, &cronJob.Name, &cronJob.Command, &cronJob.IntervalMinutes,
		&cronJob.StartHourMinute, &cronJob.Enabled, &lastRun, &cronJob.RunCount, &cronJob.CreatedAt, &cronJob.UpdatedAt)
	if err != nil {
		return nil, err
	}

	if lastRun.Valid {
		cronJob.LastRun = &lastRun.Time
	}

	return &cronJob, nil
}

const query_InsertCronJob = `INSERT INTO cron_jobs (name, command, interval_minutes, start_hour_minute, enabled, created_at, updated_at) VALUES (?, ?, ?, ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`

// InsertCronJob creates a new cron job
func (db *Database) InsertCronJob(cronJob *models.CronJob) error {
	_, err := RetryableExec(db.mainDB, query_InsertCronJob, cronJob.Name, cronJob.Command, cronJob.IntervalMinutes, cronJob.StartHourMinute, cronJob.Enabled)
	return err
}

const query_UpdateCronJob = `UPDATE cron_jobs SET name = ?, command = ?, interval_minutes = ?, start_hour_minute = ?, enabled = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`

// UpdateCronJob updates an existing cron job
func (db *Database) UpdateCronJob(cronJob *models.CronJob) error {
	_, err := RetryableExec(db.mainDB, query_UpdateCronJob, cronJob.Name, cronJob.Command, cronJob.IntervalMinutes, cronJob.StartHourMinute, cronJob.Enabled, cronJob.ID)
	return err
}

const query_DeleteCronJob = `DELETE FROM cron_jobs WHERE id = ?`

// DeleteCronJob deletes a cron job
func (db *Database) DeleteCronJob(id int64) error {
	_, err := RetryableExec(db.mainDB, query_DeleteCronJob, id)
	return err
}

// query_SetCronJobEnabled only writes when the row still has the state the caller read, so two
// concurrent toggles cannot both flip it (check-then-write race).
const query_SetCronJobEnabled = `UPDATE cron_jobs SET enabled = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ? AND enabled = ?`

// SetCronJobEnabled sets enabled to the given state when the job is still in state from.
// It reports false (and no error) when no row matched: the job was changed by someone else
// in the meantime, or it no longer exists.
func (db *Database) SetCronJobEnabled(id int64, enabled, from bool) (bool, error) {
	res, err := RetryableExec(db.mainDB, query_SetCronJobEnabled, enabled, id, from)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("rows affected: %w", err)
	}
	return n > 0, nil
}

// ErrCronJobChanged is returned by ToggleCronJob when the job's enabled state changed between
// reading and writing it.
var ErrCronJobChanged = errors.New("cron job was already changed")

// ToggleCronJob enables or disables a cron job. The write is conditional on the state it read
// (SetCronJobEnabled); a concurrent change returns ErrCronJobChanged.
func (db *Database) ToggleCronJob(id int64) error {
	cronJob, err := db.GetCronJobByID(id)
	if err != nil {
		return err
	}
	changed, err := db.SetCronJobEnabled(id, !cronJob.Enabled, cronJob.Enabled)
	if err != nil {
		return err
	}
	if !changed {
		return ErrCronJobChanged
	}
	return nil
}

const query_UpdateCronJobRunStats = `UPDATE cron_jobs SET last_run = ?, run_count = run_count + 1 WHERE id = ?`

// UpdateCronJobRunStats updates the run statistics after a cron job execution
func (db *Database) UpdateCronJobRunStats(id int64) error {
	_, err := RetryableExec(db.mainDB, query_UpdateCronJobRunStats, time.Now(), id)
	return err
}
