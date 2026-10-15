package models

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/cache"
)

const (
	adfSanitizeFrom    = "Alice Example <alice@test.invalid>"
	adfSanitizeBody    = "body <script>x</script> end"
	adfSanitizeSubject = "subj <iframe src=x></iframe> tail"
	adfSanitizeRefs    = "<adf-web-cache-ref-1@test.invalid>"
)

func adfSanitizeArticle(seq string) *Article {
	return &Article{
		MessageID:  "<adf-web-cache-" + seq + "@test.invalid>",
		Subject:    adfSanitizeSubject,
		FromHeader: adfSanitizeFrom,
		BodyText:   adfSanitizeBody,
		References: adfSanitizeRefs,
		DateString: "Thu, 15 Oct 2026 06:00:00 +0000",
		DateSent:   time.Date(2026, 10, 15, 6, 0, 0, 0, time.UTC),
	}
}

func adfSanitizeCheckUnmodified(t *testing.T, a *Article) {
	t.Helper()
	a.Mux.RLock()
	defer a.Mux.RUnlock()
	if a.FromHeader != adfSanitizeFrom {
		t.Errorf("FromHeader modified: %q", a.FromHeader)
	}
	if a.BodyText != adfSanitizeBody {
		t.Errorf("BodyText modified: %q", a.BodyText)
	}
	if a.Subject != adfSanitizeSubject {
		t.Errorf("Subject modified: %q", a.Subject)
	}
	if a.References != adfSanitizeRefs {
		t.Errorf("References modified: %q", a.References)
	}
	if a.Sanitized {
		t.Errorf("Sanitized flag set on the article")
	}
}

// TestAdfSanitizeNoWriteBack: the display path never writes sanitized values into the Article (A-web-4).
func TestAdfSanitizeNoWriteBack(t *testing.T) {
	a := adfSanitizeArticle("nowb-1")

	from := string(a.PrintSanitized("fromheader"))
	if from != "Alice Example" {
		t.Errorf("PrintSanitized(fromheader)=%q, want %q", from, "Alice Example")
	}
	body := string(a.PrintSanitized("bodytext"))
	if strings.Contains(body, "script") || body != "body  end" {
		t.Errorf("PrintSanitized(bodytext)=%q, want %q", body, "body  end")
	}
	subj := string(a.PrintSanitized("subject"))
	if strings.Contains(subj, "iframe") || subj != "subj  tail" {
		t.Errorf("PrintSanitized(subject)=%q, want %q", subj, "subj  tail")
	}
	refs := string(a.PrintSanitized("references"))
	if refs != "&lt;adf-web-cache-ref-1@test.invalid&gt;" {
		t.Errorf("PrintSanitized(references)=%q", refs)
	}
	adfSanitizeCheckUnmodified(t, a)

	// Same values as the mutating StripDangerousHTML on a separate copy.
	ref := adfSanitizeArticle("nowb-ref")
	ref.StripDangerousHTML()
	a.Mux.RLock()
	sf := a.sanitizedFields()
	a.Mux.RUnlock()
	if sf.Subject != ref.Subject || sf.FromHeader != ref.FromHeader || sf.BodyText != ref.BodyText ||
		sf.DateString != ref.DateString || sf.References != ref.References {
		t.Errorf("sanitizedFields()=%+v differs from StripDangerousHTML result %q/%q/%q/%q/%q",
			sf, ref.Subject, ref.FromHeader, ref.BodyText, ref.DateString, ref.References)
	}

	// BatchSanitizeArticles with a live cache: values cached, article untouched.
	sc := cache.NewSanitizedCache(100, time.Hour)
	prev := sanitizedCache
	sanitizedCache = sc
	t.Cleanup(func() {
		sanitizedCache = prev
		sc.Stop()
	})

	b := adfSanitizeArticle("nowb-2")
	BatchSanitizeArticles([]*Article{a, b})
	adfSanitizeCheckUnmodified(t, a)
	adfSanitizeCheckUnmodified(t, b)

	for _, art := range []*Article{a, b} {
		if v, ok := sc.GetField(art.MessageID, "fromheader"); !ok || string(v) != "Alice Example" {
			t.Errorf("%s cached fromheader ok=%v v=%q", art.MessageID, ok, v)
		}
		if v, ok := sc.GetField(art.MessageID, "bodytext"); !ok || string(v) != "body  end" {
			t.Errorf("%s cached bodytext ok=%v v=%q", art.MessageID, ok, v)
		}
		if v, ok := sc.GetField(art.MessageID, "subject"); !ok || string(v) != "subj  tail" {
			t.Errorf("%s cached subject ok=%v v=%q", art.MessageID, ok, v)
		}
	}
}

// TestAdfSanitizeConcurrentPrint: PrintSanitized only reads the shared Article; run with -race.
func TestAdfSanitizeConcurrentPrint(t *testing.T) {
	a := adfSanitizeArticle("conc-1")
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				for _, f := range []string{"subject", "fromheader", "bodytext", "references", "date"} {
					if a.PrintSanitized(f) == "" {
						t.Errorf("PrintSanitized(%s) empty", f)
						return
					}
				}
			}
		}()
	}
	wg.Wait()
	adfSanitizeCheckUnmodified(t, a)
}
