//go:build integration

package content

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/seorilabs/platform/server/internal/identity"
	"github.com/seorilabs/platform/server/internal/store"
)

// GCS와 이미 검증된 세션 경계만 fixture다. 해시 검증·선택·HTTP·사용량은 실제 구현이다.
func TestServiceRuntimeContentReleaseReadingPairingAndRestart(t *testing.T) {
	host, _, err := net.SplitHostPort(os.Getenv("FIRESTORE_EMULATOR_HOST"))
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		t.Fatal("loopback emulator required")
	}
	st, err := store.New(t.Context(), "demo-platform-service-e2e", "content_"+strings.ReplaceAll(uuid.NewString(), "-", "")+"_")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	app := testContentApp()
	app.RequireAppCheck = true
	app.Content.PairingEnabled = true
	app.Content.DeepAlwaysOpen = true
	app.Content.ReadingDailyLimit = 10
	app.Content.TermDailyLimit = 1
	reading := validResolveRequest()
	selection, err := Select(reading)
	if err != nil {
		t.Fatal(err)
	}
	pairing := validPairingRequest()
	pairSelection, err := SelectPairing(pairing)
	if err != nil {
		t.Fatal(err)
	}
	items := map[string]Item{}
	for _, id := range selection.BaseIDs {
		items[id] = Item{ID: id, Text: "Runtime reading", Access: AccessFree, Contexts: []Context{ContextReading, ContextTerm}}
	}
	for _, ids := range selection.DeepIDs {
		for _, id := range ids {
			items[id] = Item{ID: id, Text: "Runtime deep", Access: AccessDeep, Contexts: []Context{ContextReading}}
		}
	}
	for _, id := range pairSelection.DeepIDs {
		items[id] = Item{ID: id, Text: "Runtime pairing", Access: AccessDeep, Contexts: []Context{ContextReading}}
	}
	list := make([]Item, 0, len(items))
	for _, item := range items {
		list = append(list, item)
	}
	contentBytes, err := json.Marshal(contentFile{SchemaVersion: 1, Items: list})
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(contentBytes)
	digest := hex.EncodeToString(sum[:])
	version := "sha256-" + digest
	manifest, _ := json.Marshal(releaseManifest{SchemaVersion: 1, ContentVersion: version, ContentSHA256: digest, ItemCount: len(list)})
	active, _ := json.Marshal(activePointer{SchemaVersion: 1, ContentVersion: version})
	objects := memoryObjects{"private-content/production/ungeul/active.json": active, "private-content/production/ungeul/releases/" + version + "/manifest.json": manifest, "private-content/production/ungeul/releases/" + version + "/content.json": contentBytes}
	english := append([]Item(nil), list...)
	for i := range english {
		english[i].Text = "English runtime " + english[i].ID
	}
	englishVersion := putLocaleItems(t, objects, "en", "en", english)
	var server *httptest.Server
	start := func() {
		loader, e := NewReleaseLoader(objects, "production")
		if e != nil {
			t.Fatal(e)
		}
		repo := NewStoreRepository(st)
		svc, e := NewService(fakeApps{app}, loader, repo, NewAccessService(repo, nil, nil))
		if e != nil {
			t.Fatal(e)
		}
		h, e := NewHandler(svc, fakeSessions{session: identity.Session{AppID: "ungeul", AppUserID: "fixture-user", PlatformUserID: "pu_content_runtime"}}, &fakeAppChecks{})
		if e != nil {
			t.Fatal(e)
		}
		mux := http.NewServeMux()
		h.Register(mux)
		server = httptest.NewServer(mux)
		server.Client().Timeout = 15 * time.Second
	}
	start()
	t.Cleanup(func() { server.Close() })
	request := func(method, path string, body any, want int) map[string]any {
		t.Helper()
		var data io.Reader
		if body != nil {
			b, e := json.Marshal(body)
			if e != nil {
				t.Fatal(e)
			}
			data = strings.NewReader(string(b))
		}
		r, e := http.NewRequest(method, server.URL+path, data)
		if e != nil {
			t.Fatal(e)
		}
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Firebase-AppCheck", "fixture-attestation")
		res, e := server.Client().Do(r)
		if e != nil {
			t.Fatal(e)
		}
		defer func() { _ = res.Body.Close() }()
		var envelope map[string]any
		if e = json.NewDecoder(res.Body).Decode(&envelope); e != nil {
			t.Fatal(e)
		}
		if res.StatusCode != want {
			t.Fatalf("%s status=%d want=%d error=%v", path, res.StatusCode, want, envelope["error"])
		}
		result, _ := envelope["result"].(map[string]any)
		return result
	}
	if request("GET", "/v1/content/version", nil, 200)["contentVersion"] != version {
		t.Fatal("version/hash mismatch")
	}
	if got := request("GET", "/v1/content/version?locale=en", nil, 200); got["locale"] != "en" || got["contentVersion"] != englishVersion {
		t.Fatal("English version/hash mismatch")
	}
	first := request("POST", "/v1/content/readings:resolve", reading, 200)
	if len(first["articles"].([]any)) == 0 {
		t.Fatal("reading must resolve articles")
	}
	reading.Locale = "en"
	if second := request("POST", "/v1/content/readings:resolve", reading, 200); second["locale"] != "en" || first["readingKey"] != second["readingKey"] {
		t.Fatal("locale changed reading identity")
	}
	koPair := request("POST", "/v1/content/pairings:resolve", pairing, 200)
	pairing.Locale = "en"
	if enPair := request("POST", "/v1/content/pairings:resolve", pairing, 200); enPair["locale"] != "en" || koPair["pairKey"] != enPair["pairKey"] {
		t.Fatal("locale changed pairing identity")
	}
	request("GET", "/v1/content/deep-access", nil, 200)
	if request("GET", "/v1/content/terms/ilju.mujin?locale=en", nil, 200)["locale"] != "en" {
		t.Fatal("English term")
	}
	server.Close()
	start()
	request("POST", "/v1/content/readings:resolve", reading, 200)
	request("GET", "/v1/content/terms/ilju.mujin", nil, 429)
	request("POST", "/v1/content/readings:resolve", map[string]any{"schemaVersion": 1, "articleIds": []string{"arbitrary"}}, 400)
	request("GET", "/v1/content/version?locale=fr", nil, 400)
	objects["private-content/production/ungeul/releases/"+version+"/content.json"] = []byte(`{"schemaVersion":1,"items":[]}`)
	server.Close()
	start()
	request("GET", "/v1/content/version", nil, 503)
	objects["private-content/production/ungeul/releases/"+version+"/content.json"] = contentBytes
	server.Close()
	start()
	request("GET", "/v1/content/version", nil, 200)
	t.Log("HTTP release hash → reading/pairing/deep/term → persisted limits/restart → forged coordinates rejected → corrupted content rejected/recovered; GCS and attestation provider are fixtures")
}
