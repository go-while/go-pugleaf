package web

import (
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/go-while/go-pugleaf/internal/models"
)

// adminCreateCronJob creates a new cron job
func (s *WebServer) adminCreateCronJob(c *gin.Context) {
	if !s.requireAdminAuth(c) {
		return
	}
	session := s.getWebSession(c)

	// Get form data
	name := strings.TrimSpace(c.PostForm("name"))
	command := strings.TrimSpace(c.PostForm("command"))
	intervalStr := c.PostForm("interval_minutes")
	startHourMinute := strings.TrimSpace(c.PostForm("start_hour_minute"))
	enabled := c.PostForm("enabled") == "on"

	// Validate input
	if name == "" {
		session.SetError("Cron job name is required")
		c.Redirect(http.StatusSeeOther, "/admin?tab=crons")
		return
	}

	if command == "" {
		session.SetError("Command is required")
		c.Redirect(http.StatusSeeOther, "/admin?tab=crons")
		return
	}

	intervalMinutes, err := strconv.Atoi(intervalStr)
	if err != nil || intervalMinutes < 1 {
		session.SetError("Invalid interval minutes")
		c.Redirect(http.StatusSeeOther, "/admin?tab=crons")
		return
	}

	// Create cron job
	cronJob := &models.CronJob{
		Name:            name,
		Command:         command,
		IntervalMinutes: intervalMinutes,
		StartHourMinute: startHourMinute,
		Enabled:         enabled,
	}

	err = s.DB.InsertCronJob(cronJob)
	if err != nil {
		session.SetError("Failed to create cron job: " + err.Error())
		c.Redirect(http.StatusSeeOther, "/admin?tab=crons")
		return
	}

	session.SetSuccess("Cron job created successfully")
	c.Redirect(http.StatusSeeOther, "/admin?tab=crons")
}

// adminUpdateCronJob updates an existing cron job
func (s *WebServer) adminUpdateCronJob(c *gin.Context) {
	if !s.requireAdminAuth(c) {
		return
	}

	session := s.getWebSession(c)

	if !s.CronEdit {
		session.SetError("Editing Crons is disabled")
		c.Redirect(http.StatusSeeOther, "/admin?tab=crons")
		return
	}

	// Get cron job ID
	cronIDStr := c.PostForm("cron_id")
	cronID, err := strconv.ParseInt(cronIDStr, 10, 64)
	if err != nil {
		session.SetError("Invalid cron job ID")
		c.Redirect(http.StatusSeeOther, "/admin?tab=crons")
		return
	}

	// Get existing cron job
	cronJob, err := s.DB.GetCronJobByID(cronID)
	if err != nil {
		session.SetError("Cron job not found")
		c.Redirect(http.StatusSeeOther, "/admin?tab=crons")
		return
	}

	// Get form data
	name := strings.TrimSpace(c.PostForm("name"))
	command := strings.TrimSpace(c.PostForm("command"))
	intervalStr := strings.TrimSpace(c.PostForm("interval_minutes"))
	startHourMinute := strings.TrimSpace(c.PostForm("start_hour_minute"))
	enabled := c.PostForm("enabled") == "on"

	// Validate input
	if name == "" {
		session.SetError("Cron job name is required")
		c.Redirect(http.StatusSeeOther, "/admin?tab=crons")
		return
	}

	if command == "" {
		session.SetError("Command is required")
		c.Redirect(http.StatusSeeOther, "/admin?tab=crons")
		return
	}

	intervalMinutes, err := strconv.Atoi(intervalStr)
	if err != nil || intervalMinutes < 1 {
		session.SetError("Invalid interval minutes")
		c.Redirect(http.StatusSeeOther, "/admin?tab=crons")
		return
	}

	// Update cron job
	cronJob.Name = name
	cronJob.Command = command
	cronJob.IntervalMinutes = intervalMinutes
	cronJob.StartHourMinute = startHourMinute
	cronJob.Enabled = enabled

	err = s.DB.UpdateCronJob(cronJob)
	if err != nil {
		session.SetError("Failed to update cron job: " + err.Error())
		c.Redirect(http.StatusSeeOther, "/admin?tab=crons")
		return
	}

	session.SetSuccess("Cron job updated successfully")
	c.Redirect(http.StatusSeeOther, "/admin?tab=crons")
}

// adminToggleCronJob toggles a cron job's enabled status
func (s *WebServer) adminToggleCronJob(c *gin.Context) {
	if !s.requireAdminAuth(c) {
		return
	}

	session := s.getWebSession(c)

	// Get cron job ID
	cronIDStr := c.PostForm("cron_id")
	cronID, err := strconv.ParseInt(cronIDStr, 10, 64)
	if err != nil {
		session.SetError("Invalid cron job ID")
		c.Redirect(http.StatusSeeOther, "/admin?tab=crons")
		return
	}

	err = s.DB.ToggleCronJob(cronID)
	if err != nil {
		session.SetError("Failed to toggle cron job: " + err.Error())
		c.Redirect(http.StatusSeeOther, "/admin?tab=crons")
		return
	}

	session.SetSuccess("Cron job status updated successfully")
	c.Redirect(http.StatusSeeOther, "/admin?tab=crons")
}

// adminDeleteCronJob deletes a cron job
func (s *WebServer) adminDeleteCronJob(c *gin.Context) {
	if !s.requireAdminAuth(c) {
		return
	}

	session := s.getWebSession(c)

	if !s.CronEdit {
		session.SetError("Editing Crons is disabled")
		c.Redirect(http.StatusSeeOther, "/admin?tab=crons")
		return
	}

	// Get cron job ID
	cronIDStr := c.PostForm("cron_id")
	cronID, err := strconv.ParseInt(cronIDStr, 10, 64)
	if err != nil {
		session.SetError("Invalid cron job ID")
		c.Redirect(http.StatusSeeOther, "/admin?tab=crons")
		return
	}

	err = s.DB.DeleteCronJob(cronID)
	if err != nil {
		session.SetError("Failed to delete cron job: " + err.Error())
		c.Redirect(http.StatusSeeOther, "/admin?tab=crons")
		return
	}

	session.SetSuccess("Cron job deleted successfully")
	c.Redirect(http.StatusSeeOther, "/admin?tab=crons")
}

// adminViewCronJobLog displays the log output for a specific cron job
func (s *WebServer) adminViewCronJobLog(c *gin.Context) {
	// Check authentication and admin permissions
	session := s.getWebSession(c)
	if session == nil {
		c.String(http.StatusUnauthorized, "Not authenticated")
		return
	}

	currentUser, err := s.DB.GetUserByID(session.UserID)
	if err != nil {
		c.String(http.StatusInternalServerError, "Failed to load user")
		return
	}

	if !s.isAdmin(currentUser) {
		c.String(http.StatusForbidden, "Admin access required")
		return
	}

	idStr := c.Param("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		c.String(http.StatusBadRequest, "Invalid cron job ID")
		return
	}

	// Get the cron job
	cronJob, err := s.DB.GetCronJobByID(id)
	if err != nil {
		c.String(http.StatusNotFound, "Cron job not found")
		return
	}
	log.Printf("Viewing log for cron job ID %d: %s", cronJob.ID, cronJob.Name)

	// Get the log output from the cron manager
	logOutput := s.CronManager.GetJobOutput(id)

	// Parse limit parameter (default to 1000)
	limit := 1000
	filter := ""
	if limitStr := c.Query("limit"); limitStr != "" {
		if parsedLimit, err := strconv.Atoi(limitStr); err == nil && parsedLimit > 0 {
			limit = parsedLimit
		}
	}

	if filterStr := c.Query("filter"); filterStr != "" {
		filter = filterStr
	}
	// Limit the output to the last N lines
	displayOutput := logOutput
	totalLines := len(logOutput)

	if filter != "" {
		var filteredOutput []string
		filteredOutput = append(filteredOutput, "Filtered log output (filter: '"+filter+"'):")
		for _, line := range logOutput {
			if strings.Contains(line, filter) {
				filteredOutput = append(filteredOutput, line)
			}
		}
		filteredOutput = append(filteredOutput, fmt.Sprintf("--- End of filtered output lines: %d ---", len(filteredOutput)-1))
		displayOutput = filteredOutput
	}
	if totalLines > limit {
		displayOutput = logOutput[totalLines-limit:]
	}

	// Return plain text log for now
	c.Header("Content-Type", "text/plain; charset=utf-8")
	if totalLines > limit {
		c.String(http.StatusOK, "Cron Job: %s\nCommand: %s\nLast Run: %v\nRun Count: %d\n\n--- Log Output (Showing last %d of %d lines) ---\n%s",
			cronJob.Name,
			cronJob.Command,
			cronJob.LastRun,
			cronJob.RunCount,
			limit,
			totalLines,
			strings.Join(displayOutput, "\n"))
	} else {
		c.String(http.StatusOK, "Cron Job: %s\nCommand: %s\nLast Run: %v\nRun Count: %d\n\n--- Log Output (All %d lines) ---\n%s",
			cronJob.Name,
			cronJob.Command,
			cronJob.LastRun,
			cronJob.RunCount,
			totalLines,
			strings.Join(displayOutput, "\n"))
	}
	log.Printf("Displayed log for cron job ID %d", cronJob.ID)
}

// adminStopCronJob stops a running cron job
func (s *WebServer) adminStopCronJob(c *gin.Context) {
	if !s.requireAdminAuth(c) {
		return
	}

	session := s.getWebSession(c)

	// Get cron job ID
	cronIDStr := c.PostForm("cron_id")
	cronID, err := strconv.ParseInt(cronIDStr, 10, 64)
	if err != nil {
		session.SetError("Invalid cron job ID")
		c.Redirect(http.StatusSeeOther, "/admin?tab=crons")
		return
	}

	// Get cron job
	cronJob, err := s.DB.GetCronJobByID(cronID)
	if err != nil {
		session.SetError("Cron job not found")
		c.Redirect(http.StatusSeeOther, "/admin?tab=crons")
		return
	}

	// Stop the job using the cron manager
	err = s.CronManager.StopJob(cronID)
	if err != nil {
		session.SetError("Failed to stop cron job: " + err.Error())
		c.Redirect(http.StatusSeeOther, "/admin?tab=crons")
		return
	}

	session.SetSuccess("Cron job '" + cronJob.Name + "' stopped successfully")
	c.Redirect(http.StatusSeeOther, "/admin?tab=crons")
}
