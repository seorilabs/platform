package identity

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeAppVersionObserver struct {
	calls []ClientInfo
	err   error
}

func (f *fakeAppVersionObserver) ObserveAppVersion(
	_ context.Context,
	_ string,
	client ClientInfo,
) error {
	f.calls = append(f.calls, client)
	return f.err
}

func TestClientInfoReadsObservationHeaders(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/v1/auth/session", nil)
	r.Header.Set(appVersionHeader, " 1.2.4 ")
	r.Header.Set(runtimeHeader, "godot-native-android")
	r.Header.Set(sdkHeader, "gd/0.6.8")

	client := clientInfo(r)
	if client.AppVersion != "1.2.4" || client.Runtime != "godot-native-android" ||
		client.SDK != "gd/0.6.8" {
		t.Fatalf("실행 환경이 어긋났다: %+v", client)
	}
}

func TestClientInfoDropsMalformedHeaderWithoutFailing(t *testing.T) {
	// 관측 축 하나가 형식을 어겼다고 로그인이 막히면 안 된다.
	for name, value := range map[string]string{
		"상한 초과": strings.Repeat("9", maxClientInfoLen+1),
		"서식 문자": "**1.2.4**",
		"공백 섞임": "1.2.4 dirty",
		"한글 섞임": "버전1",
	} {
		t.Run(name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/v1/auth/session", nil)
			r.Header.Set(appVersionHeader, value)
			r.Header.Set(runtimeHeader, "web")

			client := clientInfo(r)
			if client.AppVersion != "" {
				t.Fatalf("형식을 어긴 버전이 실렸다: %q", client.AppVersion)
			}
			if client.Runtime != "web" {
				t.Fatalf("멀쩡한 축까지 비웠다: %q", client.Runtime)
			}
		})
	}
}

func TestSessionObservesAppVersion(t *testing.T) {
	repo := newMemRepo()
	observer := &fakeAppVersionObserver{}
	svc := newTestService(t, fakeVerifier{}, repo).WithAppVersionObserver(observer)

	client := ClientInfo{AppVersion: "1.2.4", Runtime: "godot-native-android", SDK: "gd/0.6.8"}
	if _, err := svc.CreateSession(context.Background(), testApp().AppID, Credential{
		Kind: KindFirebaseIDToken, Value: "id-token",
	}, client); err != nil {
		t.Fatal(err)
	}

	if len(observer.calls) != 1 || observer.calls[0] != client {
		t.Fatalf("관측 호출이 어긋났다: %+v", observer.calls)
	}
	// 같은 사실이 신규 계정 이벤트에도 실려야 어느 빌드의 가입인지 읽힌다.
	if repo.lastIdentity.Client != client {
		t.Fatalf("계정에 실행 환경이 실리지 않았다: %+v", repo.lastIdentity.Client)
	}
}

func TestBridgeObservesAppVersion(t *testing.T) {
	repo := newMemRepo()
	observer := &fakeAppVersionObserver{}
	svc := newBridgeTestService(
		t, fakeVerifier{}, repo, &fakeCustomTokenIssuer{token: "signed-custom-token"},
	).WithAppVersionObserver(observer)

	client := ClientInfo{AppVersion: "3.0.9", Runtime: "ait-rn", SDK: "ts/0.4.0"}
	if _, err := svc.CreateFirebaseCustomToken(
		context.Background(), "lizard-tycoon", "", client,
	); err != nil {
		t.Fatal(err)
	}

	if len(observer.calls) != 1 || observer.calls[0] != client {
		t.Fatalf("bridge 경로가 관측하지 않았다: %+v", observer.calls)
	}
}

func TestVersionlessClientIsNotObserved(t *testing.T) {
	// 헤더를 보내지 않는 구버전 클라이언트다. 관측할 조합이 없으므로
	// 빈 버전으로 문서를 만들지 않는다.
	repo := newMemRepo()
	observer := &fakeAppVersionObserver{}
	svc := newTestService(t, fakeVerifier{}, repo).WithAppVersionObserver(observer)

	if _, err := svc.CreateSession(context.Background(), testApp().AppID, Credential{
		Kind: KindFirebaseIDToken, Value: "id-token",
	}, ClientInfo{Runtime: "web"}); err != nil {
		t.Fatal(err)
	}
	if len(observer.calls) != 0 {
		t.Fatalf("버전 없는 요청을 관측했다: %+v", observer.calls)
	}
}

func TestObservationFailureDoesNotBlockSession(t *testing.T) {
	// 관측은 인증이 아니다. Firestore가 한 번 흔들렸다고 로그인이 막히면 안 된다.
	repo := newMemRepo()
	observer := &fakeAppVersionObserver{err: errors.New("firestore unavailable")}
	svc := newTestService(t, fakeVerifier{}, repo).WithAppVersionObserver(observer)

	res, err := svc.CreateSession(context.Background(), testApp().AppID, Credential{
		Kind: KindFirebaseIDToken, Value: "id-token",
	}, ClientInfo{AppVersion: "1.2.4"})
	if err != nil {
		t.Fatalf("관측 실패가 세션을 막았다: %v", err)
	}
	if res.PlatformToken == "" {
		t.Fatal("세션 토큰이 비어 있다")
	}
}

func TestAppVersionEventAttributesOmitEmptyAxes(t *testing.T) {
	full := appVersionEventAttributes(
		ClientInfo{AppVersion: "1.2.4", Runtime: "godot-native-android", SDK: "gd/0.6.8"},
	)
	if full["appVersion"] != "1.2.4" || full["runtime"] != "godot-native-android" ||
		full["sdk"] != "gd/0.6.8" {
		t.Fatalf("속성이 어긋났다: %v", full)
	}

	bare := appVersionEventAttributes(ClientInfo{AppVersion: "1.2.4"})
	if _, ok := bare["runtime"]; ok {
		t.Fatalf("빈 런타임이 실렸다: %v", bare)
	}
	if _, ok := bare["sdk"]; ok {
		t.Fatalf("빈 SDK가 실렸다: %v", bare)
	}
}

func TestIdentityEventAttributesCarryBuild(t *testing.T) {
	attributes := identityEventAttributes(NewIdentity{
		UID:      "firebase-user",
		AuthType: "firebase_bridge",
		Client:   ClientInfo{AppVersion: "1.2.4", Runtime: "godot-native-android"},
	})
	if attributes["appVersion"] != "1.2.4" ||
		attributes["runtime"] != "godot-native-android" {
		t.Fatalf("계정 이벤트에 빌드가 실리지 않았다: %v", attributes)
	}

	// SDK 버전은 계정의 사실이 아니라 버전 관측의 사실이다. 여기 싣지 않는다.
	if _, ok := attributes["sdk"]; ok {
		t.Fatalf("계정 이벤트에 SDK가 실렸다: %v", attributes)
	}

	bare := identityEventAttributes(NewIdentity{UID: "firebase-user", AuthType: "firebase"})
	if _, ok := bare["appVersion"]; ok {
		t.Fatalf("구버전 클라이언트에 빈 버전이 실렸다: %v", bare)
	}
	if _, ok := bare["runtime"]; ok {
		t.Fatalf("구버전 클라이언트에 빈 런타임이 실렸다: %v", bare)
	}
}

func TestAppVersionPathSeparatesAdjacentValues(t *testing.T) {
	// "1.2" + "4"와 "" + "1.2.4"가 같은 문서가 되면 한쪽이 조용히 묻힌다.
	first, err := appVersionPath("lizard-tycoon", "1.2", "4")
	if err != nil {
		t.Fatal(err)
	}
	second, err := appVersionPath("lizard-tycoon", "", "1.2.4")
	if err != nil {
		t.Fatal(err)
	}
	if first.String() == second.String() {
		t.Fatalf("서로 다른 조합이 같은 경로를 쓴다: %v", first)
	}
}
