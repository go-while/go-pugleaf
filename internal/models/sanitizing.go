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
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/htmlindex"
	"golang.org/x/text/transform"
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
	mimeWordRegex := regexp.MustCompile(`=\?([^?]+)\?([QqBb])\?([^?]*)\?=`)

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

// sanitizeText removes dangerous HTML/script content and event handlers from a string

// Article PrintSanitized returns UTF-8 converted and HTML-escaped text safe for web display
// This method now primarily relies on pre-cached values from BatchSanitizeArticles
// groupName parameter is optional for caching - if empty, caching is skipped
func (a *Article) PrintSanitized(field string, groupName ...string) template.HTML {
	if a == nil {
		log.Printf("PrintSanitized called on nil Article")
		return ""
	}

	// Try to get from cache first if messageID is available
	if a.MessageID != "" {
		if cached, found := GetCachedSanitized(a.MessageID, field); found {
			return cached
		}
	}

	if !a.Sanitized {
		a.StripDangerousHTML()
		a.Sanitized = true
	}

	var text string
	switch field {
	case "message_id", "messageid":
		text = a.MessageID
	case "subject":
		text = truncateSubject(a.Subject)
	case "from", "fromheader":
		text = a.FromHeader
	case "date", "date_string":
		if !a.DateSent.IsZero() {
			text = a.DateSent.Format("Mon, 02 Jan 2006 15:04")
		} else {
			text = "-???-"
		}
	case "date_since":
		if !a.DateSent.IsZero() {
			text = PrintTimeSinceHumanReadable(a.DateSent)
		} else {
			text = "-???-"
		}
	case "references":
		text = a.References
	case "body", "body_text", "bodytext":
		text = a.BodyText
	case "path":
		text = a.Path
	case "headers_json":
		text = a.HeadersJSON
	// HeadersJSON is intentionally excluded - it's JSON data, not user text
	default:
		return "--sanitizing-F1--missing-field--"
	}

	utf8Text := ConvertToUTF8(text)
	// Since ConvertToUTF8 already handles HTML entity decoding,
	// we need to re-escape for safe HTML output
	result := template.HTML(html.EscapeString(utf8Text))

	// Cache the result if messageID is available
	if a.MessageID != "" {
		SetCachedSanitized(a.MessageID, field, result)
	}

	return result
}

const replacedMailDomain = "..."

func splitDomainFromEmail(email string) (string, string) {
	// This function splits an email address into local part and domain
	if email == "" {
		return "", ""
	}
	parts := strings.SplitN(email, "@", 2)
	if len(parts) != 2 {
		return email, "" // return original if no domain found
	}
	localPart := parts[0]
	domain := parts[1]
	if len(domain) > 4 {
		domain = domain[:4] + "..." // truncate domain to 4 characters
	}
	if localPart == "" {
		return "anon", "..." // return domain only if local part is empty
	}
	if domain == "" {
		return localPart, domain
	}
	return localPart, domain
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
	spaceRegex := regexp.MustCompile(`\s+`)
	result = spaceRegex.ReplaceAllString(result, " ")

	// If result is empty after cleaning, return "Anon"
	if result == "" {
		return "Anon"
	}

	// Limit to 32 characters for readability
	if len(result) > 32 {
		result = result[:32] + "..."
	}

	return result
}

func filterMailDomainInFromHeader(fromHeader string) string {
	// This function replaces the domain in the From header with a generic domain
	if fromHeader == "" {
		return "Anon <nohdr@" + replacedMailDomain + ">"
	}
	name, email := SplitStringForLastSpace(strings.TrimSpace(fromHeader))
	if !strings.Contains(email, "@") {
		if len(fromHeader) > 16 {
			fromHeader = fromHeader[:16]
		}
		if name != "" {
			if len(name) > 16 {
				name = name[:16]
			}
			// Clean up non-alphabetic characters from name
			name = cleanFromHeaderName(name)
			return name
		}
		return cleanFromHeaderName(fromHeader) // invalid format, clean and return
	}
	if len(name) > 16 {
		name = name[:16]
	}
	/*
		localPart, domain := splitDomainFromEmail(strings.Trim(email, "<>"))
		if domain == replacedMailDomain {
			return fromHeader // already filtered
		}

		if len(localPart) > 16 {
			localPart = localPart[:16] + ".." // truncate local part to 2 characters
		}


		newFromHeader := name + " <" + localPart + "@" + replacedMailDomain + ">"
		if len(newFromHeader) > 16 {
			newFromHeader = newFromHeader[:16] + ".." // truncate to 16 characters
		}
	*/
	//log.Printf("Filtering From header: name='%s' hdr='%s' -> '%s'", name, fromHeader, newFromHeader)
	// Replace from domain with the generic domain and clean the name
	return cleanFromHeaderName(name)
	//return newFromHeader
}

// StripDangerousHTML removes potentially dangerous HTML/script content from all user-facing fields
func (a *Article) StripDangerousHTML() {
	if a == nil {
		return
	}

	a.Subject = sanitizeText(a.Subject)
	a.FromHeader = sanitizeText(filterMailDomainInFromHeader(a.FromHeader))
	a.BodyText = sanitizeText(a.BodyText)
	a.DateString = sanitizeText(a.DateString)
	a.References = sanitizeText(a.References)
	// Add more fields if needed

	a.Sanitized = true
}

// Overview PrintSanitized returns a sanitized version of the specified field for web display
// groupName parameter is optional for caching - if empty, caching is skipped
func (o *Overview) PrintSanitized(field string, groupName ...string) template.HTML {
	if o == nil {
		return ""
	}

	// Try to get from cache first if messageID is available
	if o.MessageID != "" {
		if cached, found := GetCachedSanitized(o.MessageID, field); found {
			return cached
		}
	}

	if !o.Sanitized {
		o.Subject = sanitizeText(o.Subject)
		o.FromHeader = sanitizeText(filterMailDomainInFromHeader(o.FromHeader))
		o.DateString = sanitizeText(o.DateString)
		o.References = sanitizeText(o.References)
		o.Sanitized = true
	}

	var text string
	switch strings.ToLower(field) {
	case "subject":
		text = truncateSubject(o.Subject)

	case "fromheader", "from", "from_header":
		text = o.FromHeader

	case "date", "datestring", "date_string":
		if !o.DateSent.IsZero() {
			text = o.DateSent.Format("Mon, 02 Jan 2006 15:04")
		} else {
			text = "--"
		}

	case "date_since":
		if !o.DateSent.IsZero() {
			text = PrintTimeSinceHumanReadable(o.DateSent)
		} else {
			text = "--"
		}

	case "messageid", "message_id":
		text = o.MessageID
	case "references":
		text = o.References
	default:
		return "--sanitizing-F2--missing-field--"
	}

	// Convert from Latin-1 to UTF-8 if needed
	text = ConvertToUTF8(text)

	// HTML escape for web safety
	result := template.HTML(html.EscapeString(text))

	// Cache the result if messageID is available
	if o.MessageID != "" {
		SetCachedSanitized(o.MessageID, field, result)
	}

	return result
}

// GetCleanSubject returns the article subject text decoded and cleaned but without HTML escaping
// This is suitable for use in browser titles where HTML entities should not be displayed literally
func (a *Article) GetCleanSubject() string {
	if a == nil {
		return "[No Subject]"
	}

	if a.Subject == "" {
		return "[No Subject]"
	}

	// Get the subject text, apply basic sanitization but without HTML escaping
	text := sanitizeText(truncateSubject(a.Subject))

	// Convert from Latin-1 to UTF-8 and decode entities
	text = ConvertToUTF8(text)

	return text
}

// GetCleanSubject returns the overview subject text decoded and cleaned but without HTML escaping
// This is suitable for use in browser titles where HTML entities should not be displayed literally
func (o *Overview) GetCleanSubject() string {
	if o == nil {
		return "[No Subject]"
	}

	if o.Subject == "" {
		return "[No Subject]"
	}

	// Get the subject text, apply basic sanitization but without HTML escaping
	text := sanitizeText(truncateSubject(o.Subject))

	// Convert from Latin-1 to UTF-8 and decode entities
	text = ConvertToUTF8(text)

	return text
}

func sanitizeText(input string) string {
	if input == "" {
		return ""
	}

	// Remove script tags and their content
	scriptRegex := regexp.MustCompile(`(?i)<script[^>]*>.*?</script>`)
	output := scriptRegex.ReplaceAllString(input, "")

	// Remove iframe tags
	iframeRegex := regexp.MustCompile(`(?i)<iframe[^>]*>.*?</iframe>`)
	output = iframeRegex.ReplaceAllString(output, "")

	// Remove javascript: links
	jsRegex := regexp.MustCompile(`(?i)javascript:[^"'\s>]*`)
	output = jsRegex.ReplaceAllString(output, "")

	// Remove onload, onclick, onerror, etc. event handlers
	eventRegex := regexp.MustCompile(`(?i)on\w+\s*=\s*["'][^"']*["']`)
	output = eventRegex.ReplaceAllString(output, "")

	return output
}

// truncateSubject truncates subject lines to 128 characters with ellipsis
func truncateSubject(subject string) string {
	if len(subject) <= 128 {
		return subject
	}
	return subject[:128] + "..."
}

// BatchSanitizeArticles pre-sanitizes and caches all commonly-used fields for multiple articles
// This should be called before rendering templates to avoid lock contention during template execution
func BatchSanitizeArticles(articles []*Article) {
	if len(articles) == 0 || sanitizedCache == nil {
		return
	}

	// Collect all sanitized fields for all articles
	batch := make(map[string]map[string]template.HTML)

	for _, article := range articles {
		if article == nil || article.MessageID == "" {
			continue
		}

		// Skip if already fully cached
		if cached, found := sanitizedCache.GetArticle(article.MessageID); found && cached != nil {
			continue
		}

		// Sanitize the article if not already done
		if !article.Sanitized {
			article.StripDangerousHTML()
			article.Sanitized = true
		}

		// Prepare sanitized fields for this article
		fields := make(map[string]template.HTML)

		// Sanitize commonly-used fields - use same order as old PrintSanitized
		utf8Subject := ConvertToUTF8(truncateSubject(article.Subject))
		fields["subject"] = template.HTML(html.EscapeString(utf8Subject))

		utf8FromHeader := ConvertToUTF8(article.FromHeader)
		fields["fromheader"] = template.HTML(html.EscapeString(utf8FromHeader))

		utf8BodyText := ConvertToUTF8(article.BodyText)
		fields["bodytext"] = template.HTML(html.EscapeString(utf8BodyText))

		utf8References := ConvertToUTF8(article.References)
		fields["references"] = template.HTML(html.EscapeString(utf8References))

		// Handle date formatting like PrintSanitized does
		var dateText string
		if !article.DateSent.IsZero() {
			dateText = article.DateSent.Format("2006-Jan-02 15:04:05")
		} else {
			dateText = "invalid-date"
		}
		utf8DateText := ConvertToUTF8(dateText)
		fields["datestring"] = template.HTML(html.EscapeString(utf8DateText))

		// Add other commonly-used fields
		utf8MessageID := ConvertToUTF8(article.MessageID)
		fields["messageid"] = template.HTML(html.EscapeString(utf8MessageID))

		utf8Path := ConvertToUTF8(article.Path)
		fields["path"] = template.HTML(html.EscapeString(utf8Path))

		batch[article.MessageID] = fields
	}

	// Store all sanitized articles in one batch operation
	if len(batch) > 0 {
		sanitizedCache.BatchSetArticles(batch)
	}
}

// BatchSanitizeOverviews pre-sanitizes and caches all commonly-used fields for multiple overviews
// This should be called before rendering templates to avoid lock contention during template execution
func BatchSanitizeOverviews(overviews []*Overview) {
	if len(overviews) == 0 || sanitizedCache == nil {
		return
	}

	// Collect all sanitized fields for all overviews
	batch := make(map[string]map[string]template.HTML)

	for _, overview := range overviews {
		if overview == nil || overview.MessageID == "" {
			continue
		}

		// Skip if already fully cached
		if cached, found := sanitizedCache.GetArticle(overview.MessageID); found && cached != nil {
			continue
		}

		// Sanitize the overview if not already done
		if !overview.Sanitized {
			overview.Subject = sanitizeText(overview.Subject)
			overview.FromHeader = sanitizeText(filterMailDomainInFromHeader(overview.FromHeader))
			overview.DateString = sanitizeText(overview.DateString)
			overview.References = sanitizeText(overview.References)
			overview.Sanitized = true
		}

		// Prepare sanitized fields for this overview
		fields := make(map[string]template.HTML)

		// Sanitize commonly-used fields - use same order as old PrintSanitized
		utf8Subject := ConvertToUTF8(truncateSubject(overview.Subject))
		fields["subject"] = template.HTML(html.EscapeString(utf8Subject))

		utf8FromHeader := ConvertToUTF8(overview.FromHeader)
		fields["fromheader"] = template.HTML(html.EscapeString(utf8FromHeader))

		utf8References := ConvertToUTF8(overview.References)
		fields["references"] = template.HTML(html.EscapeString(utf8References))

		utf8MessageID := ConvertToUTF8(overview.MessageID)
		fields["messageid"] = template.HTML(html.EscapeString(utf8MessageID))

		// Handle date formatting like PrintSanitized does
		var dateText string
		if !overview.DateSent.IsZero() {
			dateText = overview.DateSent.Format("2006-Jan-02 15:04:05")
		} else {
			dateText = "invalid-date"
		}
		utf8DateText := ConvertToUTF8(dateText)
		fields["datestring"] = template.HTML(html.EscapeString(utf8DateText))

		batch[overview.MessageID] = fields
	}

	// Store all sanitized overviews in one batch operation
	if len(batch) > 0 {
		sanitizedCache.BatchSetArticles(batch)
	}
}
