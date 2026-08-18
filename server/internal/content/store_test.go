package content

import (
	"testing"

	"github.com/seorilabs/platform/server/internal/platformerr"
)

func TestReadingLimitExemptsSameReadingKey(t *testing.T) {
	doc := &usageDoc{ReadingKeys: map[string]bool{}}
	if err := allowReadingKey(doc, 1, "rk_same"); err != nil {
		t.Fatal(err)
	}
	if err := allowReadingKey(doc, 1, "rk_same"); err != nil {
		t.Fatalf("같은 readingKey 재조회가 한도에 걸렸다: %v", err)
	}
	if err := allowReadingKey(doc, 1, "rk_other"); platformerr.CodeOf(err) != platformerr.CodeRateLimited {
		t.Fatalf("새 readingKey code=%q err=%v", platformerr.CodeOf(err), err)
	}
}

func TestTermLimitCountsEveryLookup(t *testing.T) {
	doc := &usageDoc{}
	if err := allowTermLookup(doc, 1); err != nil {
		t.Fatal(err)
	}
	if err := allowTermLookup(doc, 1); platformerr.CodeOf(err) != platformerr.CodeRateLimited {
		t.Fatalf("code=%q err=%v", platformerr.CodeOf(err), err)
	}
}
