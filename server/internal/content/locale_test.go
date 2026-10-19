package content

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/seorilabs/platform/server/internal/platformerr"
)

func TestLocaleHTTPRejectsExplicitInvalidValues(t *testing.T) {
	mux := testHandler(t, &fakeAppChecks{})
	for _, query := range []string{"locale=", "locale=en&locale=ko", "locale=fr"} {
		r := httptest.NewRequest("GET", "/v1/content/version?"+query, nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != 400 {
			t.Fatalf("query %q status=%d", query, w.Code)
		}
	}
	for _, value := range []string{`""`, `null`, `"fr"`} {
		base, err := json.Marshal(validResolveRequest())
		if err != nil {
			t.Fatal(err)
		}
		var body map[string]json.RawMessage
		if err := json.Unmarshal(base, &body); err != nil {
			t.Fatal(err)
		}
		body["locale"] = json.RawMessage(value)
		payload, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest("POST", "/v1/content/readings:resolve", strings.NewReader(string(payload)))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != 400 {
			t.Fatalf("locale %s status=%d", value, w.Code)
		}
	}
}

func TestLocaleServiceReadingPairingAndTerm(t *testing.T) {
	reading, pairing := validResolveRequest(), validPairingRequest()
	items := serviceRelease(t, reading).Items
	for id, item := range pairingRelease(t, pairing).Items {
		items[id] = item
	}
	items["hidden.private"] = Item{ID: "hidden.private", Text: "Internal", Access: AccessFree, Contexts: []Context{ContextInternal}}
	objects := memoryObjects{}
	for _, locale := range []string{"ko", "en"} {
		list := make([]Item, 0, len(items))
		for _, item := range items {
			item.Text = locale + " " + item.ID
			item.More = locale + " full manuscript " + item.ID
			if item.Access == AccessFree && item.ID != "hidden.private" {
				item.Contexts = []Context{ContextReading, ContextTerm}
			}
			list = append(list, item)
		}
		putLocaleItems(t, objects, locale, locale, list)
	}
	loader, _ := NewReleaseLoader(objects, "production")
	app := pairingApp()
	app.Content.DeepAlwaysOpen = true
	service, err := NewService(fakeApps{app}, loader, serviceUsage{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var readingKey, pairKey string
	for _, locale := range []string{"ko", "en", "ko"} {
		reading.Locale, pairing.Locale = RequestLocale(locale), RequestLocale(locale)
		got, err := service.Resolve(t.Context(), "ungeul", "puid", reading)
		if err != nil || got.Locale != locale || len(got.Articles) == 0 {
			t.Fatalf("reading: %#v %v", got, err)
		}
		if readingKey != "" && got.ReadingKey != readingKey {
			t.Fatal("locale changed reading key")
		}
		readingKey = got.ReadingKey
		for _, article := range got.Articles {
			if article.Text != locale+" "+article.ID || article.More != locale+" full manuscript "+article.ID {
				t.Fatalf("mixed locale: %#v", article)
			}
		}
		pair, err := service.ResolvePairing(t.Context(), "ungeul", "puid", pairing)
		if err != nil || pair.Locale != locale || len(pair.Articles) == 0 {
			t.Fatalf("pairing: %#v %v", pair, err)
		}
		if pairKey != "" && pair.PairKey != pairKey {
			t.Fatal("locale changed pair key")
		}
		pairKey = pair.PairKey
		term, err := service.Term(t.Context(), "ungeul", "puid", "ilju.mujin", locale)
		if err != nil || term.Locale != locale || term.Article.Text != locale+" ilju.mujin" {
			t.Fatalf("term: %#v %v", term, err)
		}
		if _, err := service.Term(t.Context(), "ungeul", "puid", "hidden.private", locale); platformerr.CodeOf(err) != platformerr.CodeContentTermNotFound {
			t.Fatalf("internal exposed: %v", err)
		}
	}
}

func putEnglishRelease(t *testing.T, objects memoryObjects, text, marker string) string {
	t.Helper()
	return putLocaleItems(t, objects, "en", marker, []Item{{
		ID: "ilju.gapja", Text: text, Access: AccessFree, Contexts: []Context{ContextReading},
	}})
}

func putLocaleItems(t *testing.T, objects memoryObjects, locale, marker string, items []Item) string {
	t.Helper()
	file := map[string]any{"schemaVersion": 1, "locale": marker, "items": items}
	contentBytes, err := json.Marshal(file)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(contentBytes)
	digest := hex.EncodeToString(sum[:])
	version := "sha256-" + digest
	manifest, _ := json.Marshal(map[string]any{"schemaVersion": 1, "locale": marker,
		"contentVersion": version, "contentSha256": digest, "itemCount": len(items)})
	active, _ := json.Marshal(map[string]any{"schemaVersion": 1, "locale": marker, "contentVersion": version})
	base := "private-content/production/ungeul/"
	if locale != "ko" {
		base += "locales/" + locale + "/"
	}
	objects[base+"active.json"] = active
	objects[base+"releases/"+version+"/manifest.json"] = manifest
	objects[base+"releases/"+version+"/content.json"] = contentBytes
	return version
}

func TestLocaleReleaseIsolation(t *testing.T) {
	objects := memoryObjects{}
	ko := putRelease(t, objects, "한국어 원고")
	en := putEnglishRelease(t, objects, "English manuscript", "en")
	loader, err := NewReleaseLoader(objects, "production")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	loader.WithClock(func() time.Time { return now })
	for _, tc := range []struct{ locale, version string }{{"", ko}, {"en", en}, {"ko", ko}} {
		got, err := loader.Load(t.Context(), testContentApp(), tc.locale)
		wantLocale := tc.locale
		if wantLocale == "" {
			wantLocale = "ko"
		}
		if err != nil || got.ContentVersion != tc.version || got.Locale != wantLocale {
			t.Fatalf("locale %q: got=%#v err=%v", tc.locale, got, err)
		}
	}
	objects["private-content/production/ungeul/locales/en/active.json"] = []byte("broken")
	now = now.Add(10 * time.Minute)
	got, err := loader.Load(t.Context(), testContentApp(), "en")
	if err != nil || got.ContentVersion != en || got.Locale != "en" {
		t.Fatalf("English last good release=%#v err=%v", got, err)
	}
}

func TestLocaleColdFailureNeverFallsBackToKorean(t *testing.T) {
	for _, marker := range []string{"", "ko", "fr"} {
		t.Run(marker, func(t *testing.T) {
			objects := memoryObjects{}
			putRelease(t, objects, "한국어 원고")
			putEnglishRelease(t, objects, "Wrong locale binding", marker)
			loader, _ := NewReleaseLoader(objects, "production")
			if _, err := loader.Load(t.Context(), testContentApp(), "ko"); err != nil {
				t.Fatal(err)
			}
			if _, err := loader.Load(t.Context(), testContentApp(), "en"); platformerr.CodeOf(err) != platformerr.CodeContentUnavailable {
				t.Fatalf("wrong locale %q must be unavailable: %v", marker, err)
			}
		})
	}
}

func TestRejectUnsupportedLocale(t *testing.T) {
	loader, _ := NewReleaseLoader(memoryObjects{}, "production")
	for _, locale := range []string{"EN", "en-US", "../ko", "en/../../", "fr", " en"} {
		if _, err := loader.Load(t.Context(), testContentApp(), locale); platformerr.CodeOf(err) != platformerr.CodeRequestInvalid {
			t.Fatalf("locale %q: %v", locale, err)
		}
	}
}
