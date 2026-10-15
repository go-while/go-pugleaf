package nntp

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"math"
	"strconv"
	"time"

	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/models"
)

// selectableGroup returns the newsgroups row of name when the server carries the group
// (it exists in the main DB and is active). It never opens a group DB, so an unknown or
// inactive name cannot create a group database (SEC-2, A-nntp-sec-4). found is false for
// an unknown or inactive group (411); err is a main DB fault (403, decision 15).
func (c *ClientConnection) selectableGroup(name string) (g *models.Newsgroup, found bool, err error) {
	if name == "" {
		return nil, false, nil
	}
	g, err = c.server.DB.MainDBGetNewsgroup(name)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if g == nil || !g.Active {
		return nil, false, nil
	}
	return g, true, nil
}

// selectGroup opens the group DB of g (which passed selectableGroup), reads the marks of
// its visible articles and selects it: current group g, current article = low (0 for an
// empty group). It returns the group DB (the caller must Return it) and the numbers of the
// "211 count low high group" reply (decision 14): low/high are the lowest/highest visible
// article numbers, count is newsgroups.message_count clamped to high-low+1 (an estimate
// that may include hidden articles, RFC 3977 §6.1.1.2; a count below 1 for a non-empty
// group becomes high-low+1), and an empty group gives 0 0 0.
// On error nothing is changed and no group DB is held.
func (c *ClientConnection) selectGroup(g *models.Newsgroup) (groupDB *database.GroupDB, count, low, high int64, err error) {
	groupDB, err = c.server.DB.GetGroupDB(g.Name)
	if err != nil {
		return nil, 0, 0, 0, err
	}
	low, high, err = c.server.DB.NNTPGroupWatermarks(groupDB)
	if err != nil {
		groupDB.Return()
		return nil, 0, 0, 0, err
	}
	if high <= 0 {
		low, high, count = 0, 0, 0
	} else {
		count = g.MessageCount
		if span := high - low + 1; count > span {
			count = span
		}
		if count < 1 {
			// a stale or zero message_count: a non-empty group must not report 0
			// (RFC 3977 §6.1.1.2: the estimate is at least the actual number)
			count = high - low + 1
		}
	}
	c.currentGroup = g.Name
	c.currentFirst = low
	c.currentLast = high
	c.currentArticle = low
	return groupDB, count, low, high, nil
}

// handleGroup handles GROUP (RFC 3977 §6.1.1).
func (c *ClientConnection) handleGroup(args []string) error {
	if c.server.GroupCmdDelay > 0 {
		time.Sleep(c.server.GroupCmdDelay) // ratelimit (DefaultGroupCmdDelay)
	}

	if len(args) == 0 {
		c.rateLimitOnError()
		return c.sendResponse(501, "GROUP command requires a group name")
	}

	g, ok, err := c.selectableGroup(args[0])
	if err != nil {
		log.Printf("[NNTP] GROUP: newsgroup lookup: %v", err)
		return c.sendResponse(403, "Internal fault")
	}
	if !ok {
		// unknown or inactive: the current group stays unchanged (§6.1.1.2)
		return c.sendResponse(411, "No such newsgroup")
	}
	groupDB, count, low, high, err := c.selectGroup(g)
	if err != nil {
		log.Printf("[NNTP] GROUP %s: %v", g.Name, err)
		return c.sendResponse(403, "Internal fault")
	}
	groupDB.Return()
	return c.sendResponse(211, fmt.Sprintf("%d %d %d %s", count, low, high, g.Name))
}

// handleListGroup handles LISTGROUP [group [range]] (RFC 3977 §6.1.2): it selects the
// group like GROUP and lists the numbers of its visible articles (optionally only those in
// range), read in chunks of database.NNTPListGroupChunk numbers (no overview rows: SEC-7).
func (c *ClientConnection) handleListGroup(args []string) error {
	groupName := c.currentGroup
	if len(args) > 0 {
		groupName = args[0]
	}
	if groupName == "" {
		c.requireGroup() // 412
		return nil
	}

	var r articleRange
	hasRange := len(args) > 1
	if hasRange {
		var err error
		if r, err = c.parseRangeArg(args[1]); err != nil {
			c.rateLimitOnError()
			return c.sendResponse(501, "Syntax error in range")
		}
	}

	g, ok, err := c.selectableGroup(groupName)
	if err != nil {
		log.Printf("[NNTP] LISTGROUP: newsgroup lookup: %v", err)
		return c.sendResponse(403, "Internal fault")
	}
	if !ok {
		if len(args) == 0 {
			// the selected group was deactivated or deleted since GROUP: no group is
			// selected any more (requireGroup)
			c.dropCurrentGroup()
			c.requireGroup() // 412
			return nil
		}
		return c.sendResponse(411, "No such newsgroup")
	}
	groupDB, count, low, high, err := c.selectGroup(g)
	if err != nil {
		log.Printf("[NNTP] LISTGROUP %s: %v", g.Name, err)
		return c.sendResponse(403, "Internal fault")
	}
	defer groupDB.Return()

	first, last := low, high
	if hasRange {
		first, last = r.First, r.Last
	}

	if err := c.sendResponse(211, fmt.Sprintf("%d %d %d %s", count, low, high, g.Name)); err != nil {
		return err
	}
	d := c.newDataBlock()
	// An empty group, an Empty() range or a range outside the group gives an empty list.
	if high > 0 && first <= last {
		for {
			nums, err := c.server.DB.NNTPArticleNumbers(groupDB, first, last, database.NNTPListGroupChunk)
			if err != nil {
				// The status line is out: no silent truncation, the connection closes (decision 15).
				log.Printf("[NNTP] LISTGROUP %s %d-%d: %v", g.Name, first, last, err)
				return fmt.Errorf("LISTGROUP %s: %w", g.Name, err)
			}
			for _, n := range nums {
				if err := d.Line(strconv.FormatInt(n, 10)); err != nil {
					return err
				}
			}
			if len(nums) < database.NNTPListGroupChunk {
				break
			}
			lastNum := nums[len(nums)-1]
			if lastNum >= last || lastNum == math.MaxInt64 {
				break
			}
			first = lastNum + 1
		}
	}
	return d.Close()
}
