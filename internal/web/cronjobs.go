package web

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/models"
)

// CronJobManager manages running cron jobs
type CronJobManager struct {
	db           *database.Database
	jobs         map[int64]*CronJob
	mutex        sync.RWMutex
	stopChannel  chan struct{}
	TotalRunning uint64
}

// CronJob represents a currently running or completed cron job
type CronJob struct {
	ID          int64
	Name        string
	Command     string
	IntervalMin int
	IsActive    bool
	LastRun     time.Time
	IsRunning   bool
	PID         int // Process ID of the running command
	Output      []string
	mutex       sync.RWMutex
	stopChan    chan struct{}
}

var maxLogLines int = 25000

// getShellCommand returns the appropriate shell command for the current OS
func getShellCommand(command string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		command = strings.TrimPrefix(command, "./")
		if !strings.HasSuffix(command, ".exe") {
			command = command + ".exe"
		}
		return exec.Command("cmd", "/C", command)
	}
	// For Unix-like systems (Linux, macOS, etc.), use sh for better compatibility
	// sh is POSIX-compliant and available on virtually all Unix-like systems
	return exec.Command("sh", "-c", command)
}

// NewCronJobManager creates a new cron job manager
func NewCronJobManager(db *database.Database) *CronJobManager {
	return &CronJobManager{
		db:          db,
		jobs:        make(map[int64]*CronJob),
		stopChannel: make(chan struct{}),
	}
}

// Start initializes and starts all active cron jobs
func (cm *CronJobManager) StartCronManager() {
	//log.Printf("[CRON] Starting cron job manager...")
	go func(cm *CronJobManager) {
		for {
			if isClosedChannel(cm.stopChannel) {
				return
			}
			// Load all active cron jobs from database
			cronJobs, err := cm.db.GetAllCronJobs()
			if err != nil {
				log.Printf("[CRON] CronManager: Failed to load cron jobs: %v", err)
				return
			}
			created := 0
			// Start each active cron job
		startJobs:
			for _, job := range cronJobs {
				if !job.Enabled {
					continue startJobs
				}
				if err := cm.startJob(job); err != nil {
					if err == ErrCronExists {
						continue startJobs
					}
					log.Printf("[CRON] Failed to start job %d (%s): %v", job.ID, job.Name, err)

				} else {
					log.Printf("[CRON] Started job %d (%s) with interval %d minutes", job.ID, job.Name, job.IntervalMinutes)
					created++
				}

			}
			if created > 0 {
				cm.mutex.Lock()
				log.Printf("[CRON] started: %d | total: %d", created, len(cm.jobs))
				cm.mutex.Unlock()
			}
			time.Sleep(time.Minute)
		}
	}(cm)
}

func isClosedChannel(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// Stop gracefully shuts down the cron job manager
func (cm *CronJobManager) StopCronManager() {
	go func(cm *CronJobManager) {
		log.Printf("[CRON] Stopping cron job manager...")
		close(cm.stopChannel)
		defer cm.db.WG.Done() // (defer MainWG)
		defer log.Printf("[CRON] Cron job manager stopped (defer MainWG)")

		// Collect job IDs while holding lock
		cm.mutex.Lock()
		jobIDs := make([]int64, 0, len(cm.jobs))
		for id := range cm.jobs {
			jobIDs = append(jobIDs, id)
		}
		cm.mutex.Unlock()

		// Stop all jobs without holding manager lock to avoid deadlock
		for _, jobID := range jobIDs {
			go cm.StopJob(jobID)
		}

		for {
			time.Sleep(time.Second)

			// Check if all jobs finished
			cm.mutex.RLock()
			running := cm.TotalRunning
			cm.mutex.RUnlock()

			if running == 0 {
				break
			}

			// Log status of running jobs without holding manager lock
			cm.mutex.RLock()
			for _, job := range cm.jobs {
				job.mutex.RLock()
				if job.PID > 0 {
					log.Printf("[CRON] wait! job id=%d cmd='%s' (pid: %d)", job.ID, job.Command, job.PID)
				}
				job.mutex.RUnlock()
			}
			cm.mutex.RUnlock()
			log.Printf("[CRON] Waiting for all jobs to finish... (still running: %d)", running)
		}

		log.Printf("[CRON] Cron job manager stopped")
	}(cm)
}

// parseStartTime parses a time string in HH:MM format and returns hour and minute
func parseStartTime(timeStr string) (hour, minute int, err error) {
	if timeStr == "" {
		return 0, 0, nil
	}

	parts := strings.Split(timeStr, ":")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("invalid time format, expected HH:MM")
	}

	hour, err = strconv.Atoi(parts[0])
	if err != nil || hour < 0 || hour > 23 {
		return 0, 0, fmt.Errorf("invalid hour: %s", parts[0])
	}

	minute, err = strconv.Atoi(parts[1])
	if err != nil || minute < 0 || minute > 59 {
		return 0, 0, fmt.Errorf("invalid minute: %s", parts[1])
	}

	return hour, minute, nil
}

// calculateNextRun calculates the next run time for a job considering start_hour_minute
func calculateNextRun(job *models.CronJob, now time.Time) (time.Time, error) {
	intervalDuration := time.Duration(job.IntervalMinutes) * time.Minute
	// If no start time specified, just run based on interval from now
	if job.StartHourMinute == "" {
		return now.Add(intervalDuration), nil
	}

	hour, minute, err := parseStartTime(job.StartHourMinute)
	if err != nil {
		log.Printf("Error parsing start time for job %d: %v", job.ID, err)
		return time.Time{}, err
	}

	// Find today's occurrence of the start time
	jobStartTime := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, now.Location())

	// If we haven't reached start time yet, schedule for later
	if jobStartTime.After(now) {
		return jobStartTime, nil
	}

	// We've passed start time - schedule next run based on interval
	// Calculate how many intervals have passed since the start time
	elapsed := now.Sub(jobStartTime)
	intervalsPassed := int(elapsed / intervalDuration)

	// Schedule the next interval that will be in the future
	nextRun := jobStartTime.Add(time.Duration(intervalsPassed+1) * intervalDuration)
	return nextRun, nil
}

// GetJobOutput returns the last output lines for a specific cron job
func (cm *CronJobManager) GetJobOutput(jobID int64) []string {
	cm.mutex.RLock()
	job, exists := cm.jobs[jobID]
	cm.mutex.RUnlock()

	if exists {
		job.mutex.RLock()
		result := make([]string, len(job.Output))
		copy(result, job.Output)
		job.mutex.RUnlock()
		return result
	}

	return []string{"No output available for this job yet."}
}

var ErrCronExists error = fmt.Errorf("cronExists")
var ErrCronNotFound error = fmt.Errorf("cron404")

func (cm *CronJobManager) StopJob(jobId int64) error {
	cm.mutex.Lock()
	defer cm.mutex.Unlock()
	if job, exists := cm.jobs[jobId]; exists {
		if job.stopChan != nil {
			select {
			case job.stopChan <- struct{}{}:
			default:
			}
		}
		job.mutex.Lock()
		if job.IsRunning {
			if job.PID > 0 {
				log.Printf("[CRON] Stopping cron job %d (pid: %d)", job.ID, job.PID)
				// Try to interrupt the process gracefully
				if process, err := os.FindProcess(job.PID); err == nil {
					// Use os.Interrupt which works cross-platform (Ctrl+C equivalent)
					if err := process.Signal(os.Interrupt); err != nil {
						log.Printf("[CRON] Failed to interrupt cron job %d (pid: %d): %v", job.ID, job.PID, err)
						/*
							// If interrupt fails, try to kill the process
							if killErr := process.Kill(); killErr != nil {
								log.Printf("[CRON] Failed to kill cron job %d (pid: %d): %v", job.ID, job.PID, killErr)
							} else {
								log.Printf("[CRON] KILLED cron job %d (pid: %d)", job.ID, job.PID)
							}
						*/
					} else {
						log.Printf("[CRON] Successfully sent interrupt to cron job %d (pid: %d)", job.ID, job.PID)
						// delete(cm.jobs, jobId)
					}
				} else {
					log.Printf("[CRON] Failed to find process for cron job %d (pid: %d): %v", job.ID, job.PID, err)
				}
			} else {
				log.Printf("[CRON] Job %d has no valid PID, cannot send SIGINT", job.ID)
			}
		}
		job.mutex.Unlock()
	} else {
		return ErrCronNotFound
	}

	return nil
}

// startJob starts a specific cron job
func (cm *CronJobManager) startJob(cronJob *models.CronJob) error {
	cm.mutex.Lock()
	defer cm.mutex.Unlock()

	if _, exists := cm.jobs[cronJob.ID]; exists {
		return ErrCronExists
	}

	job := &CronJob{
		ID:          cronJob.ID,
		Command:     cronJob.Command,
		IntervalMin: cronJob.IntervalMinutes,
		IsActive:    cronJob.Enabled,
		LastRun:     time.Time{}, // Will be set on first run
		IsRunning:   false,
		Output:      make([]string, 0, maxLogLines),
		stopChan:    make(chan struct{}),
	}

	cm.jobs[cronJob.ID] = job

	// Start goroutine to handle job execution
	go cm.runJobScheduler(job)

	return nil
}

// runJobScheduler handles the scheduling and execution of a specific job
func (cm *CronJobManager) runJobScheduler(job *CronJob) {
	if isClosedChannel(cm.stopChannel) {
		log.Printf("[CRON] runJobScheduler: is closed, abort job %d/%s", job.ID, job.Name)
		return
	}

	// Get the cron job info to check to update values for job
	cronJob, err := cm.db.GetCronJobByID(job.ID)
	if err != nil {
		log.Printf("[CRON] Failed to get cron job %d: %v", job.ID, err)
		return
	}
	if !cronJob.Enabled {
		log.Printf("[CRON] runJobScheduler: Job %d/%s is no longer enabled.", job.ID, job.Name)
		return
	}

	// Collect changes while holding lock
	var logMessages []string
	job.mutex.Lock()
	if job.Name != cronJob.Name {
		log.Printf("[CRON] Job %d/%s name has changed from '%s' to '%s'", job.ID, job.Name, job.Name, cronJob.Name)
		logMessages = append(logMessages, fmt.Sprintf("[%s] Name changed from '%s' to '%s'", time.Now().Format("2006-01-02 15:04:05"), job.Name, cronJob.Name))
		job.Name = cronJob.Name
	}
	if job.Command != cronJob.Command {
		log.Printf("[CRON] Job %d/%s command has changed from '%s' to '%s'", job.ID, job.Name, job.Command, cronJob.Command)
		logMessages = append(logMessages, fmt.Sprintf("[%s] Command changed from '%s' to '%s'", time.Now().Format("2006-01-02 15:04:05"), job.Command, cronJob.Command))
		job.Command = cronJob.Command
	}
	if job.IntervalMin != cronJob.IntervalMinutes {
		log.Printf("[CRON] Job %d/%s interval has changed from %d minutes to %d minutes", job.ID, job.Name, job.IntervalMin, cronJob.IntervalMinutes)
		logMessages = append(logMessages, fmt.Sprintf("[%s] Interval changed from %d minutes to %d minutes", time.Now().Format("2006-01-02 15:04:05"), job.IntervalMin, cronJob.IntervalMinutes))
		job.IntervalMin = cronJob.IntervalMinutes
	}
	job.mutex.Unlock()

	// Add log messages after releasing lock to avoid holding lock during addLogLine
	for _, msg := range logMessages {
		job.addLogLine(msg)
	}

	// Calculate next run time considering start_hour_minute
	now := time.Now()
	nextRun, err := calculateNextRun(cronJob, now)
	if err != nil {
		log.Printf("[CRON] Failed to calculate next run for job %d/%s: %v", job.ID, job.Name, err)
		return
	}

	if nextRun.After(now) {
		waitDuration := nextRun.Sub(now)
		job.addLogLine(fmt.Sprintf("[%s] Waiting until %s (in %v)", now.Format("2006-01-02 15:04:05"), nextRun.Format("2006-01-02 15:04:05"), waitDuration))
		// Wait
		select {
		case <-job.stopChan:
			log.Printf("[CRON] Job %d/%s signal stopChan", job.ID, job.Name)
			job.addLogLine(fmt.Sprintf("[%s] Job %d/%s got stop signal... quit now.", time.Now().Format("2006-01-02 15:04:05"), job.ID, job.Name))
			return
		case <-time.After(waitDuration):
			// Continue
		}
	}

	// Check if job is already running before executing
	job.mutex.Lock()
	if job.IsRunning {
		job.mutex.Unlock()
		job.addLogLine(fmt.Sprintf("[%s] runJobScheduler: job %d/%s is already running", time.Now().Format("2006-01-02 15:04:05"), job.ID, job.Name))
		time.Sleep(1 * time.Minute)
	} else {
		job.IsRunning = true
		var execWG sync.WaitGroup
		execWG.Add(1)
		go cm.executeJob(job, &execWG)
		job.mutex.Unlock()
		execWG.Wait()
	}
	go cm.runJobScheduler(job)
}

// executeJob executes a single job
func (cm *CronJobManager) executeJob(job *CronJob, execWG *sync.WaitGroup) {
	defer func() {
		job.mutex.Lock()
		job.IsRunning = false
		job.mutex.Unlock()
		cm.mutex.Lock()
		cm.TotalRunning--
		cm.mutex.Unlock()
		execWG.Done()
	}()
	if isClosedChannel(cm.stopChannel) {
		log.Printf("[CRON] executeJob: Cron manager is stopping, aborting job %d", job.ID)
		return
	}

	job.mutex.Lock()
	job.LastRun = time.Now()
	startTime := job.LastRun
	job.mutex.Unlock()
	cm.mutex.Lock()
	cm.TotalRunning++
	cm.mutex.Unlock()

	//job.addLogLine(fmt.Sprintf("[%s] Starting job execution: %s", startTime.Format("2006-01-02 15:04:05"), job.Command))

	// Execute command without timeout
	cmd := getShellCommand(job.Command)

	// Create pipes for stdout and stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		job.addLogLine(fmt.Sprintf("[%s] Failed to create stdout pipe: %v", time.Now().Format("2006-01-02 15:04:05"), err))
		log.Printf("executeJob failed to create stdout pipe: %v", err)
		return
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		job.addLogLine(fmt.Sprintf("[%s] Failed to create stderr pipe: %v", time.Now().Format("2006-01-02 15:04:05"), err))
		return
	}

	// Start command
	if err := cmd.Start(); err != nil {
		job.addLogLine(fmt.Sprintf("[%s] Failed to start command: %v", time.Now().Format("2006-01-02 15:04:05"), err))
		return
	}

	// Capture the process ID
	job.mutex.Lock()
	job.PID = cmd.Process.Pid
	job.mutex.Unlock()
	job.addLogLine(fmt.Sprintf("[%s] PID: %d, Command: '%s'", time.Now().Format("2006-01-02 15:04:05"), cmd.Process.Pid, job.Command))
	defer func() {
		// Clear the PID when done
		job.mutex.Lock()
		job.PID = 0
		job.mutex.Unlock()
	}()
	// Read output in goroutines
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			job.addLogLine(fmt.Sprintf("[%s] STDOUT: %s", time.Now().Format("2006-01-02 15:04:05"), scanner.Text()))
		}
	}()

	go func() {
		defer wg.Done()
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			job.addLogLine(fmt.Sprintf("[%s] STDERR: %s", time.Now().Format("2006-01-02 15:04:05"), scanner.Text()))
		}
	}()

	// Wait for command to complete
	err = cmd.Wait()
	wg.Wait() // Wait for output readers to finish

	duration := time.Since(startTime)
	if err != nil {
		job.addLogLine(fmt.Sprintf("[%s] Job completed with error after %v: %v", time.Now().Format("2006-01-02 15:04:05"), duration, err))
	} else {
		job.addLogLine(fmt.Sprintf("[%s] Job completed successfully after %v", time.Now().Format("2006-01-02 15:04:05"), duration))
	}

	// Update database with last run time and increment run count
	if err := cm.db.UpdateCronJobRunStats(job.ID); err != nil {
		job.addLogLine(fmt.Sprintf("[%s] Failed to update run stats in database: %v", time.Now().Format("2006-01-02 15:04:05"), err))
	}
}

// addLogLine adds a line to the job's output log
func (job *CronJob) addLogLine(line string) {
	job.mutex.Lock()
	defer job.mutex.Unlock()

	job.Output = append(job.Output, line)

	// Keep only the last maxLogLines
	if len(job.Output) > maxLogLines {
		// Remove the oldest lines
		copy(job.Output, job.Output[len(job.Output)-maxLogLines:])
		job.Output = job.Output[:maxLogLines]
	}
}

// GetJobStatus returns the current status of a job
func (cm *CronJobManager) GetJobStatus(jobID int64) (*CronJob, bool) {
	cm.mutex.RLock()
	job, exists := cm.jobs[jobID]
	cm.mutex.RUnlock()
	return job, exists
}

// GetJobPID returns the PID of a running job, or 0 if not running
func (cm *CronJobManager) GetJobPID(jobID int64) int {
	cm.mutex.RLock()
	defer cm.mutex.RUnlock()

	if job, exists := cm.jobs[jobID]; exists {
		job.mutex.RLock()
		pid := job.PID
		job.mutex.RUnlock()
		return pid
	}
	return 0
}

// GetAllJobStatuses returns status of all jobs
func (cm *CronJobManager) GetAllJobStatuses() map[int64]*CronJob {
	cm.mutex.RLock()
	defer cm.mutex.RUnlock()

	result := make(map[int64]*CronJob)
	for id, job := range cm.jobs {
		job.mutex.RLock()
		// Create a copy of the job status
		jobCopy := &CronJob{
			ID:          job.ID,
			Command:     job.Command,
			IntervalMin: job.IntervalMin,
			IsActive:    job.IsActive,
			LastRun:     job.LastRun,
			IsRunning:   job.IsRunning,
			PID:         job.PID,
			Output:      nil, // Don't copy output for status overview
		}
		job.mutex.RUnlock()
		result[id] = jobCopy
	}
	return result
}
