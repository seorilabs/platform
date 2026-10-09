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
	"time"

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

func TestDebugBuildAccountIsNotCountedAsUser(t *testing.T) {
	// Backoffice 플랫폼 개요의 전체·활성 사용자 수가 이 집계다. QA 기기가
	// 저장을 지울 때마다 만든 계정이 여기 쌓이면 실사용자 규모를 읽을 수 없다.
	if os.Getenv("FIRESTORE_EMULATOR_HOST") == "" {
		t.Skip("local Firestore emulator required")
	}
	ctx := context.Background()
	st, err := store.New(ctx, "platform-debug-build-test", "t"+strings.ReplaceAll(uuid.NewString(), "-", "")+"_")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	repo := NewStoreRepository(st)
	for _, tt := range []struct {
		uid   string
		debug bool
	}{
		{"release-user", false},
		{"debug-user-1", true},
		{"debug-user-2", true},
	} {
		if _, err := repo.EnsureUser(ctx, "lizard-tycoon", NewIdentity{
			UID: tt.uid, Anonymous: true, AuthType: "firebase",
			Client: ClientInfo{DebugBuild: tt.debug},
		}); err != nil {
			t.Fatal(err)
		}
	}

	counts, err := repo.CountUsers(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	want := UserCounts{Total: 1, ActiveHour: 1, ActiveDay: 1, ActiveWeek: 1}
	if counts != want {
		t.Fatalf("사용자 수 = %+v, want %+v", counts, want)
	}
}
