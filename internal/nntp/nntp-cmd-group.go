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
	if c.server == nil || c.server.DB == nil || c.server.DB.GetMainDB() == nil {
		// without the main database the group cannot be checked, so it is not opened either
		// (SEC-2); the caller answers 403 like on any other main DB fault (decision 15)
		return nil, false, fmt.Errorf("group %s: main database is not open", name)
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

// openSelectableGroupDB opens the group database of name, but only when the server still
// carries the group: selectableGroup re-checks the main DB first, so a name that is unknown
// or inactive by now is never passed to database.GetGroupDB (which would create its group
// database file for any name). Used wherever a command opens a group it did not just select
// through GROUP/LISTGROUP: the group a session selected earlier (an admin may have
// deactivated or deleted it since) and a group name a message-id lookup produced (the
// history index may name a group that is gone). SEC-2 residual.
//
// found is false for an unknown or inactive group (the caller decides the reply: 412 for the
// selected group, 430 for a message-id lookup); err is a main DB or group DB fault (403,
// decision 15). The caller must Return the group DB.
func (c *ClientConnection) openSelectableGroupDB(name string) (groupDB *database.GroupDB, found bool, err error) {
	g, ok, err := c.selectableGroup(name)
	if err != nil || !ok {
		return nil, false, err
	}
	groupDB, err = c.server.DB.GetGroupDB(g.Name)
	if err != nil {
		return nil, false, err
	}
	return groupDB, true, nil
}

// openCurrentGroupDB opens the group database of the selected group for a group-relative
// command (openSelectableGroupDB). When the server does not carry the group any more the
// session drops the selection and the command answers "412 No newsgroup selected", as if no
// group had been selected (requireGroup/currentGroupSelectable); a main or group DB fault
// answers 403 (decision 15). what names the command for the log line.
//
// ok is false when the reply has been sent: the caller returns err (the send error, nil for
// the dropped 412 send error as in requireGroup). The caller must Return the group DB.
func (c *ClientConnection) openCurrentGroupDB(what string) (groupDB *database.GroupDB, ok bool, err error) {
	groupDB, found, err := c.openSelectableGroupDB(c.currentGroup)
	if err != nil {
		log.Printf("[NNTP] %s: group DB %s: %v", what, c.currentGroup, err)
		return nil, false, c.sendResponse(403, "Internal fault")
	}
	if !found {
		// deactivated or deleted since it was selected: no group is selected any more
		c.dropCurrentGroup()
		c.requireGroup() // 412
		return nil, false, nil
	}
	return groupDB, true, nil
}

// currentGroupDBForMsgID opens the group database of the selected group for a message-id
// lookup, which searches the current group first (§6.2.1.3). The message-id forms need no
// group, so a selection the server does not carry any more is dropped and ok is false
// without a reply: the lookup goes on with its other sources and ends in 430 when none of
// them has the article (a 412 would be wrong for a form that needs no group).
//
// ok is also false, with err nil, when no group is selected. err is a main DB or group DB
// fault and leaves the selection alone (the caller logs it). The caller must Return the
// group DB.
func (c *ClientConnection) currentGroupDBForMsgID() (groupDB *database.GroupDB, ok bool, err error) {
	if c.currentGroup == "" {
		return nil, false, nil
	}
	groupDB, found, err := c.openSelectableGroupDB(c.currentGroup)
	if err != nil {
		return nil, false, err
	}
	if !found {
		c.dropCurrentGroup()
		return nil, false, nil
	}
	return groupDB, true, nil
}

// selectGroup opens the group DB of g (which passed selectableGroup), reads the marks of
// its visible articles and selects it: current group g, current article = low (0 for an
// empty group). It returns the group DB (the caller must Return it) and the numbers of the
// "211 count low high group" reply (decision 14): low/high are the lowest/highest visible
// article numbers, count is newsgroups.message_count clamped to high-low+1 (an estimate
// that may include hidden articles, RFC 3977 §6.1.1.2; a count below 1 for a non-empty
// group becomes high-low+1). A group without visible articles gives "0 next next-1", the
// form RFC 3977 §6.1.1.2 recommends, with next = the number the next stored article gets
// (NNTPGroupNextArticleNum): "0 1 0" for a never-used group, "0 6 5" after articles 1..5
// were all expired or hidden; there is no current article then.
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
		next, nerr := c.server.DB.NNTPGroupNextArticleNum(groupDB)
		if nerr != nil {
			groupDB.Return()
			return nil, 0, 0, 0, nerr
		}
		low, high, count = next, next-1, 0
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
	if count == 0 {
		c.currentArticle = 0 // empty group: the current article number is invalid (§6.1.1.2)
	}
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
	// An empty group (count 0), an Empty() range or a range outside the group gives an empty list.
	if count > 0 && first <= last {
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
