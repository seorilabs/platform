// Package apple은 App Store 구매를 검증한다.
//
// JWS 서명 검증은 richzw/appstore에 맡긴다. ADR 0009다.
// 그 라이브러리는 Apple Root CA G3를 하드코딩하고 x509 체인을
// 실제로 검증한다. 우리가 더한 것은 OCSP 폐기 확인이다.
//
// 라이브러리는 transactionSource 인터페이스 뒤에 둔다.
// 도메인 판단 로직 — 카탈로그 상품 유형 대조, 환경 대조,
// 상품 유형별 canonicalId 선택 — 은 전부 이 패키지의 순수 함수에 있어서
// Apple 자격증명 없이 테스트할 수 있다.
package apple

import (
	"context"
	"time"

	"github.com/richzw/appstore"

	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/platformerr"
)

// transactionSource는 App Store Server API다.
//
// 소비자인 이 패키지가 인터페이스를 정의한다. 구현은 client.go의
// 실제 어댑터와 테스트의 fake 두 벌이다.
type transactionSource interface {
	// GetTransaction은 transactionId로 거래를 조회하고 JWS를 검증한다.
	GetTransaction(ctx context.Context, transactionID string) (*appstore.JWSTransaction, error)
	// Finish는 finishTransaction을 호출한다.
	Finish(ctx context.Context, transactionID string) error
}

// Verifier는 App Store 구매 검증기다.
type Verifier struct {
	src         transactionSource
	bundleID    string
	environment appstore.Environment
	now         func() time.Time
}

// Option은 검증기 설정이다.
type Option func(*Verifier)

// WithClock은 시계를 주입한다.
func WithClock(now func() time.Time) Option {
	return func(v *Verifier) { v.now = now }
}

// New는 검증기를 만든다.
//
// environment는 부팅 시점에 고정된다. 불변식 9의 절반이다.
// 런타임에 production과 sandbox를 오가지 않는다.
func New(src transactionSource, bundleID string, sandbox bool, opts ...Option) (*Verifier, error) {
	if src == nil {
		return nil, platformerr.New(platformerr.CodeProviderConfigInvalid,
			"App Store 클라이언트가 필요해요")
	}
	if bundleID == "" {
		return nil, platformerr.New(platformerr.CodeProviderConfigInvalid,
			"App Store 번들 ID가 필요해요")
	}

	env := appstore.Production
	if sandbox {
		env = appstore.Sandbox
	}

	v := &Verifier{
		src:         src,
		bundleID:    bundleID,
		environment: env,
		now:         time.Now,
	}
	for _, o := range opts {
		o(v)
	}
	return v, nil
}

func (v *Verifier) Platform() domain.Platform { return domain.PlatformAppStore }

// Environment는 부팅 시 고정된 환경이다. 진단과 부팅 검사에 쓴다.
func (v *Verifier) Environment() string { return string(v.environment) }

// Verify는 transactionId로 App Store에 확인한다.
func (v *Verifier) Verify(ctx context.Context, proof domain.Proof) (domain.VerifiedPurchase, error) {
	if proof.Platform != domain.PlatformAppStore {
		return domain.VerifiedPurchase{}, platformerr.New(platformerr.CodePlatformMismatch,
			"App Store 검증기에 다른 마켓 증명이 왔어요")
	}
	// productId는 없어도 된다. 조회는 transactionId만으로 하고
	// 상품은 응답에서 온다. 알림 재검증 경로가 이 형태를 쓴다.
	if proof.Token == "" {
		return domain.VerifiedPurchase{}, platformerr.New(platformerr.CodeProofInvalid,
			"구매 정보가 비어 있어요")
	}

	observedAt := v.now().UTC()

	tx, err := v.src.GetTransaction(ctx, proof.Token)
	if err != nil {
		return domain.VerifiedPurchase{}, err
	}
	if tx == nil {
		return domain.VerifiedPurchase{}, platformerr.New(platformerr.CodeProviderResponseInvalid,
			"App Store 응답이 비어 있어요")
	}

	return v.mapTransaction(tx, proof, observedAt)
}

// mapTransaction은 검증된 거래를 도메인 구매로 옮긴다.
//
// 순수 함수다. 네트워크도 시계도 건드리지 않는다.
// App Store 검증 불변식이 여기 전부 들어 있다.
func (v *Verifier) mapTransaction(
	tx *appstore.JWSTransaction,
	proof domain.Proof,
	observedAt time.Time,
) (domain.VerifiedPurchase, error) {
	// 다른 앱의 거래를 우리 앱 구매로 인정하면 안 된다.
	if tx.BundleID != "" && tx.BundleID != v.bundleID {
		return domain.VerifiedPurchase{}, platformerr.New(platformerr.CodeBundleMismatch,
			"다른 앱의 구매예요")
	}

	// 불변식 9. production과 sandbox를 자동으로 오가지 않는다.
	//
	// Apple 문서는 production에서 404가 나면 sandbox를 다시 호출하라고
	// 권하지만, 그러면 샌드박스 구매로 실제 지급을 받을 수 있다.
	// 환경은 배포 설정으로만 정한다.
	if tx.Environment != "" && tx.Environment != v.environment {
		return domain.VerifiedPurchase{}, platformerr.Newf(platformerr.CodeEnvironmentMismatch,
			"%s 환경 구매는 처리하지 않아요", tx.Environment)
	}

	// 요청이 상품을 지정했으면 대조한다. 지정하지 않았으면 응답을 따른다.
	if proof.ProductID != "" && tx.ProductID != proof.ProductID {
		return domain.VerifiedPurchase{}, platformerr.New(platformerr.CodeProductMismatch,
			"구매한 상품이 요청과 달라요")
	}

	expectedType, ok := expectedAppleType(proof.ProductType, tx.Type)
	if !ok || tx.Type != expectedType {
		return domain.VerifiedPurchase{}, platformerr.Newf(platformerr.CodeProductTypeMismatch,
			"%s 유형은 카탈로그 상품 유형과 일치하지 않아요", tx.Type)
	}
	if expectedType == appstore.Consumable && tx.Quantity > 1 {
		return domain.VerifiedPurchase{}, platformerr.New(platformerr.CodeProviderResponseInvalid,
			"여러 개를 한 번에 구매한 소모성 상품은 아직 처리할 수 없어요")
	}

	// 가족 공유로 받은 구매는 구매자 본인 것이 아니다.
	// FAMILY_SHARED를 지급하면 한 번 산 것으로 여러 계정이 받는다.
	if tx.InAppOwnershipType != "" && tx.InAppOwnershipType != ownershipPurchased {
		return domain.VerifiedPurchase{}, platformerr.New(platformerr.CodePurchaseInvalid,
			"본인이 구매한 상품이 아니에요")
	}

	// 비소모성은 복원에도 같은 originalTransactionId를 쓰고, 소모성은
	// 재구매 건마다 다른 transactionId를 써야 각각 한 번씩 지급된다.
	canonicalID := tx.OriginalTransactionId
	if expectedType == appstore.Consumable {
		canonicalID = tx.TransactionID
	}
	if canonicalID == "" {
		return domain.VerifiedPurchase{}, platformerr.New(platformerr.CodeProviderResponseInvalid,
			"App Store 거래 식별자가 없어요")
	}

	state := domain.StateActive
	completion := domain.CompletionAppleFinish

	// 환불·취소된 거래다. 지급하지 않고 회수한다.
	if tx.RevocationDate > 0 {
		state = domain.StateRevoked
		completion = domain.CompletionNone
	}

	return domain.VerifiedPurchase{
		Platform:          domain.PlatformAppStore,
		ProductID:         tx.ProductID,
		CanonicalID:       canonicalID,
		ProviderOrderID:   tx.TransactionID,
		PlatformAccountID: tx.AppAccountToken,
		PurchasedAt:       millisToTime(tx.PurchaseDate),
		ObservedAt:        observedAt,
		State:             state,
		Completion:        completion,
	}, nil
}

// expectedAppleType은 서버 카탈로그 유형을 Apple 유형으로 바꾼다.
//
// 빈 유형은 마켓 웹훅 재검증 경로다. 서명 검증된 Apple 거래가 지원하는
// 일회성 유형이면 그 유형을 따른다. 클라이언트 검증 경로는 서비스가 항상
// 카탈로그 유형을 명시적으로 주입한다.
func expectedAppleType(configured domain.ProductType, provider appstore.IAPType) (appstore.IAPType, bool) {
	switch configured {
	case domain.ProductNonConsumable:
		return appstore.NonConsumable, true
	case domain.ProductConsumable:
		return appstore.Consumable, true
	case "":
		switch provider {
		case appstore.NonConsumable, appstore.Consumable:
			return provider, true
		}
	}
	return "", false
}

// ownershipPurchased는 본인이 직접 산 구매다.
// 나머지는 FAMILY_SHARED다.
const ownershipPurchased = "PURCHASED"

// CompleteGrant는 finishTransaction을 호출한다.
//
// 실패해도 지급은 롤백하지 않는다. 불변식 7이다.
//
// 완료 처리에는 transactionId가 필요하다. originalTransactionId가
// 아니다. 그래서 ProviderOrderID를 쓴다.
func (v *Verifier) CompleteGrant(ctx context.Context, p domain.VerifiedPurchase) error {
	if p.Platform != domain.PlatformAppStore {
		return platformerr.New(platformerr.CodePlatformMismatch,
			"App Store 완료 처리에 다른 마켓 구매가 왔어요")
	}
	if p.Completion != domain.CompletionAppleFinish {
		return platformerr.New(platformerr.CodeCompletionMismatch,
			"완료 처리 방식이 올바르지 않아요")
	}

	txID := p.ProviderOrderID
	if txID == "" {
		// 복원 경로에서 transactionId를 잃었을 수 있다.
		// originalTransactionId로도 finishTransaction이 동작한다.
		txID = p.CanonicalID
	}
	if txID == "" {
		return platformerr.New(platformerr.CodeCompletionMismatch,
			"완료 처리할 거래 식별자가 없어요")
	}

	return v.src.Finish(ctx, txID)
}

// millisToTime은 Apple의 밀리초 타임스탬프를 시각으로 바꾼다.
func millisToTime(ms int64) time.Time {
	if ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}
