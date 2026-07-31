package verify

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/seorilabs/platform/server/internal/iap/binding"
	"github.com/seorilabs/platform/server/internal/iap/catalog"
	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/iap/ledger"
	"github.com/seorilabs/platform/server/internal/platformerr"
)

// fake들이 이 패키지의 인터페이스를 import 없이 만족한다.
// Go의 암묵적 인터페이스라 원본의 dependencies 주입보다 단순하다.

type fakeVerifier struct {
	platform   domain.Platform
	purchase   domain.VerifiedPurchase
	verifyErr  error
	completeEr error
	completed  int
}

func (f *fakeVerifier) Platform() domain.Platform { return f.platform }

func (f *fakeVerifier) Verify(context.Context, domain.Proof) (domain.VerifiedPurchase, error) {
	if f.verifyErr != nil {
		return domain.VerifiedPurchase{}, f.verifyErr
	}
	return f.purchase, nil
}

func (f *fakeVerifier) CompleteGrant(context.Context, domain.VerifiedPurchase) error {
	f.completed++
	return f.completeEr
}

type fakeLedger struct {
	granted  []ledger.GrantInput
	pending  []ledger.GrantInput
	active   []string
	grantErr error
}

func (f *fakeLedger) Grant(_ context.Context, in ledger.GrantInput) (domain.GrantResult, error) {
	if f.grantErr != nil {
		return domain.GrantResult{}, f.grantErr
	}
	f.granted = append(f.granted, in)
	f.active = append(f.active, in.EntitlementID)
	return domain.GrantResult{
		Granted:       true,
		EntitlementID: in.EntitlementID,
		Entitlements:  f.active,
	}, nil
}

func (f *fakeLedger) RecordPending(_ context.Context, in ledger.GrantInput) error {
	f.pending = append(f.pending, in)
	return nil
}

func (f *fakeLedger) ListActive(context.Context, string) ([]string, error) {
	return f.active, nil
}

type fakeOutbox struct{ enqueued int }

func (f *fakeOutbox) Enqueue(context.Context, string, domain.VerifiedPurchase) error {
	f.enqueued++
	return nil
}

const catalogJSON = `{
  "version": 1,
  "entitlements": {
    "sp_galaxy_gecko": {"google_play": "gecko_galaxy", "app_store": "com.x.gecko"}
  }
}`

func newTestService(t *testing.T, v *fakeVerifier, l *fakeLedger, out OutboxWriter) *Service {
	t.Helper()

	cat, err := catalog.Parse([]byte(catalogJSON), nil)
	if err != nil {
		t.Fatalf("카탈로그 파싱 실패: %v", err)
	}

	s, err := New(Config{
		Verifiers: []Verifier{v},
		Ledger:    l,
		Catalog:   cat,
		Outbox:    out,
	})
	if err != nil {
		t.Fatalf("서비스 생성 실패: %v", err)
	}
	return s
}

func activePurchase() domain.VerifiedPurchase {
	now := time.Now().UTC()
	return domain.VerifiedPurchase{
		Platform:    domain.PlatformGooglePlay,
		ProductID:   "gecko_galaxy",
		CanonicalID: "token-1",
		PurchasedAt: now,
		ObservedAt:  now,
		State:       domain.StateActive,
		Completion:  domain.CompletionGoogleAcknowledge,
	}
}

func TestVerifyPurchaseGrants(t *testing.T) {
	v := &fakeVerifier{platform: domain.PlatformGooglePlay, purchase: activePurchase()}
	l := &fakeLedger{}
	s := newTestService(t, v, l, nil)

	out, err := s.VerifyPurchase(context.Background(), "lizard-tycoon", "pu_1", domain.Proof{
		Platform:  domain.PlatformGooglePlay,
		ProductID: "gecko_galaxy",
		Token:     "token-1",
	})
	if err != nil {
		t.Fatalf("검증 실패: %v", err)
	}

	if out.Status != "verified" {
		t.Errorf("status = %q, want verified", out.Status)
	}
	if out.EntitlementID != "sp_galaxy_gecko" {
		t.Errorf("entitlementId = %q", out.EntitlementID)
	}
	if out.Granted == nil || !*out.Granted {
		t.Error("granted가 true가 아니다")
	}
	if len(l.granted) != 1 {
		t.Errorf("원장 지급 호출 = %d, want 1", len(l.granted))
	}
	if v.completed != 1 {
		t.Errorf("마켓 완료 호출 = %d, want 1", v.completed)
	}
	if out.Completion == nil || out.Completion.Action != domain.ActionNone {
		t.Errorf("completion = %+v, want none", out.Completion)
	}
}

// 불변식 7. 마켓 완료가 실패해도 지급은 롤백하지 않는다.
//
// 반대로 하면 "돈은 나갔는데 물건이 없다"가 된다.
func TestCompleteFailureDoesNotRollbackGrant(t *testing.T) {
	v := &fakeVerifier{
		platform:   domain.PlatformGooglePlay,
		purchase:   activePurchase(),
		completeEr: errors.New("마켓 응답 없음"),
	}
	l := &fakeLedger{}
	out := &fakeOutbox{}
	s := newTestService(t, v, l, out)

	res, err := s.VerifyPurchase(context.Background(), "lizard-tycoon", "pu_1", domain.Proof{
		Platform:  domain.PlatformGooglePlay,
		ProductID: "gecko_galaxy",
		Token:     "token-1",
	})
	if err != nil {
		t.Fatalf("완료 실패로 전체가 실패했다: %v", err)
	}

	// 지급은 유지된다
	if len(l.granted) != 1 {
		t.Error("불변식 7 위반: 완료 실패로 지급이 롤백됐다")
	}
	if res.Granted == nil || !*res.Granted {
		t.Error("granted가 false가 됐다")
	}

	// 클라이언트에게 재시도 중임을 알린다
	if res.Completion == nil || res.Completion.Action != domain.ActionRetryServerCompletion {
		t.Errorf("completion = %+v, want retry_server_completion", res.Completion)
	}

	// 워커가 집을 수 있게 outbox에 넣는다
	if out.enqueued != 1 {
		t.Errorf("outbox 적재 = %d, want 1", out.enqueued)
	}
}

func TestPendingPurchase(t *testing.T) {
	p := activePurchase()
	p.State = domain.StatePending

	v := &fakeVerifier{platform: domain.PlatformGooglePlay, purchase: p}
	l := &fakeLedger{}
	s := newTestService(t, v, l, nil)

	out, err := s.VerifyPurchase(context.Background(), "app", "pu_1", domain.Proof{
		Platform:  domain.PlatformGooglePlay,
		ProductID: "gecko_galaxy",
	})
	if err != nil {
		t.Fatalf("검증 실패: %v", err)
	}

	if out.Status != "pending" {
		t.Errorf("status = %q, want pending", out.Status)
	}
	if len(l.pending) != 1 {
		t.Error("pending이 기록되지 않았다")
	}
	if len(l.granted) != 0 {
		t.Error("pending인데 지급했다")
	}
	// 완료 처리를 하면 안 된다
	if v.completed != 0 {
		t.Error("pending인데 마켓 완료를 호출했다")
	}
}

// AIT는 서버가 아니라 클라이언트가 완료 처리를 한다.
func TestAppsInTossDefersCompletionToClient(t *testing.T) {
	p := activePurchase()
	p.Platform = domain.PlatformAppsInToss
	p.Completion = domain.CompletionAppsInTossClient
	p.CanonicalID = "order-123"

	v := &fakeVerifier{platform: domain.PlatformAppsInToss, purchase: p}
	l := &fakeLedger{}

	cat, _ := catalog.Parse([]byte(`{"version":1,"entitlements":{
      "sp_galaxy_gecko":{"apps_in_toss":"ait_gecko"}}}`), nil)
	s, err := New(Config{Verifiers: []Verifier{v}, Ledger: l, Catalog: cat})
	if err != nil {
		t.Fatalf("서비스 생성 실패: %v", err)
	}

	out, err := s.VerifyPurchase(context.Background(), "app", "pu_1", domain.Proof{
		Platform:  domain.PlatformAppsInToss,
		ProductID: "ait_gecko",
	})
	if err != nil {
		t.Fatalf("검증 실패: %v", err)
	}

	if out.Completion == nil || out.Completion.Action != domain.ActionAppsInTossCompleteGrant {
		t.Errorf("completion = %+v, want apps_in_toss_complete_product_grant", out.Completion)
	}
	if out.Completion.OrderID != "order-123" {
		t.Errorf("orderId = %q", out.Completion.OrderID)
	}
	// 서버가 마켓 완료를 부르면 안 된다
	if v.completed != 0 {
		t.Error("AIT인데 서버가 완료를 호출했다")
	}
}

// 자격증명이 없어 조립하지 못한 마켓은 명확히 거부한다.
// AIT mTLS 인증서 미확보로 실제 일어날 수 있는 상황이다.
func TestUnsupportedPlatform(t *testing.T) {
	v := &fakeVerifier{platform: domain.PlatformGooglePlay, purchase: activePurchase()}
	s := newTestService(t, v, &fakeLedger{}, nil)

	if s.Supports(domain.PlatformAppsInToss) {
		t.Error("없는 마켓을 지원한다고 한다")
	}

	_, err := s.VerifyPurchase(context.Background(), "app", "pu_1", domain.Proof{
		Platform:  domain.PlatformAppsInToss,
		ProductID: "x",
	})
	if code := platformerr.CodeOf(err); code != platformerr.CodePlatformUnavailable {
		t.Errorf("code = %q, want platform_unavailable", code)
	}
}

func TestUnknownSKU(t *testing.T) {
	v := &fakeVerifier{platform: domain.PlatformGooglePlay, purchase: activePurchase()}
	s := newTestService(t, v, &fakeLedger{}, nil)

	_, err := s.VerifyPurchase(context.Background(), "app", "pu_1", domain.Proof{
		Platform:  domain.PlatformGooglePlay,
		ProductID: "팔지않는상품",
	})
	if code := platformerr.CodeOf(err); code != platformerr.CodeProductNotAllowed {
		t.Errorf("code = %q, want product_not_allowed", code)
	}
}

// 계정 바인딩이 다르면 다른 사용자의 구매를 가로채는 것이다.
func TestAccountBindingMismatch(t *testing.T) {
	keyring, err := binding.NewKeyring([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("keyring 생성 실패: %v", err)
	}

	p := activePurchase()
	// 다른 사용자에게 발급된 계정 참조를 실어 보낸다
	p.PlatformAccountID = keyring.GoogleAccountID("pu_다른사람")

	v := &fakeVerifier{platform: domain.PlatformGooglePlay, purchase: p}
	l := &fakeLedger{}
	cat, _ := catalog.Parse([]byte(catalogJSON), nil)

	s, err := New(Config{
		Verifiers: []Verifier{v}, Ledger: l, Catalog: cat, Keyring: keyring,
	})
	if err != nil {
		t.Fatalf("서비스 생성 실패: %v", err)
	}

	_, err = s.VerifyPurchase(context.Background(), "app", "pu_나", domain.Proof{
		Platform:  domain.PlatformGooglePlay,
		ProductID: "gecko_galaxy",
	})
	if err == nil {
		t.Fatal("다른 사용자의 계정 참조를 통과시켰다")
	}
	if code := platformerr.CodeOf(err); code != platformerr.CodeAccountBindingMismatch {
		t.Errorf("code = %q, want account_binding_mismatch", code)
	}
	if len(l.granted) != 0 {
		t.Error("바인딩이 틀렸는데 지급했다")
	}
}

func TestAccountReferences(t *testing.T) {
	keyring, _ := binding.NewKeyring([]byte("0123456789abcdef0123456789abcdef"))
	cat, _ := catalog.Parse([]byte(catalogJSON), nil)

	s, _ := New(Config{Ledger: &fakeLedger{}, Catalog: cat, Keyring: keyring})

	g, a, err := s.AccountReferences("pu_1")
	if err != nil {
		t.Fatalf("발급 실패: %v", err)
	}
	if !binding.ValidGoogleFormat(g) {
		t.Errorf("Google 형식이 아니다: %q", g)
	}
	if !binding.ValidAppleFormat(a) {
		t.Errorf("Apple 형식이 아니다: %q", a)
	}
}

// 활성 목록은 null이 아니라 빈 배열로 나가야 한다.
// 클라이언트가 length를 바로 쓸 수 있어야 한다.
func TestEmptyEntitlementsIsArray(t *testing.T) {
	cat, _ := catalog.Parse([]byte(catalogJSON), nil)
	s, _ := New(Config{Ledger: &fakeLedger{}, Catalog: cat})

	list, err := s.ListEntitlements(context.Background(), "pu_없음")
	if err != nil {
		t.Fatalf("조회 실패: %v", err)
	}
	if list == nil {
		t.Error("nil이 나왔다. 빈 배열이어야 한다")
	}
}
