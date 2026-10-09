//go:build integration

// Firestore emulator에서 디버그 빌드 계정이 운영 이벤트를 남기지 않는지 검증한다.
//
//	FIRESTORE_EMULATOR_HOST=127.0.0.1:8080 GOOGLE_CLOUD_PROJECT=platform-test \
//	  go test -tags=integration -run TestDebugBuild ./internal/identity/
package identity

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/seorilabs/platform/server/internal/fspath"
	"github.com/seorilabs/platform/server/internal/operational"
	"github.com/seorilabs/platform/server/internal/store"
)

func TestDebugBuildAccountSkipsIdentityCreatedEvent(t *testing.T) {
	// 운영 DB에 이 테스트를 실행할 수 없도록 에뮬레이터를 필수로 한다.
	if os.Getenv("FIRESTORE_EMULATOR_HOST") == "" {
		t.Skip("local Firestore emulator required")
	}
	ctx := context.Background()
	st, err := store.New(ctx, "platform-debug-build-test", "t"+strings.ReplaceAll(uuid.NewString(), "-", "")+"_")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	repo := NewStoreRepository(st).WithOperationalEvents(operational.NewRepository(st))
	const appID = "lizard-tycoon"

	tests := []struct {
		name      string
		uid       string
		debug     bool
		wantEvent bool
	}{
		{"운영 빌드는 신규 가입 이벤트를 남긴다", "release-user", false, true},
		// QA 기기는 pm clear마다 새 익명 계정을 만든다. 그게 #action-events의
		// 신규 가입과 누적 수로 흘러가면 안 된다.
		{"디버그 빌드는 계정만 만들고 이벤트를 남기지 않는다", "debug-user", true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			puid, err := repo.EnsureUser(ctx, appID, NewIdentity{
				UID: tt.uid, Anonymous: true, AuthType: "firebase",
				Client: ClientInfo{AppVersion: "9.9.9", Runtime: "godot-native-android", DebugBuild: tt.debug},
			})
			if err != nil {
				t.Fatal(err)
			}

			eventPath, err := fspath.Parse(
				"operational_event_outbox/" + operational.StableEventID("identity", appID, puid),
			)
			if err != nil {
				t.Fatal(err)
			}
			_, err = st.Get(ctx, eventPath)
			if err != nil && !errors.Is(err, store.ErrNotFound) {
				t.Fatal(err)
			}
			if gotEvent := err == nil; gotEvent != tt.wantEvent {
				t.Fatalf("신규 가입 이벤트 = %v, want %v", gotEvent, tt.wantEvent)
			}

			uPath, err := userPath(puid)
			if err != nil {
				t.Fatal(err)
			}
			snap, err := st.Get(ctx, uPath)
			if err != nil {
				t.Fatal(err)
			}
			var user userDoc
			if err := snap.DataTo(&user); err != nil {
				t.Fatal(err)
			}
			if user.DebugBuild != tt.debug {
				t.Fatalf("계정의 디버그 빌드 표시 = %v, want %v", user.DebugBuild, tt.debug)
			}
		})
	}
}
