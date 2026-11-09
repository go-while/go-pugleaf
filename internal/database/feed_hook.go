package database

// The commit-point hook of the outgoing transit feed (plan nntp-peering, slice feed-queue).
//
// internal/feed must not be imported here (the feed imports this package for the SQLite
// retry helpers and the peers table), so the batch writer calls a registered function
// instead: cmd/nntp-server installs internal/feed's enqueuer with SetFeedHook (and cmd/web,
// for the web posts its post queue worker files), and without it the whole thing costs one
// atomic load per batch.
//
// The hook is called once per ARTICLE, not once per group copy: processNewsgroupBatch takes
// the snapshot where the last group copy of an article is done (NewsgroupsPtr has become
// empty), just before the fields are cleared. Only articles the NNTP feed, an NNTP POST or a
// web post brought in are passed on (models.Article.FeedOut, set by the processor for a
// non-bulk ProcessArticle or a Posted article); fetched and imported articles are never
// queued for a peer. A web post is filed as one copy per group, so a crosspost may call the
// hook once per copy; the queue keeps one row per peer and message-id.

import (
	"strings"
	"sync/atomic"

	"github.com/go-while/go-pugleaf/internal/common"
	"github.com/go-while/go-pugleaf/internal/models"
)

// FeedArticle is the snapshot of one committed article the outgoing feed needs to decide
// which peers get it. It holds no pointers into the models.Article it was taken from, so it
// stays valid after the batch writer cleared that struct.
type FeedArticle struct {
	// MessageID of the article.
	MessageID string
	// Path is the stored Path header, with this server's identity already prepended: the
	// feed skips a peer whose path_token it names.
	Path string
	// Newsgroups are the groups of the article's Newsgroups header (out_patterns, GN, Nm/Nu).
	Newsgroups []string
	// Newsgroup is one group the article is stored in: where the sender reads it from.
	Newsgroup string
	// Bytes is the article's wire size, Lines its body line count (the <N / >N flags).
	Bytes int
	Lines int
}

// FeedHook is called once per committed non-bulk article. It must not block: the batch
// writer's goroutine runs it (internal/feed's enqueuer only hands the snapshot to a
// bounded channel and drops it when that channel is full).
type FeedHook func(FeedArticle)

// feedHook holds the installed hook, nil when the outgoing feed is off. A pointer to the
// func value is stored because a func is not comparable and cannot go into an
// atomic.Value directly.
var feedHook atomic.Pointer[FeedHook]

// SetFeedHook installs the hook the batch writer calls at the commit point. A nil hook
// turns it off again. It may be called while the batch writer is running.
func SetFeedHook(h FeedHook) {
	if h == nil {
		feedHook.Store(nil)
		return
	}
	feedHook.Store(&h)
}

// loadFeedHook returns the installed hook or nil. processNewsgroupBatch calls it once per
// batch, so a server without an outgoing feed pays one atomic load per batch.
func loadFeedHook() FeedHook {
	if p := feedHook.Load(); p != nil {
		return *p
	}
	return nil
}

// feedSnapshot takes the snapshot of one article for group. It is called with
// article.Mux held, at the commit point of the article's last group copy and before the
// fields are cleared, so everything it needs is still there.
//
// The newsgroups come from the stored header block: that is the article's own Newsgroups
// header, which is what the peer patterns and the newsfeeds flags are defined against.
// article.NewsgroupsPtr cannot be used for it - the batch writer has just emptied it, and
// it only ever held the groups this server stores the article in.
func feedSnapshot(article *models.Article, group string) FeedArticle {
	snap := FeedArticle{
		MessageID: article.MessageID,
		Path:      article.Path,
		Newsgroup: group,
		Bytes:     article.Bytes,
		Lines:     article.Lines,
	}
	if article.HeadersJSON != "" {
		value := common.HeaderValue(strings.Split(article.HeadersJSON, "\n"), "Newsgroups")
		if groups, err := common.ParseNewsgroups(value); err == nil {
			snap.Newsgroups = groups
		}
	}
	if len(snap.Newsgroups) == 0 && group != "" {
		// No readable header block: the group it is stored in is all we know.
		snap.Newsgroups = []string{group}
	}
	return snap
}
