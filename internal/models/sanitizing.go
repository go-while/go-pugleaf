package models

import (
	"encoding/base64"
	"fmt"
	"html"
	"html/template"
	"io"
	"log"
	"mime"
	"mime/quotedprintable"
	"net/mail"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/go-while/go-pugleaf/internal/cache"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/htmlindex"
	"golang.org/x/text/transform"
)

var (
	mimeWordRegex  = regexp.MustCompile(`=\?([^?]+)\?([QqBb])\?([^?]*)\?=`)
	spaceRegex     = regexp.MustCompile(`\s+`)
	angleAddrRegex = regexp.MustCompile(`<[^<>]*>`)
)

// Security and sanitization methods

// ConvertToUTF8 converts text from Latin-1 to UTF-8 if needed, decodes MIME encoded-words and HTML entities
// This function is used for properly decoding newsgroup text content without HTML escaping
func ConvertToUTF8(text string) string {
	// First try standard MIME decoding (RFC 2047)
	decoder := mime.WordDecoder{}
	mimeDecoded, err := decoder.DecodeHeader(text)
	if err != nil {
		// If standard MIME decoding fails, try custom decoding for unsupported charsets
		mimeDecoded = decodeUnsupportedMIME(text)
	}

	qpDecoded := decodeQuotedPrintable(mimeDecoded)

	// Then decode HTML entities if present
	htmlDecoded := html.UnescapeString(qpDecoded)

	// Check if already valid UTF-8
	if utf8.ValidString(htmlDecoded) {
		return htmlDecoded
	}

	// Try Latin-1 (ISO-8859-1) to UTF-8 conversion
	charsetDecoder := charmap.ISO8859_1.NewDecoder()
	result, _, err := transform.String(charsetDecoder, htmlDecoded)
	if err != nil {
		// Fallback: replace invalid UTF-8 sequences with replacement character
		return strings.ToValidUTF8(htmlDecoded, "�")
	}
	return result
}

func decodeQuotedPrintable(text string) string {
	reader := quotedprintable.NewReader(strings.NewReader(text))
	decoded, err := io.ReadAll(reader)
	if err != nil {
		return text // fallback to original if decoding fails
	}
	return string(decoded)
}

// decodeUnsupportedMIME decodes MIME encoded-words using extended charset support
// This function handles charsets that Go's standard mime.WordDecoder doesn't support,
// such as ISO-8859-15 and many other legacy charsets
func decodeUnsupportedMIME(text string) string {
	// MIME encoded-word pattern: =?charset?encoding?encoded-text?=

	result := mimeWordRegex.ReplaceAllStringFunc(text, func(match string) string {
		parts := mimeWordRegex.FindStringSubmatch(match)
		if len(parts) != 4 {
			return match // Return original if parsing fails
		}

		charset := strings.ToLower(strings.TrimSpace(parts[1]))
		encoding := strings.ToUpper(parts[2])
		encodedText := parts[3]

		// Decode the encoded text based on encoding type
		var decodedBytes []byte
		var err error

		switch encoding {
		case "B": // Base64
			decodedBytes, err = base64.StdEncoding.DecodeString(encodedText)
		case "Q": // Quoted-Printable
			// Replace underscores with spaces (Q-encoding specific)
			qpText := strings.ReplaceAll(encodedText, "_", " ")
			reader := quotedprintable.NewReader(strings.NewReader(qpText))
			decodedBytes, err = io.ReadAll(reader)
		default:
			return match // Unknown encoding, return original
		}

		if err != nil {
			return match // Decoding failed, return original
		}

		// Convert from the specified charset to UTF-8
		utf8Text, err := decodeCharsetToUTF8(decodedBytes, charset)
		if err != nil {
			// Fallback: try to interpret as Latin-1
			charsetDecoder := charmap.ISO8859_1.NewDecoder()
			if result, _, fallbackErr := transform.String(charsetDecoder, string(decodedBytes)); fallbackErr == nil {
				return result
			}
			// Last resort: return as UTF-8 with replacement chars
			return strings.ToValidUTF8(string(decodedBytes), "�")
		}

		return utf8Text
	})

	return result
}

// decodeCharsetToUTF8 converts bytes from the specified charset to UTF-8 string
// Uses golang.org/x/text/encoding/htmlindex for extended charset support
func decodeCharsetToUTF8(data []byte, charset string) (string, error) {
	charset = normalizeCharsetName(charset)

	// Handle UTF-8 directly
	if charset == "utf-8" || charset == "utf8" {
		return string(data), nil
	}

	// Get encoding from htmlindex (supports many more charsets than Go's standard library)
	enc, err := htmlindex.Get(charset)
	if err != nil {
		return "", fmt.Errorf("unsupported charset: %s", charset)
	}

	if enc == nil {
		// UTF-8 case (htmlindex returns nil for UTF-8)
		return string(data), nil
	}

	// Decode using the charset
	decoder := enc.NewDecoder()
	result, _, err := transform.String(decoder, string(data))
	if err != nil {
		return "", fmt.Errorf("failed to decode from %s: %v", charset, err)
	}

	return result, nil
}

// normalizeCharsetName normalizes charset names to match htmlindex expectations
// Based on the approach used in ProtonMail's go-mime library
func normalizeCharsetName(charset string) string {
	// Convert to lowercase and trim whitespace
	normalized := strings.ToLower(strings.TrimSpace(charset))

	// Handle common aliases and variants
	switch normalized {
	case "iso-8859-15", "iso8859-15", "iso_8859-15", "latin-9", "latin9":
		return "iso-8859-15"
	case "iso-8859-1", "iso8859-1", "iso_8859-1", "latin-1", "latin1":
		return "iso-8859-1"
	case "iso-8859-2", "iso8859-2", "iso_8859-2", "latin-2", "latin2":
		return "iso-8859-2"
	case "windows-1252", "cp1252", "win1252":
		return "windows-1252"
	case "windows-1251", "cp1251", "win1251":
		return "windows-1251"
	case "windows-1250", "cp1250", "win1250":
		return "windows-1250"
	case "utf-8", "utf8":
		return "utf-8"
	case "us-ascii", "ascii":
		return "windows-1252" // Use windows-1252 as superset of ASCII
	default:
		return normalized
	}
}

// headerWordDecoder decodes RFC 2047 encoded words. Charsets other than the three
// the standard library knows (utf-8, iso-8859-1, us-ascii) go through htmlindex.
// A mime.WordDecoder holds no state, so the shared instance is safe for concurrent use.
var headerWordDecoder = &mime.WordDecoder{CharsetReader: charsetReader}

// fromAddressParser parses From headers with the extended-charset word decoder.
var fromAddressParser = &mail.AddressParser{WordDecoder: headerWordDecoder}

// charsetReader converts input from the named charset to UTF-8 (mime.WordDecoder.CharsetReader).
func charsetReader(charset string, input io.Reader) (io.Reader, error) {
	cs := normalizeCharsetName(charset)
	if cs == "utf-8" {
		return input, nil
	}
	enc, err := htmlindex.Get(cs)
	if err != nil {
		return nil, fmt.Errorf("unsupported charset: %s", charset)
	}
	if enc == nil {
		return input, nil
	}
	return transform.NewReader(input, enc.NewDecoder()), nil
}

// decodeHeaderText decodes a header value for display (A-web-8): RFC 2047 encoded
// words, HTML entities (as ConvertToUTF8 always did for headers) and invalid UTF-8
// repaired per byte sequence. Unlike ConvertToUTF8 it never runs a quoted-printable
// decoder over the value and never re-decodes the whole string as Latin-1.
func decodeHeaderText(text string) string {
	if text == "" {
		return ""
	}
	decoded := text
	if strings.Contains(text, "=?") {
		var err error
		decoded, err = headerWordDecoder.DecodeHeader(text)
		if err != nil {
			decoded = decodeUnsupportedMIME(text)
		}
	}
	return repairUTF8(html.UnescapeString(decoded))
}

// repairUTF8 returns s unchanged when it is valid UTF-8. Otherwise every byte that
// does not start a valid UTF-8 sequence is read as ISO-8859-1, so the valid UTF-8
// parts of a mixed string are kept as they are (A-web-8).
func repairUTF8(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 8)
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			b.WriteRune(rune(s[i]))
		} else {
			b.WriteString(s[i : i+size])
		}
		i += size
	}
	return b.String()
}

// rawHeaderValue returns the first value of the named header in a raw header block
// (the merged header lines stored in Article.HeadersJSON, which is not JSON), with
// folded continuation lines joined. It returns "" when the header is missing.
func rawHeaderValue(block, name string) string {
	var value strings.Builder
	found := false
	for _, line := range strings.Split(block, "\n") {
		line = strings.TrimRight(line, "\r")
		if found {
			if line != "" && (line[0] == ' ' || line[0] == '\t') {
				value.WriteByte(' ')
				value.WriteString(strings.TrimSpace(line))
				continue
			}
			break
		}
		if line == "" {
			break // end of the header section
		}
		colon := strings.IndexByte(line, ':')
		if colon <= 0 {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(line[:colon]), name) {
			found = true
			value.WriteString(strings.TrimSpace(line[colon+1:]))
		}
	}
	return value.String()
}

// decodeBodyText decodes a stored (raw) article body for display (A-web-8). headers
// is the raw header block of the article (Article.HeadersJSON). The body is
// quoted-printable or base64 decoded only when its Content-Transfer-Encoding says
// so (never for multipart bodies) and converted from the Content-Type charset.
// RFC 2047 encoded words and HTML entities in a body are text and stay as they are.
func decodeBodyText(body, headers string) string {
	if body == "" {
		return ""
	}
	var mediaType, charset string
	if ct := rawHeaderValue(headers, "Content-Type"); ct != "" {
		mt, params, err := mime.ParseMediaType(ct)
		mediaType = mt
		if err == nil {
			charset = params["charset"]
		}
	}
	data := []byte(body)
	// Only text bodies are decoded: a single-part image or application body stays
	// as its encoded text instead of turning into binary junk.
	if mediaType == "" || strings.HasPrefix(mediaType, "text/") {
		switch strings.ToLower(rawHeaderValue(headers, "Content-Transfer-Encoding")) {
		case "quoted-printable":
			data = decodeQPBody(body)
		case "base64":
			compact := strings.Map(func(r rune) rune {
				if unicode.IsSpace(r) {
					return -1
				}
				return r
			}, body)
			if decoded, err := base64.StdEncoding.DecodeString(compact); err == nil {
				data = decoded
			}
		}
	}
	return bodyBytesToUTF8(data, charset)
}

// DecodeBodyText decodes a stored (raw) article body for display: the
// Content-Transfer-Encoding and charset of headers (the raw header block,
// Article.HeadersJSON) are applied, RFC 2047 encoded words and HTML entities in
// the body stay text. The result is not HTML-escaped. Use it instead of
// ConvertToUTF8 for article bodies (preview API, reply quoting).
func DecodeBodyText(body, headers string) string {
	return decodeBodyText(body, headers)
}

// DisplayFrom returns the author name shown for a raw From header: the display
// name (or the local part of the address), never an address domain. It is the
// text Overview/Article PrintSanitized("from") shows, before HTML escaping.
func DisplayFrom(raw string) string {
	return displayFrom(raw)
}

// decodeQPBody decodes a quoted-printable body. When the whole body does not
// decode (a malformed sequence or a control byte somewhere), it decodes line by
// line and keeps only the lines that fail as they are, so one bad line does not
// leave the whole body encoded. Each line is decoded with its line end, so a
// soft line break ("=" at the end of a line) still joins it with the next one.
func decodeQPBody(body string) []byte {
	if decoded, err := io.ReadAll(quotedprintable.NewReader(strings.NewReader(body))); err == nil {
		return decoded
	}
	out := make([]byte, 0, len(body))
	for _, line := range strings.SplitAfter(body, "\n") {
		if line == "" {
			continue
		}
		decoded, err := io.ReadAll(quotedprintable.NewReader(strings.NewReader(line)))
		if err != nil {
			out = append(out, line...)
			continue
		}
		out = append(out, decoded...)
	}
	return out
}

// bodyBytesToUTF8 converts body bytes from the declared charset to UTF-8. Without a
// charset, or for UTF-8 and US-ASCII, invalid sequences are repaired per sequence.
// Bytes that are valid UTF-8 and not plain ASCII stay UTF-8 even when a legacy
// charset is declared: mislabelled UTF-8 posts are common on Usenet, while legacy
// 8-bit text is almost never valid UTF-8.
func bodyBytesToUTF8(data []byte, charset string) string {
	cs := strings.ToLower(strings.TrimSpace(charset))
	switch cs {
	case "", "utf-8", "utf8", "us-ascii", "ascii":
		return repairUTF8(string(data))
	}
	// UTF-16/UTF-32 labels on a Usenet body are bogus (the body is line-based 8-bit
	// text); decoding ASCII as UTF-16 yields CJK garbage. Treat them as unknown.
	for _, wide := range []string{"utf-16", "utf16", "utf-32", "utf32"} {
		if strings.HasPrefix(cs, wide) {
			return repairUTF8(string(data))
		}
	}
	if utf8.Valid(data) && !isASCII(data) {
		return string(data)
	}
	if s, err := decodeCharsetToUTF8(data, cs); err == nil {
		return s
	}
	return repairUTF8(string(data))
}

func isASCII(data []byte) bool {
	for _, c := range data {
		if c >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

// displayDateFormat is the date format of the "date" field, in PrintSanitized and
// in the batch prefill alike (A-web-10).
const displayDateFormat = "Mon, 02 Jan 2006 15:04"

// Article PrintSanitized returns UTF-8 decoded and HTML-escaped text safe for web display.
// The text is not filtered: html.EscapeString (and html/template) is the XSS defence (A-web-9).
// groupName is optional: the sanitized cache is keyed by group and message-id (A-web-10),
// so without a group the value is computed but not cached. BatchSanitizeArticles with the
// same group prefills the cache entries read here.
func (a *Article) PrintSanitized(field string, groupName ...string) template.HTML {
	if a == nil {
		log.Printf("PrintSanitized called on nil Article")
		return ""
	}
	canon, ok := cache.CanonicalField(field)
	if !ok {
		return "--sanitizing-F1--missing-field--"
	}

	// Read-only: the *Article may be shared (article cache), so the display
	// values are computed into locals and never written back (A-web-4).
	a.Mux.RLock()
	defer a.Mux.RUnlock()

	group := firstGroupName(groupName)
	if cached, found := GetCachedSanitized(group, a.MessageID, canon); found {
		return cached
	}

	text, ok := a.displayText(canon)
	if !ok {
		return "--sanitizing-F1--missing-field--"
	}
	result := template.HTML(html.EscapeString(text))
	SetCachedSanitized(group, a.MessageID, canon, result)
	return result
}

// displayText returns the decoded, not yet escaped display text of a canonical
// field name (cache.CanonicalField). Caller holds a.Mux (read lock is enough).
func (a *Article) displayText(field string) (string, bool) {
	switch field {
	case "subject":
		return a.sanitizedSubject(), true
	case "from":
		return a.sanitizedFromHeader(), true
	case "date":
		if !a.DateSent.IsZero() {
			return a.DateSent.Format(displayDateFormat), true
		}
		return "-???-", true
	case "date_since":
		if !a.DateSent.IsZero() {
			return PrintTimeSinceHumanReadable(a.DateSent), true
		}
		return "-???-", true
	case "references":
		return a.sanitizedReferences(), true
	case "body":
		return a.sanitizedBodyText(), true
	case "messageid":
		return decodeHeaderText(a.MessageID), true
	case "path":
		return decodeHeaderText(a.Path), true
	case "headers_json":
		// The raw header block (not JSON): header decoding only.
		return decodeHeaderText(a.HeadersJSON), true
	}
	return "", false
}

func PrintTimeSinceHumanReadable(t time.Time) string {
	// This function returns a human-readable time difference from now
	if t.IsZero() {
		return "never"
	}

	diff := time.Since(t)

	// Handle future timestamps (negative duration)
	if diff < 0 {
		futureDiff := -diff
		if futureDiff < time.Minute {
			return fmt.Sprintf("in %d seconds", int(futureDiff.Seconds()))
		} else if futureDiff < time.Hour {
			return fmt.Sprintf("in %d minutes", int(futureDiff.Minutes()))
		} else if futureDiff < 24*time.Hour {
			return fmt.Sprintf("in %d hours", int(futureDiff.Hours()))
		} else {
			futureDays := int(futureDiff.Hours() / 24)
			if futureDays == 1 {
				return "tomorrow"
			} else if futureDays < 7 {
				return fmt.Sprintf("in %d days", futureDays)
			} else {
				return fmt.Sprintf("in %d days", futureDays)
			}
		}
	}

	totalDays := int(diff.Hours() / 24)

	if diff < time.Minute {
		return fmt.Sprintf("%d seconds ago", int(diff.Seconds()))
	} else if diff < time.Hour {
		return fmt.Sprintf("%d minutes ago", int(diff.Minutes()))
	} else if diff < 24*time.Hour {
		return fmt.Sprintf("%d hours ago", int(diff.Hours()))
	} else if totalDays < 30 {
		return fmt.Sprintf("%d days ago", totalDays)
	} else if totalDays < 365 {
		months := totalDays / 30
		remainingDays := totalDays % 30
		if remainingDays > 0 {
			if months == 1 {
				return fmt.Sprintf("1 Month %d Days ago", remainingDays)
			}
			return fmt.Sprintf("%d Months %d Days ago", months, remainingDays)
		} else {
			if months == 1 {
				return "1 Month ago"
			}
			return fmt.Sprintf("%d Months ago", months)
		}
	} else {
		years := totalDays / 365
		remainingDays := totalDays % 365
		months := remainingDays / 30
		if months > 0 {
			if years == 1 && months == 1 {
				return "1 Year 1 Month ago"
			} else if years == 1 {
				return fmt.Sprintf("1 Year %d Months ago", months)
			} else if months == 1 {
				return fmt.Sprintf("%d Years 1 Month ago", years)
			} else {
				return fmt.Sprintf("%d Years %d Months ago", years, months)
			}
		} else {
			if years == 1 {
				return "1 Year ago"
			}
			return fmt.Sprintf("%d Years ago", years)
		}
	}

}

func SplitStringForLastSpace(s string) (string, string) {
	// This function splits a string into two parts at the last space
	if s == "" {
		return "", ""
	}
	lastSpaceIndex := strings.LastIndex(s, " ")
	if lastSpaceIndex == -1 {
		return s, "" // no space found, return original string and empty suffix
	}
	localPart := s[:lastSpaceIndex]
	suffix := s[lastSpaceIndex+1:]
	if localPart == "" {
		return "anon", suffix // return "anon" if local part is empty
	}
	if suffix == "" {
		return localPart, "..." // return local part and "..." if suffix is empty
	}
	return localPart, suffix
}

// cleanFromHeaderName removes non-alphabetic characters and control characters from name fields
func cleanFromHeaderName(name string) string {
	if name == "" {
		return "Anon"
	}

	// Remove control characters and common problematic characters
	var cleaned strings.Builder
	for _, r := range name {
		// Keep letters, numbers, spaces, hyphens, periods
		if unicode.IsLetter(r) || unicode.IsNumber(r) || r == ' ' || r == '-' || r == '.' {
			cleaned.WriteRune(r)
		} else if r == '(' || r == ')' || r == '[' || r == ']' {
			// Replace brackets with spaces to avoid truncated names like "User name ("
			cleaned.WriteRune(' ')
		}
		// Skip all other characters (control chars, weird symbols, etc.)
	}

	result := strings.TrimSpace(cleaned.String())

	// Remove multiple consecutive spaces
	result = spaceRegex.ReplaceAllString(result, " ")

	// If result is empty after cleaning, return "Anon"
	if result == "" {
		return "Anon"
	}

	// Limit to 32 characters (runes) for readability
	return truncateRunes(result, maxFromRunes)
}

const (
	maxSubjectRunes = 128 // display length of a subject, in runes
	maxFromRunes    = 32  // display length of an author name, in runes
)

// truncateRunes cuts s to at most n runes and appends "..." when it was longer.
// It never splits a UTF-8 sequence (A-web-6, A-web-7).
func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	count := 0
	for pos := range s {
		if count == n {
			return s[:pos] + "..."
		}
		count++
	}
	return s
}

// truncateSubject truncates a decoded subject to 128 runes with ellipsis.
func truncateSubject(subject string) string {
	return truncateRunes(subject, maxSubjectRunes)
}

// displaySubject decodes a raw Subject header first and truncates it afterwards (A-web-7).
func displaySubject(raw string) string {
	return truncateSubject(decodeHeaderText(raw))
}

// displayFrom returns the author name shown for a raw From header (A-web-6).
// Encoded words are decoded before anything is cut, the header is parsed with
// net/mail (display name, or the comment of the "addr (Name)" form, else the local
// part of the address), and the result is cut by runes. Address domains are never
// shown. A header net/mail cannot parse falls back to cleanFromHeaderName.
func displayFrom(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "Anon"
	}
	addr, err := fromAddressParser.Parse(raw)
	if err != nil {
		addr, err = fromAddressParser.Parse(decodeHeaderText(raw))
	}
	if err == nil {
		name := tidyFromName(decodeHeaderText(addr.Name))
		if name == "" {
			name = tidyFromName(localPart(addr.Address))
		}
		if name == "" {
			return "Anon"
		}
		return truncateRunes(name, maxFromRunes)
	}
	// Not an RFC 5322 address (e.g. "Last, First <addr>" or a bare name):
	// drop the <addr> part, hide domains, keep only the safe characters.
	decoded := decodeHeaderText(raw)
	rest := angleAddrRegex.ReplaceAllString(decoded, " ")
	if strings.TrimSpace(rest) == "" {
		rest = localPart(strings.Trim(angleAddrRegex.FindString(decoded), "<>"))
	}
	return cleanFromHeaderName(hideMailDomains(rest))
}

// localPart returns the part of an address before the "@".
func localPart(address string) string {
	local, _, _ := strings.Cut(address, "@")
	return strings.TrimSpace(local)
}

// hideMailDomains replaces every whitespace-separated word that contains "@" by
// the text before the "@", so no address domain is shown.
func hideMailDomains(s string) string {
	if !strings.Contains(s, "@") {
		return s
	}
	fields := strings.Fields(s)
	out := fields[:0]
	for _, f := range fields {
		if at := strings.IndexByte(f, '@'); at >= 0 {
			f = strings.TrimLeft(f[:at], "<(\"'")
			if f == "" {
				continue
			}
		}
		out = append(out, f)
	}
	return strings.Join(out, " ")
}

// tidyFromName hides address domains, turns control characters into spaces and
// collapses whitespace.
func tidyFromName(name string) string {
	name = hideMailDomains(name)
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, name)
	return strings.Join(strings.Fields(name), " ")
}

// Per-field display helpers: the single entry points for the decoded, not yet
// escaped display text of an Article field. They only read the Article; the
// caller holds a.Mux (read lock is enough).
func (a *Article) sanitizedSubject() string { return displaySubject(a.Subject) }

func (a *Article) sanitizedFromHeader() string { return displayFrom(a.FromHeader) }

func (a *Article) sanitizedBodyText() string { return decodeBodyText(a.BodyText, a.HeadersJSON) }

func (a *Article) sanitizedReferences() string { return decodeHeaderText(a.References) }

// Overview PrintSanitized returns a decoded and HTML-escaped version of the specified
// field for web display. It only reads the Overview, which may be shared through the
// tree cache (A-web-9). groupName is optional: without it the value is not cached.
func (o *Overview) PrintSanitized(field string, groupName ...string) template.HTML {
	if o == nil {
		return ""
	}
	canon, ok := cache.CanonicalField(field)
	if !ok {
		return "--sanitizing-F2--missing-field--"
	}

	group := firstGroupName(groupName)
	if cached, found := GetCachedSanitized(group, o.MessageID, canon); found {
		return cached
	}

	text, ok := o.displayText(canon)
	if !ok {
		return "--sanitizing-F2--missing-field--"
	}
	result := template.HTML(html.EscapeString(text))
	SetCachedSanitized(group, o.MessageID, canon, result)
	return result
}

// displayText returns the decoded, not yet escaped display text of a canonical
// field name (cache.CanonicalField).
func (o *Overview) displayText(field string) (string, bool) {
	switch field {
	case "subject":
		return displaySubject(o.Subject), true
	case "from":
		return displayFrom(o.FromHeader), true
	case "date":
		if !o.DateSent.IsZero() {
			return o.DateSent.Format(displayDateFormat), true
		}
		return "--", true
	case "date_since":
		if !o.DateSent.IsZero() {
			return PrintTimeSinceHumanReadable(o.DateSent), true
		}
		return "--", true
	case "messageid":
		return decodeHeaderText(o.MessageID), true
	case "references":
		return decodeHeaderText(o.References), true
	}
	return "", false
}

// GetCleanSubject returns the article subject text decoded but without HTML escaping
// This is suitable for use in browser titles where HTML entities should not be displayed literally
func (a *Article) GetCleanSubject() string {
	if a == nil {
		return "[No Subject]"
	}

	if a.Subject == "" {
		return "[No Subject]"
	}

	return displaySubject(a.Subject)
}

// GetCleanSubject returns the overview subject text decoded but without HTML escaping
// This is suitable for use in browser titles where HTML entities should not be displayed literally
func (o *Overview) GetCleanSubject() string {
	if o == nil {
		return "[No Subject]"
	}

	if o.Subject == "" {
		return "[No Subject]"
	}

	return displaySubject(o.Subject)
}

// Fields prefilled by the batch sanitizers, as canonical names (cache.CanonicalField).
var (
	batchArticleFields  = []string{"subject", "from", "body", "references", "date", "messageid", "path"}
	batchOverviewFields = []string{"subject", "from", "references", "date", "messageid"}
)

// BatchSanitizeArticles pre-sanitizes and caches all commonly-used fields for multiple articles
// This should be called before rendering templates to avoid lock contention during template execution
// groupName must be the group the templates pass to PrintSanitized: the cache is keyed by group and
// message-id (A-web-10). Without a group nothing is cached, since PrintSanitized could not find it.
func BatchSanitizeArticles(articles []*Article, groupName ...string) {
	group := firstGroupName(groupName)
	if len(articles) == 0 || sanitizedCache == nil || group == "" {
		return
	}

	// Collect all sanitized fields for all articles
	batch := make(map[string]map[string]template.HTML)

	for _, article := range articles {
		if article == nil {
			continue
		}
		// Read-only: compute display values locally, never write them back
		// into the (possibly shared) *Article (A-web-4).
		article.Mux.RLock()
		key := sanitizedCacheKey(group, article.MessageID)
		if key == "" {
			article.Mux.RUnlock()
			continue
		}
		// Skip if already cached
		if cached, found := sanitizedCache.GetArticle(key); found && cached != nil {
			article.Mux.RUnlock()
			continue
		}
		fields := make(map[string]template.HTML, len(batchArticleFields))
		for _, f := range batchArticleFields {
			if text, ok := article.displayText(f); ok {
				fields[f] = template.HTML(html.EscapeString(text))
			}
		}
		article.Mux.RUnlock()
		batch[key] = fields
	}

	// Store all sanitized articles in one batch operation
	if len(batch) > 0 {
		sanitizedCache.BatchSetArticles(batch)
	}
}

// BatchSanitizeOverviews pre-sanitizes and caches all commonly-used fields for multiple overviews
// This should be called before rendering templates to avoid lock contention during template execution
// It only reads the overviews (they may be shared through the tree cache). groupName as for
// BatchSanitizeArticles: without a group nothing is cached.
func BatchSanitizeOverviews(overviews []*Overview, groupName ...string) {
	group := firstGroupName(groupName)
	if len(overviews) == 0 || sanitizedCache == nil || group == "" {
		return
	}

	// Collect all sanitized fields for all overviews
	batch := make(map[string]map[string]template.HTML)

	for _, overview := range overviews {
		if overview == nil {
			continue
		}
		key := sanitizedCacheKey(group, overview.MessageID)
		if key == "" {
			continue
		}
		// Skip if already cached
		if cached, found := sanitizedCache.GetArticle(key); found && cached != nil {
			continue
		}
		fields := make(map[string]template.HTML, len(batchOverviewFields))
		for _, f := range batchOverviewFields {
			if text, ok := overview.displayText(f); ok {
				fields[f] = template.HTML(html.EscapeString(text))
			}
		}
		batch[key] = fields
	}

	// Store all sanitized overviews in one batch operation
	if len(batch) > 0 {
		sanitizedCache.BatchSetArticles(batch)
	}
}
