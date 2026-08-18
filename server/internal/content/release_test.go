package content

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/seorilabs/platform/server/internal/platformerr"
	"github.com/seorilabs/platform/server/internal/registry"
)

type memoryObjects map[string][]byte

func (m memoryObjects) Read(_ context.Context, bucket, object string) ([]byte, error) {
	value, ok := m[bucket+"/"+object]
	if !ok {
		return nil, errors.New("not found")
	}
	return append([]byte(nil), value...), nil
}

func testContentApp() registry.App {
	return registry.App{
		AppID: "ungeul", Features: map[string]bool{"content": true},
		Content: registry.ContentConfig{Bucket: "private-content", Prefix: "ungeul"},
	}
}

func putRelease(t *testing.T, objects memoryObjects, text string) string {
	return putReleaseAt(t, objects, "ungeul", text)
}

func putReleaseAt(t *testing.T, objects memoryObjects, prefix, text string) string {
	t.Helper()
	file := contentFile{SchemaVersion: SupportedSchemaVersion, Items: []Item{{
		ID: "ilju.gapja", Text: text, Access: AccessFree, Contexts: []Context{ContextReading},
	}}}
	contentBytes, err := json.Marshal(file)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(contentBytes)
	digest := hex.EncodeToString(sum[:])
	version := "sha256-" + digest
	manifestBytes, _ := json.Marshal(releaseManifest{
		SchemaVersion: 1, ContentVersion: version, ContentSHA256: digest, ItemCount: 1,
	})
	activeBytes, _ := json.Marshal(activePointer{SchemaVersion: 1, ContentVersion: version})
	base := "private-content/production/" + prefix + "/"
	objects[base+"active.json"] = activeBytes
	objects[base+"releases/"+version+"/manifest.json"] = manifestBytes
	objects[base+"releases/"+version+"/content.json"] = contentBytes
	return version
}

type blockingObjects struct {
	memoryObjects
	object  string
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *blockingObjects) Read(ctx context.Context, bucket, object string) ([]byte, error) {
	if object == b.object {
		b.once.Do(func() { close(b.started) })
		select {
		case <-b.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return b.memoryObjects.Read(ctx, bucket, object)
}

func TestReleaseLoaderLoadsAndRetainsPreviousGoodRelease(t *testing.T) {
	objects := memoryObjects{}
	firstVersion := putRelease(t, objects, "첫 릴리스")
	now := time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC)
	loader, err := NewReleaseLoader(objects, "production")
	if err != nil {
		t.Fatal(err)
	}
	loader.WithClock(func() time.Time { return now }).WithTTL(time.Minute)

	got, err := loader.Load(t.Context(), testContentApp())
	if err != nil || got.ContentVersion != firstVersion {
		t.Fatalf("first load = %#v, %v", got, err)
	}
	objects["private-content/production/ungeul/active.json"] = []byte(`{"schemaVersion":1,"contentVersion":"broken"}`)
	now = now.Add(2 * time.Minute)

	got, err = loader.Load(t.Context(), testContentApp())
	if err != nil || got.ContentVersion != firstVersion {
		t.Fatalf("fallback load = %#v, %v", got, err)
	}
}

func TestReleaseLoaderRejectsCorruptColdRelease(t *testing.T) {
	objects := memoryObjects{
		"private-content/production/ungeul/active.json": []byte(`{"schemaVersion":1,"contentVersion":"broken"}`),
	}
	loader, err := NewReleaseLoader(objects, "production")
	if err != nil {
		t.Fatal(err)
	}
	_, err = loader.Load(t.Context(), testContentApp())
	if platformerr.CodeOf(err) != platformerr.CodeContentUnavailable {
		t.Fatalf("code = %q, err = %v", platformerr.CodeOf(err), err)
	}
}

func TestReleaseLoaderAcceptsVersionTransitionAndRollback(t *testing.T) {
	objects := memoryObjects{}
	firstVersion := putRelease(t, objects, "첫 릴리스")
	now := time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC)
	loader, err := NewReleaseLoader(objects, "production")
	if err != nil {
		t.Fatal(err)
	}
	loader.WithClock(func() time.Time { return now }).WithTTL(time.Minute)
	if got, err := loader.Load(t.Context(), testContentApp()); err != nil || got.ContentVersion != firstVersion {
		t.Fatalf("first=%q err=%v", got.ContentVersion, err)
	}

	secondVersion := putRelease(t, objects, "둘째 릴리스")
	now = now.Add(2 * time.Minute)
	if got, err := loader.Load(t.Context(), testContentApp()); err != nil || got.ContentVersion != secondVersion {
		t.Fatalf("transition=%q err=%v", got.ContentVersion, err)
	}

	activeBytes, _ := json.Marshal(activePointer{SchemaVersion: 1, ContentVersion: firstVersion})
	objects["private-content/production/ungeul/active.json"] = activeBytes
	now = now.Add(2 * time.Minute)
	if got, err := loader.Load(t.Context(), testContentApp()); err != nil || got.ContentVersion != firstVersion {
		t.Fatalf("rollback=%q err=%v", got.ContentVersion, err)
	}
}

func TestReleaseLoaderDoesNotBlockOtherAppsDuringGCSRead(t *testing.T) {
	objects := memoryObjects{}
	putReleaseAt(t, objects, "ungeul", "운글 릴리스")
	otherVersion := putReleaseAt(t, objects, "other", "다른 앱 릴리스")
	source := &blockingObjects{
		memoryObjects: objects,
		object:        "production/ungeul/active.json",
		started:       make(chan struct{}),
		release:       make(chan struct{}),
	}
	defer func() {
		select {
		case <-source.release:
		default:
			close(source.release)
		}
	}()
	loader, err := NewReleaseLoader(source, "production")
	if err != nil {
		t.Fatal(err)
	}

	firstDone := make(chan error, 1)
	go func() {
		_, loadErr := loader.Load(t.Context(), testContentApp())
		firstDone <- loadErr
	}()
	select {
	case <-source.started:
	case <-time.After(time.Second):
		t.Fatal("첫 앱의 GCS 읽기가 시작되지 않았다")
	}

	other := testContentApp()
	other.AppID = "other"
	other.Content.Prefix = "other"
	otherDone := make(chan error, 1)
	go func() {
		got, loadErr := loader.Load(t.Context(), other)
		if loadErr == nil && got.ContentVersion != otherVersion {
			loadErr = errors.New("다른 앱 릴리스 버전이 일치하지 않는다")
		}
		otherDone <- loadErr
	}()
	select {
	case loadErr := <-otherDone:
		if loadErr != nil {
			t.Fatal(loadErr)
		}
	case <-time.After(time.Second):
		t.Fatal("한 앱의 GCS 읽기가 다른 앱 로드를 막았다")
	}

	close(source.release)
	if loadErr := <-firstDone; loadErr != nil {
		t.Fatal(loadErr)
	}
}

func TestValidateItemRejectsDeepTermBypass(t *testing.T) {
	err := validateItem(Item{
		ID: "seunsal.samjae_in", Text: "심화 해설", Access: AccessDeep,
		Contexts: []Context{ContextReading, ContextTerm},
	})
	if err == nil {
		t.Fatal("심화 본문을 단독 사전 문맥에 허용했다")
	}
}

func TestDecodeStrictRejectsTrailingJSON(t *testing.T) {
	var active activePointer
	if err := decodeStrict([]byte(`{"schemaVersion":1,"contentVersion":"x"}{}`), &active); err == nil {
		t.Fatal("뒤에 붙은 JSON을 허용했다")
	}
}
