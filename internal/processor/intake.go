package processor

// Article-layer intake checks and policy for ProcessArticle (plan nntp-intake-injection, S3):
// strict header validation for NNTP intake, newsgroup resolution against the newsgroups
// table (D1/D2/D8), the Path/Xref helpers for the stored header block (D7, A-intake-6),
// claim release on temporary failures (K5) and the capture limiter (A-tools-1).

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-while/go-pugleaf/internal/common"
	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/history"
	"github.com/go-while/go-pugleaf/internal/models"
)

// intakeGroupDecision applies the D1/D2 policy to one resolved newsgroup row:
// inactive → drop; status 'y' → keep; 'm' → keep, moderated; 'n' → keep unless the
// article was posted (POST: posting not permitted, feeds are accepted); any other
// status → drop.
func intakeGroupDecision(g database.IntakeGroup, posted bool) (keep bool, moderated bool) {
	if !g.Active {
		return false, false
	}
	switch g.Status {
	case "y":
		return true, false
	case "m":
		return true, true
	case "n":
		return !posted, false
	}
	return false, false
}

// resolveIntakeGroups looks up every syntactically valid newsgroup name of a non-bulk
// article in the newsgroups table and returns the groups the article may be stored in
// (D1, D2, D8, D11). Reserved names, names without a row, inactive groups, groups whose
// status forbids the article and groups whose max_art_size is below wireSize are dropped
// (one [INTAKE] log line each). wireSize is the size of the article as it arrived (the
// caller carries it in from the read, see models.Article.IntakeWireSize), so the group limit
// measures the same octets as the server limit and the peer's in_max_size. It never opens a
// group database.
// Errors: a failed lookup wraps common.ErrIntakeTemporary; a kept moderated group without
// approval or an empty result wraps common.ErrIntakeRejected.
func (proc *Processor) resolveIntakeGroups(names []string, posted, approved bool, wireSize int) (keep []*database.IntakeGroup, err error) {
	moderated := false
	for _, name := range names {
		if common.IsReservedGroupName(name) {
			log.Printf("[INTAKE] newsgroup '%s' dropped: reserved name", name)
			continue
		}
		g, err := proc.DB.GetIntakeGroup(name)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				log.Printf("[INTAKE] newsgroup '%s' dropped: no such newsgroup", name)
				continue
			}
			return nil, fmt.Errorf("%w: newsgroup lookup: %v", common.ErrIntakeTemporary, err)
		}
		ok, mod := intakeGroupDecision(*g, posted)
		if !ok {
			if !g.Active {
				log.Printf("[INTAKE] newsgroup '%s' dropped: inactive", name)
			} else {
				log.Printf("[INTAKE] newsgroup '%s' dropped: status '%s' does not accept this article (posted=%v)", name, g.Status, posted)
			}
			continue
		}
		if g.MaxArtSize > 0 && int64(wireSize) > g.MaxArtSize {
			log.Printf("[INTAKE] newsgroup '%s' dropped: article of %d bytes exceeds max_art_size %d", name, wireSize, g.MaxArtSize)
			continue
		}
		moderated = moderated || mod
		keep = append(keep, g)
	}
	if moderated && !approved {
		return nil, fmt.Errorf("%w: moderated group without Approved", common.ErrIntakeRejected)
	}
	if len(keep) == 0 {
		return nil, fmt.Errorf("%w: no valid newsgroup", common.ErrIntakeRejected)
	}
	return keep, nil
}

// releaseClaim gives up the CaseLock claim on a message-id after a temporary failure (K5):
// CheckMessageID and the next ProcessArticle fall through to the history lookup again,
// so the article can be retried.
func releaseClaim(item *history.MessageIdItem) {
	if item == nil {
		return
	}
	item.Mux.Lock()
	item.Response = history.CaseError
	item.CachedEntryExpires = time.Now().Add(history.ErrorCaseTTL)
	item.Mux.Unlock()
}

// rememberBulkReject remembers an article-level refusal of the bulk (fetcher/importer) path in
// the history index (plan nntp-peering, INN remembertrash), so a peer offering the message-id
// later gets "not wanted". A site post from the web queue is never remembered: PostQueueWorker
// marks its copies Posted, and like a POST the user may send the article again.
// Called with article.Mux held.
func (proc *Processor) rememberBulkReject(article *models.Article) {
	if article == nil || article.Posted {
		return
	}
	proc.RememberReject(article.MessageID)
}

// controlRefusalLogMax bounds the groups the bulk-path control refusal logs: past it no
// further group is logged (decision 6 of plan nntp-peering, bounded memory).
const controlRefusalLogMax = 4096

// controlRefusalLogSet remembers the groups whose control refusal was already logged, so a
// feed or an import full of control messages logs one line per group per run.
type controlRefusalLogSet struct {
	mu     sync.Mutex
	groups map[string]struct{}
	capped bool
}

// first reports whether this is the first bulk-path control refusal of group in this run.
func (s *controlRefusalLogSet) first(group string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, seen := s.groups[group]; seen {
		return false
	}
	if len(s.groups) >= controlRefusalLogMax {
		if !s.capped {
			s.capped = true
			log.Printf("[INTAKE] control refusals logged for %d groups, further groups are not logged", len(s.groups))
		}
		return false
	}
	if s.groups == nil {
		s.groups = make(map[string]struct{}, 16)
	}
	s.groups[group] = struct{}{}
	return true
}

// controlRefusals is the process-wide log state of the bulk-path control refusals.
var controlRefusals controlRefusalLogSet

// bulkControlHeader returns the control command of a bulk (fetcher/importer) article, or ""
// when it carries no Control header. It looks at the parsed headers first and at the checked
// header block (nil when the article has none) second, so an article is refused whichever
// of the two the bulk caller filled.
func bulkControlHeader(article *models.Article, lines []string) string {
	ctl := common.GetHeaderFirst(article.Headers, "control")
	if ctl == "" && lines != nil {
		ctl = common.HeaderValue(lines, "Control")
	}
	ctl = strings.TrimSpace(ctl)
	if ctl == "" {
		return ""
	}
	if f := strings.Fields(ctl); len(f) > 0 {
		return f[0] // the control command, never the whole client-supplied line
	}
	return ctl
}

// intakeHeaderMode returns the header rules of a non-bulk (NNTP) article: POST is always
// strict (the nnrpd rules), a peer feed follows the header_check of the peer that fed it
// (plan e2e-gap-tests, D1/D4: "strict" = nnrpd, anything else = relay, the innd rules).
// The NNTP layer puts the peer's setting into article.IntakeHeaderCheck.
func intakeHeaderMode(article *models.Article) common.HeaderBlockMode {
	if article.Posted || article.IntakeHeaderCheck == database.PeerHeaderCheckStrict {
		return common.HeaderBlockStrict
	}
	return common.HeaderBlockRelay
}

// logHeaderDefects logs the defects a lax mode accepted, at most one line per kind and
// article, like innd (art.c: "Article accepted but includes CR without LF").
func logHeaderDefects(messageID string, d common.HeaderDefects) {
	if d.BareCR > 0 {
		log.Printf("[INTAKE] article %s accepted but includes CR without LF(%d)", messageID, d.BareCR)
	}
	if d.BareLF > 0 {
		log.Printf("[INTAKE] article %s accepted but includes LF without CR(%d)", messageID, d.BareLF)
	}
}

// intakeBodyHasNUL reports whether the article body holds a NUL octet (decision D2: refused
// on POST, IHAVE, TAKETHIS and on the fetch path, like INN).
func intakeBodyHasNUL(article *models.Article) bool {
	if common.BodyHasNUL(article.NNTPbody) {
		return true
	}
	return strings.IndexByte(article.BodyText, 0) >= 0
}

// validateIntakeHeaders checks the header block of a non-bulk (NNTP) article in mode (see
// intakeHeaderMode): common.CheckHeaderBlockDefects, the mandatory headers present and
// non-empty, the Message-ID header equal to messageID and syntactically valid, no Control
// header (D3, control messages are refused). A Supersedes header is accepted and only
// logged (D3). approved reports a non-empty Approved header (D2); defects counts what a lax
// mode accepted. Errors wrap common.ErrIntakeRejected with a fixed K4 reason.
func validateIntakeHeaders(lines []string, messageID string, mode common.HeaderBlockMode) (normalized []string, approved bool, defects common.HeaderDefects, err error) {
	normalized, defects, err = common.CheckHeaderBlockDefects(lines, mode)
	if err != nil {
		return nil, false, common.HeaderDefects{}, err
	}
	// mandatory headers of a Netnews article (RFC 5536 §3.1)
	for _, name := range []string{"Date", "From", "Message-ID", "Newsgroups", "Path", "Subject"} {
		if common.HeaderValue(normalized, name) == "" {
			return nil, false, common.HeaderDefects{}, fmt.Errorf("%w: missing mandatory header", common.ErrIntakeRejected)
		}
	}
	if common.HeaderValue(normalized, "Message-ID") != messageID || !common.ValidMessageID(messageID) {
		return nil, false, common.HeaderDefects{}, fmt.Errorf("%w: invalid message-id", common.ErrIntakeRejected)
	}
	if ctl := common.HeaderValue(normalized, "Control"); ctl != "" {
		ctlType := ctl
		if f := strings.Fields(ctl); len(f) > 0 {
			ctlType = f[0]
		}
		log.Printf("[INTAKE] control message %s (%s) refused: control messages are not processed", messageID, ctlType)
		return nil, false, common.HeaderDefects{}, fmt.Errorf("%w: control messages not accepted", common.ErrIntakeRejected)
	}
	if common.HeaderValue(normalized, "Supersedes") != "" {
		log.Printf("[INTAKE] Supersedes ignored for %s", messageID)
	}
	return normalized, common.HeaderValue(normalized, "Approved") != "", defects, nil
}

// intakeDateLimits returns the past and future margins of a non-bulk intake. A POST keeps
// the RFC 5537 §3.7 future margin MaxFutureDate and has no past cutoff here (the NNTP POST
// handler already applied nntp.PostDateMaxAge to the proto-article). A peer feed uses the
// two admin settings PeerArticleMaxAgeHours / PeerArticleMaxFutureHours (INN artcutoff and
// DATE_FUZZ; 0 = off), in both header modes.
func intakeDateLimits(db *database.Database, posted bool) (maxAge, maxFuture time.Duration) {
	if posted {
		return 0, MaxFutureDate
	}
	return db.PeerDateMaxAge(), db.PeerDateMaxFuture()
}

// checkIntakeDates parses the Date of a non-bulk article strictly (D10) and checks the
// margins against now, measured on the Injection-Date when it is present and parseable,
// else on the Date. maxFuture <= 0 accepts any future date, maxAge <= 0 any past date.
// It returns the parsed Date.
func checkIntakeDates(dateStr, injectionDate string, now time.Time, maxAge, maxFuture time.Duration) (time.Time, error) {
	t, ok := ParseNNTPDateStrict(dateStr)
	if !ok || t.IsZero() {
		return time.Time{}, fmt.Errorf("%w: invalid date", common.ErrIntakeRejected)
	}
	measured := t
	if injectionDate != "" {
		if it, iok := ParseNNTPDateStrict(injectionDate); iok && !it.IsZero() {
			measured = it
		}
	}
	if maxFuture > 0 && measured.After(now.Add(maxFuture)) {
		return time.Time{}, fmt.Errorf("%w: date in the future", common.ErrIntakeRejected)
	}
	if maxAge > 0 && measured.Before(now.Add(-maxAge)) {
		return time.Time{}, fmt.Errorf("%w: date too old", common.ErrIntakeRejected)
	}
	return t, nil
}

// stripXrefLines removes every Xref header (with continuation lines): Xref is local to
// the server that wrote it and is never stored from a received article (INT-4, A-intake-6).
func stripXrefLines(lines []string) []string {
	return common.RemoveHeaders(lines, "Xref")
}

// syncStoredPath replaces the value of the block's Path line with path, so the stored
// header block serves the same Path as the path column (D7). A block without a Path line
// (web posts) is returned unchanged.
func syncStoredPath(lines []string, path string) []string {
	for _, line := range lines {
		if line == "" || line[0] == ' ' || line[0] == '\t' {
			continue
		}
		if colon := strings.IndexByte(line, ':'); colon > 0 && strings.EqualFold(strings.TrimSpace(line[:colon]), "Path") {
			return common.ReplaceHeader(lines, "Path", path)
		}
	}
	return lines
}

// captureLimiter bounds the goroutines parked in BatchCaptureOverviewForLater (A-tools-1).
type captureLimiter struct {
	slots    chan struct{}
	overflow atomic.Int64 // acquire(false) calls that found no free slot
	lastLog  atomic.Int64 // Unix time of the last overflow log line
}

// newCaptureLimiter returns a limiter with n slots (n < 1 → 1).
func newCaptureLimiter(n int) *captureLimiter {
	if n < 1 {
		n = 1
	}
	return &captureLimiter{slots: make(chan struct{}, n)}
}

// acquire returns true when a slot was taken. wait=false never blocks (overflow allowed,
// counted, one log line per minute "[INTAKE] capture limiter full: %d overflow"). wait=true
// blocks until a slot is free, polling stop() every 100 ms; when stop() reports true it
// returns false (the caller proceeds without a slot).
func (l *captureLimiter) acquire(wait bool, stop func() bool) bool {
	select {
	case l.slots <- struct{}{}:
		return true
	default:
	}
	if !wait {
		n := l.overflow.Add(1)
		now := time.Now().Unix()
		if last := l.lastLog.Load(); now-last >= 60 && l.lastLog.CompareAndSwap(last, now) {
			log.Printf("[INTAKE] capture limiter full: %d overflow", n)
		}
		return false
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if stop != nil && stop() {
			return false
		}
		select {
		case l.slots <- struct{}{}:
			return true
		case <-ticker.C:
		}
	}
}

// release frees one slot (non-blocking receive; a release without a slot is a no-op).
func (l *captureLimiter) release() {
	select {
	case <-l.slots:
	default:
	}
}

// MaxCaptureInFlight is the number of articles whose overview capture may be in flight at
// once (A-tools-1). Read once, at the first use of the limiter.
var MaxCaptureInFlight = 2048

var (
	defaultCaptureLimiter     *captureLimiter // built once from MaxCaptureInFlight
	defaultCaptureLimiterOnce sync.Once
)

// captureLimit returns the process-wide capture limiter.
func captureLimit() *captureLimiter {
	defaultCaptureLimiterOnce.Do(func() {
		defaultCaptureLimiter = newCaptureLimiter(MaxCaptureInFlight)
	})
	return defaultCaptureLimiter
}

// CaptureInFlight returns the number of capture slots currently taken (A-tools-1).
func CaptureInFlight() int {
	return len(captureLimit().slots)
}
