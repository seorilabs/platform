package apple

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/richzw/appstore"

	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/platformerr"
)

// 도메인 판단 로직을 Apple 자격증명 없이 검증한다.
//
// JWS 서명 검증은 richzw/appstore가 하고 그건 라이브러리의 책임이다.
// 여기서 지키는 것은 우리 불변식이다 — 카탈로그 상품 유형 대조와
// 환경 fallback 금지, 상품 유형별 canonicalId 선택.

const (
	testBundleID = "com.seorilabs.lizardtycoon"
	testProduct  = "com.seorilabs.gecko.galaxy"
)

var appleNow = time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)

// fakeSource는 App Store Server API를 대신한다.
type fakeSource struct {
	tx     *appstore.JWSTransaction
	err    error
	finish func(transactionID string) error

	gotTransactionID string
	finishedID       string
}

func (f *fakeSource) GetTransaction(_ context.Context, id string) (*appstore.JWSTransaction, error) {
	f.gotTransactionID = id
	if f.err != nil {
		return nil, f.err
	}
	return f.tx, nil
}

func (f *fakeSource) Finish(_ context.Context, id string) error {
	f.finishedID = id
	if f.finish != nil {
		return f.finish(id)
	}
	return nil
}

// validTx는 정상 비소비성 구매다. 테스트마다 필요한 필드만 바꾼다.
func validTx() *appstore.JWSTransaction {
	return &appstore.JWSTransaction{
		TransactionID:         "2000000900000001",
		OriginalTransactionId: "2000000800000000",
		BundleID:              testBundleID,
		ProductID:             testProduct,
		Type:                  appstore.NonConsumable,
		InAppOwnershipType:    "PURCHASED",
		Environment:           appstore.Production,
		AppAccountToken:       "9f8e7d6c-0000-4000-8000-000000000001",
		PurchaseDate:          appleNow.Add(-time.Hour).UnixMilli(),
	}
}

func appleProof() domain.Proof {
	return domain.Proof{
		Platform:    domain.PlatformAppStore,
		ProductID:   testProduct,
		ProductType: domain.ProductNonConsumable,
		Token:       "2000000900000001",
	}
}

func newVerifier(t *testing.T, src transactionSource, sandbox bool) *Verifier {
	t.Helper()
	v, err := New(src, testBundleID, sandbox, WithClock(func() time.Time { return appleNow }))
	if err != nil {
		t.Fatalf("검증기 생성 실패: %v", err)
	}
	return v
}

func TestVerifySuccess(t *testing.T) {
	src := &fakeSource{tx: validTx()}
	v := newVerifier(t, src, false)

	got, err := v.Verify(context.Background(), appleProof())
	if err != nil {
		t.Fatalf("검증 실패: %v", err)
	}

	// 불변식 1. Apple의 canonicalId는 originalTransactionId다.
	// transactionId는 복원할 때마다 바뀌어 멱등키가 될 수 없다.
	if got.CanonicalID != "2000000800000000" {
		t.Errorf("canonicalId = %q, want originalTransactionId", got.CanonicalID)
	}
	if got.ProviderOrderID != "2000000900000001" {
		t.Errorf("providerOrderId = %q, want transactionId", got.ProviderOrderID)
	}
	if got.State != domain.StateActive {
		t.Errorf("state = %q, want active", got.State)
	}
	if got.Completion != domain.CompletionAppleFinish {
		t.Errorf("completion = %q, want apple_finish", got.Completion)
	}
	if got.PlatformAccountID != "9f8e7d6c-0000-4000-8000-000000000001" {
		t.Errorf("platformAccountId = %q", got.PlatformAccountID)
	}
	if !got.ObservedAt.Equal(appleNow) {
		t.Errorf("observedAt = %v", got.ObservedAt)
	}
	if got.PurchasedAt.IsZero() {
		t.Error("purchasedAt이 비었다")
	}
	// 조회는 transactionId로 한다
	if src.gotTransactionID != "2000000900000001" {
		t.Errorf("조회 ID = %q", src.gotTransactionID)
	}
}

// 불변식 9의 절반. production과 sandbox를 자동으로 오가지 않는다.
//
// Apple 문서는 production 404 시 sandbox 재시도를 권하지만,
// 그러면 샌드박스 구매로 실제 지급을 받을 수 있다.
func TestEnvironmentIsolation(t *testing.T) {
	tests := []struct {
		name      string
		sandbox   bool
		txEnv     appstore.Environment
		wantError bool
	}{
		{"production 서버 + production 구매", false, appstore.Production, false},
		{"production 서버 + sandbox 구매", false, appstore.Sandbox, true},
		{"sandbox 서버 + sandbox 구매", true, appstore.Sandbox, false},
		{"sandbox 서버 + production 구매", true, appstore.Production, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tx := validTx()
			tx.Environment = tt.txEnv
			v := newVerifier(t, &fakeSource{tx: tx}, tt.sandbox)

			_, err := v.Verify(context.Background(), appleProof())

			if !tt.wantError {
				if err != nil {
					t.Fatalf("거부하면 안 되는데 거부했다: %v", err)
				}
				return
			}
			if code := platformerr.CodeOf(err); code != platformerr.CodeEnvironmentMismatch {
				t.Errorf("code = %q, want environment_mismatch", code)
			}
		})
	}
}

func TestProductTypeMustMatchCatalog(t *testing.T) {
	tests := []struct {
		name        string
		productType domain.ProductType
		iapType     appstore.IAPType
		wantOK      bool
	}{
		{"비소모성 일치", domain.ProductNonConsumable, appstore.NonConsumable, true},
		{"소모성 일치", domain.ProductConsumable, appstore.Consumable, true},
		{"카탈로그와 불일치", domain.ProductNonConsumable, appstore.Consumable, false},
		{"자동 갱신 구독", domain.ProductConsumable, appstore.AutoRenewable, false},
		{"비갱신 구독", domain.ProductConsumable, appstore.NonRenewable, false},
		{"빈 제공자 유형", domain.ProductNonConsumable, "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tx := validTx()
			tx.Type = tt.iapType
			v := newVerifier(t, &fakeSource{tx: tx}, false)
			proof := appleProof()
			proof.ProductType = tt.productType

			_, err := v.Verify(context.Background(), proof)

			if tt.wantOK {
				if err != nil {
					t.Fatalf("지원 유형인데 거부했다: %v", err)
				}
				return
			}
			if code := platformerr.CodeOf(err); code != platformerr.CodeProductTypeMismatch {
				t.Errorf("code = %q, want product_type_mismatch", code)
			}
		})
	}
}

func TestConsumableUsesTransactionIDAsCanonicalID(t *testing.T) {
	tx := validTx()
	tx.Type = appstore.Consumable
	proof := appleProof()
	proof.ProductType = domain.ProductConsumable

	got, err := newVerifier(t, &fakeSource{tx: tx}, false).Verify(context.Background(), proof)
	if err != nil {
		t.Fatal(err)
	}
	if got.CanonicalID != tx.TransactionID {
		t.Fatalf("canonicalId=%q, want transactionId=%q", got.CanonicalID, tx.TransactionID)
	}
}

func TestConsumableRejectsMultipleQuantity(t *testing.T) {
	tx := validTx()
	tx.Type = appstore.Consumable
	tx.Quantity = 2
	proof := appleProof()
	proof.ProductType = domain.ProductConsumable

	_, err := newVerifier(t, &fakeSource{tx: tx}, false).Verify(context.Background(), proof)
	if platformerr.CodeOf(err) != platformerr.CodeProviderResponseInvalid {
		t.Fatalf("code=%q err=%v", platformerr.CodeOf(err), err)
	}
}

func TestWebhookCanInferSignedConsumableType(t *testing.T) {
	tx := validTx()
	tx.Type = appstore.Consumable
	proof := appleProof()
	proof.ProductType = ""

	got, err := newVerifier(t, &fakeSource{tx: tx}, false).Verify(context.Background(), proof)
	if err != nil {
		t.Fatal(err)
	}
	if got.CanonicalID != tx.TransactionID {
		t.Fatalf("canonicalId=%q", got.CanonicalID)
	}
}

// 가족 공유 구매를 지급하면 한 번 산 것으로 여러 계정이 받는다.
func TestRejectsFamilyShared(t *testing.T) {
	tx := validTx()
	tx.InAppOwnershipType = "FAMILY_SHARED"
	v := newVerifier(t, &fakeSource{tx: tx}, false)

	_, err := v.Verify(context.Background(), appleProof())
	if code := platformerr.CodeOf(err); code != platformerr.CodePurchaseInvalid {
		t.Errorf("code = %q, want purchase_invalid", code)
	}
}

func TestRejectsOtherBundle(t *testing.T) {
	tx := validTx()
	tx.BundleID = "com.someone.else"
	v := newVerifier(t, &fakeSource{tx: tx}, false)

	_, err := v.Verify(context.Background(), appleProof())
	if code := platformerr.CodeOf(err); code != platformerr.CodeBundleMismatch {
		t.Errorf("code = %q, want bundle_mismatch", code)
	}
}

func TestRejectsProductMismatch(t *testing.T) {
	tx := validTx()
	tx.ProductID = "com.seorilabs.other.product"
	v := newVerifier(t, &fakeSource{tx: tx}, false)

	_, err := v.Verify(context.Background(), appleProof())
	if code := platformerr.CodeOf(err); code != platformerr.CodeProductMismatch {
		t.Errorf("code = %q, want product_mismatch", code)
	}
}

// 환불된 거래는 지급하지 않는다. 완료 처리도 하지 않는다.
func TestRevokedTransaction(t *testing.T) {
	tx := validTx()
	tx.RevocationDate = appleNow.Add(-time.Minute).UnixMilli()
	v := newVerifier(t, &fakeSource{tx: tx}, false)

	got, err := v.Verify(context.Background(), appleProof())
	if err != nil {
		t.Fatalf("검증 실패: %v", err)
	}
	if got.State != domain.StateRevoked {
		t.Errorf("state = %q, want revoked", got.State)
	}
	if got.Completion != domain.CompletionNone {
		t.Errorf("completion = %q, want none", got.Completion)
	}
}

func TestRejectsMissingOriginalTransactionID(t *testing.T) {
	tx := validTx()
	tx.OriginalTransactionId = ""
	v := newVerifier(t, &fakeSource{tx: tx}, false)

	_, err := v.Verify(context.Background(), appleProof())
	if code := platformerr.CodeOf(err); code != platformerr.CodeProviderResponseInvalid {
		t.Errorf("code = %q, want provider_response_invalid", code)
	}
}

func TestVerifyRejectsBadInput(t *testing.T) {
	v := newVerifier(t, &fakeSource{tx: validTx()}, false)

	t.Run("다른 마켓 증명", func(t *testing.T) {
		p := appleProof()
		p.Platform = domain.PlatformGooglePlay
		_, err := v.Verify(context.Background(), p)
		if code := platformerr.CodeOf(err); code != platformerr.CodePlatformMismatch {
			t.Errorf("code = %q, want platform_mismatch", code)
		}
	})

	t.Run("빈 토큰", func(t *testing.T) {
		p := appleProof()
		p.Token = ""
		_, err := v.Verify(context.Background(), p)
		if code := platformerr.CodeOf(err); code != platformerr.CodeProofInvalid {
			t.Errorf("code = %q, want purchase_proof_invalid", code)
		}
	})

	t.Run("빈 응답", func(t *testing.T) {
		v := newVerifier(t, &fakeSource{tx: nil}, false)
		_, err := v.Verify(context.Background(), appleProof())
		if code := platformerr.CodeOf(err); code != platformerr.CodeProviderResponseInvalid {
			t.Errorf("code = %q, want provider_response_invalid", code)
		}
	})
}

// 조회 에러는 그대로 올라온다. 매핑은 client.go의 책임이다.
func TestVerifyPropagatesSourceError(t *testing.T) {
	want := platformerr.New(platformerr.CodePurchaseNotFound, "없어요")
	v := newVerifier(t, &fakeSource{err: want}, false)

	_, err := v.Verify(context.Background(), appleProof())
	if code := platformerr.CodeOf(err); code != platformerr.CodePurchaseNotFound {
		t.Errorf("code = %q, want purchase_not_found", code)
	}
}

// finishTransaction은 transactionId로 부른다. originalTransactionId가 아니다.
func TestCompleteGrantUsesTransactionID(t *testing.T) {
	src := &fakeSource{}
	v := newVerifier(t, src, false)

	p := domain.VerifiedPurchase{
		Platform:        domain.PlatformAppStore,
		CanonicalID:     "2000000800000000", // originalTransactionId
		ProviderOrderID: "2000000900000001", // transactionId
		Completion:      domain.CompletionAppleFinish,
	}

	if err := v.CompleteGrant(context.Background(), p); err != nil {
		t.Fatalf("완료 처리 실패: %v", err)
	}
	if src.finishedID != "2000000900000001" {
		t.Errorf("완료 처리 ID = %q, want transactionId", src.finishedID)
	}
}

// 복원 경로에서 transactionId를 잃었으면 originalTransactionId로 대체한다.
func TestCompleteGrantFallsBackToCanonicalID(t *testing.T) {
	src := &fakeSource{}
	v := newVerifier(t, src, false)

	p := domain.VerifiedPurchase{
		Platform:    domain.PlatformAppStore,
		CanonicalID: "2000000800000000",
		Completion:  domain.CompletionAppleFinish,
	}

	if err := v.CompleteGrant(context.Background(), p); err != nil {
		t.Fatalf("완료 처리 실패: %v", err)
	}
	if src.finishedID != "2000000800000000" {
		t.Errorf("완료 처리 ID = %q", src.finishedID)
	}
}

func TestCompleteGrantRejectsWrongCompletion(t *testing.T) {
	v := newVerifier(t, &fakeSource{}, false)

	tests := []struct {
		name string
		p    domain.VerifiedPurchase
	}{
		{"다른 마켓", domain.VerifiedPurchase{
			Platform:   domain.PlatformGooglePlay,
			Completion: domain.CompletionAppleFinish,
		}},
		{"다른 완료 방식", domain.VerifiedPurchase{
			Platform:   domain.PlatformAppStore,
			Completion: domain.CompletionGoogleAcknowledge,
		}},
		{"식별자 없음", domain.VerifiedPurchase{
			Platform:   domain.PlatformAppStore,
			Completion: domain.CompletionAppleFinish,
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := v.CompleteGrant(context.Background(), tt.p); err == nil {
				t.Error("거부하지 않았다")
			}
		})
	}
}

// 완료 실패는 그대로 올라간다. 지급 롤백은 상위(불변식 7)가 판단한다.
func TestCompleteGrantPropagatesError(t *testing.T) {
	want := errors.New("apple down")
	src := &fakeSource{finish: func(string) error { return want }}
	v := newVerifier(t, src, false)

	p := domain.VerifiedPurchase{
		Platform:        domain.PlatformAppStore,
		ProviderOrderID: "2000000900000001",
		Completion:      domain.CompletionAppleFinish,
	}
	if err := v.CompleteGrant(context.Background(), p); !errors.Is(err, want) {
		t.Errorf("err = %v, want %v", err, want)
	}
}

func TestNewValidation(t *testing.T) {
	if _, err := New(nil, testBundleID, false); err == nil {
		t.Error("nil source를 허용했다")
	}
	if _, err := New(&fakeSource{}, "", false); err == nil {
		t.Error("빈 번들 ID를 허용했다")
	}
}

// production에서 폐기 확인을 끄면 탈취된 인증서로 만든 위조 JWS를
// 그대로 신뢰하게 된다. 부팅 시점에 잡는다.
func TestClientRequiresOCSPInProduction(t *testing.T) {
	base := Config{
		KeyContent: []byte("-----BEGIN PRIVATE KEY-----\nfake\n-----END PRIVATE KEY-----"),
		KeyID:      "2X9R4HXF34",
		Issuer:     "57246542-96fe-1a63-e053-0824d011072a",
		BundleID:   testBundleID,
	}

	t.Run("production + OCSP 없음 → 거부", func(t *testing.T) {
		cfg := base
		cfg.Sandbox = false
		cfg.RequireOCSP = false
		if _, err := NewClient(cfg); err == nil {
			t.Fatal("production에서 폐기 확인 없이 통과시켰다")
		}
	})

	t.Run("sandbox는 OCSP 없이 허용", func(t *testing.T) {
		cfg := base
		cfg.Sandbox = true
		cfg.RequireOCSP = false
		if _, err := NewClient(cfg); err != nil {
			t.Fatalf("sandbox를 거부했다: %v", err)
		}
	})

	t.Run("필수 설정 누락", func(t *testing.T) {
		for _, name := range []string{"key", "keyID", "issuer", "bundleID"} {
			cfg := base
			cfg.Sandbox = true
			switch name {
			case "key":
				cfg.KeyContent = nil
			case "keyID":
				cfg.KeyID = ""
			case "issuer":
				cfg.Issuer = ""
			case "bundleID":
				cfg.BundleID = ""
			}
			if _, err := NewClient(cfg); err == nil {
				t.Errorf("%s 누락을 허용했다", name)
			}
		}
	})
}
