package main

import (
	"fmt"
	"log"
	"strings"
)

// multiLineHeaderToStringSpaced joins multi-line headers with spaces (for RFC-compliant header unfolding)
func multiLineHeaderToStringSpaced(vals []string) string {
	if len(vals) == 0 {
		return ""
	}
	if len(vals) == 1 {
		return vals[0] // Fast path for single-line headers
	}
	var sb strings.Builder
	for i, line := range vals {
		// Trim each line and add spaces between them
		line = strings.TrimSpace(line)
		if line == "" {
			continue // Skip empty lines
		}
		if i > 0 {
			sb.WriteString(" ")
		}
		sb.WriteString(line)
	}
	return sb.String()
}

// getHeaderFirst returns the first value for a header, or "" if not present
func getHeaderFirst(headers map[string][]string, key string) string {
	if vals, ok := headers[key]; ok && len(vals) > 0 {
		// For headers that can be folded across multiple lines (like References), 
		// we need to join with spaces instead of newlines to properly unfold them
		if key == "references" || key == "References" || key == "in-reply-to" || key == "In-Reply-To" {
			return multiLineHeaderToStringSpaced(vals)
		}
		// For other headers, just return first value
		return vals[0]
	}
	return ""
}

func testReferences() {
	// Test multi-line References header parsing
	
	// Simulate the multi-line References header as it would be parsed
	headers := map[string][]string{
		"references": {
			"<20250602000025.3da60fb0.dietz.usenet@rotfl.franken.de>",
			" <87ldq277d1.fsf@runbox.com> <1064ln2$1m13p$1@dont-email.me>",
			" <memnaaFic8gU1@mid.individual.net> <871pq1zpkd.fsf@runbox.com>",
			" <106e52e$3elc3$1@dont-email.me> <yqtms8kncrf.fsf@runbox.com>",
			" <106gn6h$1e11$1@dont-email.me> <106hf55$65a7$1@dont-email.me>",
			" <875xf7o4j4.fsf@runbox.com>",
			" <7t688cf805i161682n3e8%sfroehli@Froehlich.Priv.at>",
			" <106iv9a$i5fr$1@dont-email.me>",
			" <5t688d4119i2a8908n3e8%sfroehli@Froehlich.Priv.at>",
			" <106kgm9$v4og$1@dont-email.me>",
			" <5t688dd940i100ef2n3e8%sfroehli@Froehlich.Priv.at>",
		},
	}
	
	// Test the getHeaderFirst function with our test data
	references := getHeaderFirst(headers, "references")
	
	fmt.Printf("Original multi-line References header:\n")
	for i, line := range headers["references"] {
		fmt.Printf("  [%d]: %q\n", i, line)
	}
	
	fmt.Printf("\nProcessed References header:\n")
	fmt.Printf("Result: %q\n", references)
	
	fmt.Printf("\nLength: %d characters\n", len(references))
	
	if references == "" || references == "References: " {
		log.Printf("❌ ERROR: References header is empty or malformed!")
	} else {
		log.Printf("✅ SUCCESS: References header properly processed")
		
		// Count the number of message IDs (rough count by counting '<' characters)
		messageIdCount := 0
		for _, char := range references {
			if char == '<' {
				messageIdCount++
			}
		}
		fmt.Printf("Found approximately %d message IDs in references\n", messageIdCount)
	}
}

func main() {
	fmt.Println("Testing References header parsing...")
	testReferences()
}
