// Package domain은 IAP 값 타입과 불변식을 담는다.
//
// 이 패키지는 표준 라이브러리와 platformerr 외에 아무것도 import하지 않는다.
// Firestore도 HTTP도 마켓 SDK도 모른다.
//
// 인터페이스는 여기 두지 않는다. 호출하는 쪽(iap/verify)에 정의한다.
// Go 관용구이면서 원본 domain.ts와 값 타입 대조도 가능하게 한 절충이다.
// docs/03-architecture/server-layout.md 참고.
package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"time"
)

// Platform은 마켓이다.
type Platform string

const (
	PlatformGooglePlay Platform = "google_play"
	PlatformAppStore   Platform = "app_store"
	PlatformAppsInToss Platform = "apps_in_toss"
	// PlatformOperator는 백오피스 운영자 지급이다.
	// 마켓이 아니지만 같은 원장에 source로 들어간다.
	PlatformOperator Platform = "operator"
)

// Valid는 알려진 마켓인지 본다.
func (p Platform) Valid() bool {
	switch p {
	case PlatformGooglePlay, PlatformAppStore, PlatformAppsInToss, PlatformOperator:
		return true
	default:
		return false
	}
}

// State는 구매 상태다.
type State string

const (
	StateActive  State = "active"
	StatePending State = "pending"
	StateRevoked State = "revoked"
	StateInvalid State = "invalid"
)

// Rank는 상태 우선순위다. 불변식 3의 stale 억제에 쓴다.
//
// revoked(3) > active(2) > pending(1)
//
// 늦게 도착한 grant가 이미 처리된 환불을 되돌리면 안 된다.
// 마켓 웹훅과 클라이언트 검증이 순서 없이 도착하기 때문에 필요하다.
func (s State) Rank() int {
	switch s {
	case StateRevoked:
		return 3
	case StateActive:
		return 2
	case StatePending:
		return 1
	default:
		return 0
	}
}

// Completion은 마켓 쪽 완료 처리 방식이다.
type Completion string

const (
	// CompletionNone은 완료 처리가 필요 없다.
	CompletionNone Completion = "none"
	// CompletionGoogleAcknowledge는 Play acknowledge가 필요하다.
	CompletionGoogleAcknowledge Completion = "google_acknowledge"
	// CompletionAppleFinish는 App Store finishTransaction이 필요하다.
	CompletionAppleFinish Completion = "apple_finish"
	// CompletionAppsInTossClient는 클라이언트가 completeProductGrant를 부른다.
	CompletionAppsInTossClient Completion = "apps_in_toss_client_complete"
)

// CompletionAction은 클라이언트에게 알려줄 후속 조치다.
type CompletionAction string

const (
	ActionNone CompletionAction = "none"
	// ActionRetryServerCompletion은 지급이 이미 커밋됐고 마켓 완료만 실패했다는 뜻이다.
	//
	// 불변식 7이다. 지급을 롤백하지 않는다.
	// 반대로 하면 "돈은 나갔는데 물건이 없다"가 된다.
	ActionRetryServerCompletion CompletionAction = "retry_server_completion"
	// ActionAppsInTossCompleteGrant는 AIT 클라이언트가 completeProductGrant를 불러야 한다.
	ActionAppsInTossCompleteGrant CompletionAction = "apps_in_toss_complete_product_grant"
	// ActionAppStoreSyncAfterSandboxReset은 sandbox 초기화 후 재동기화가 필요하다.
	ActionAppStoreSyncAfterSandboxReset CompletionAction = "app_store_sync_after_sandbox_reset"
)

// Proof는 클라이언트가 제시한 구매 증명이다.
type Proof struct {
	Platform  Platform
	ProductID string
	// Token은 마켓별 증명값이다.
	//   Play:      purchaseToken
	//   App Store: transactionId
	//   AIT:       orderId
	Token string
	// AITAccountHash는 AIT 전용 계정 해시다. body가 아니라 검증된
	// appLogin 세션에서만 온다. 원본 userKey는 저장하거나 세션에 싣지 않는다.
	AITAccountHash string
}

// VerifiedPurchase는 마켓 검증을 통과한 구매다.
type VerifiedPurchase struct {
	Platform  Platform
	ProductID string

	// CanonicalID는 멱등키의 재료다. 마켓마다 다르다.
	//   Play:      purchaseToken
	//   App Store: originalTransactionId — transactionId가 아니다
	//   AIT:       orderId
	CanonicalID string

	// ProviderOrderID는 마켓 쪽 주문 식별자다. 진단과 완료 처리에 쓴다.
	ProviderOrderID string

	// PlatformAccountID는 마켓 계정 참조다.
	// 저장할 때는 원문이 아니라 sha256만 남긴다. ADR 0005.
	PlatformAccountID string

	PurchasedAt time.Time
	ObservedAt  time.Time
	State       State
	Completion  Completion
}

// OrderKey는 주문 멱등키다. 불변식 1이다.
//
//	sha256("{platform}:{canonicalId}")
//
// 클라이언트 생성 ID를 쓰지 않는다. 마켓이 준 값만이 신뢰 가능하다.
//
// 이 형식은 이론적으로 모호하다. platform이 "a:b"이고 canonicalId가 "c"인
// 경우와 platform이 "a"이고 canonicalId가 "b:c"인 경우가 같은 문자열이 된다.
// 길이 접두사를 넣으면 없앨 수 있지만 불변식 1의 형식이 바뀌므로 하지 않는다.
//
// 대신 전제를 강제한다. Platform은 Valid()를 통과한 고정 enum이고
// 어느 값에도 콜론이 없다. 원장 진입점이 이를 검사하므로 모호성이 발생하지 않는다.
// PlatformsAreColonFree 테스트가 이 전제를 지킨다.
func OrderKey(p Platform, canonicalID string) string {
	sum := sha256.Sum256([]byte(string(p) + ":" + canonicalID))
	return hex.EncodeToString(sum[:])
}

// AllPlatforms는 원장에 source로 들어올 수 있는 모든 값이다.
// 운영자 지급을 포함한다.
func AllPlatforms() []Platform {
	return []Platform{
		PlatformGooglePlay,
		PlatformAppStore,
		PlatformAppsInToss,
		PlatformOperator,
	}
}

// MarketPlatforms는 외부 마켓 검증이 필요한 것들이다.
//
// AllPlatforms와 다르다. 운영자 지급은 백오피스가 근거를 갖고
// 원장에 직접 쓰므로 마켓에 물어볼 것이 없다.
// 검증기 조립과 카탈로그 검사는 이쪽을 기준으로 삼는다.
func MarketPlatforms() []Platform {
	return []Platform{
		PlatformGooglePlay,
		PlatformAppStore,
		PlatformAppsInToss,
	}
}

// IsMarket은 외부 마켓 검증이 필요한 값인지 본다.
func (p Platform) IsMarket() bool {
	switch p {
	case PlatformGooglePlay, PlatformAppStore, PlatformAppsInToss:
		return true
	default:
		return false
	}
}

// Key는 이 구매의 orderKey다.
func (v VerifiedPurchase) Key() string {
	return OrderKey(v.Platform, v.CanonicalID)
}

// HashAccountID는 마켓 계정 참조를 해시한다.
//
// 원문을 저장하지 않는다. 유출 시 피해가 크고 동일성 비교는 해시로도 된다.
func HashAccountID(s string) string {
	if s == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// Source는 entitlement를 뒷받침하는 근거 하나다.
//
// 한 entitlement에 여러 source가 붙을 수 있다. 예를 들어 유저가 환불받은
// 구매와 운영자가 보상으로 준 지급이 함께 있을 수 있다.
type Source struct {
	Platform    Platform  `firestore:"platform"`
	ProductID   string    `firestore:"productId"`
	State       State     `firestore:"state"`
	PurchasedAt time.Time `firestore:"purchasedAt"`
	ObservedAt  time.Time `firestore:"observedAt"`
	UpdatedAt   time.Time `firestore:"updatedAt"`

	// 운영자 지급 전용 필드. 마켓 구매면 비어 있다.
	ActorLogin string `firestore:"actorLogin,omitempty"`
	Reason     string `firestore:"reason,omitempty"`
}

// IsActiveFrom은 sources 맵에서 entitlement 활성 여부를 계산한다. 불변식 6이다.
//
//	active = 하나라도 state == "active"
//
// 트랜잭션 안에서 재계산해 내부 원장과 공개 projection에 동시에 쓴다.
// 한쪽만 갱신되면 클라이언트가 보는 값과 원장이 어긋난다.
func IsActiveFrom(sources map[string]Source) bool {
	for _, s := range sources {
		if s.State == StateActive {
			return true
		}
	}
	return false
}

// IsStaleUpdate는 들어온 갱신이 이미 반영된 것보다 낡았는지 본다. 불변식 3이다.
//
// observedAt이 더 이르면 무시한다. 같은 시각이면 랭크가 낮은 전이를 무시한다.
// 이 규칙이 "늦게 끝난 grant가 환불을 되돌리는" 사고를 막는다.
func IsStaleUpdate(existing, incoming State, existingAt, incomingAt time.Time) bool {
	if incomingAt.Before(existingAt) {
		return true
	}
	if incomingAt.Equal(existingAt) && incoming.Rank() < existing.Rank() {
		return true
	}
	return false
}

// GrantResult는 지급 결과다.
type GrantResult struct {
	// Granted와 AlreadyGranted는 항상 배타적이다. 불변식 2다.
	// 둘 다 false인 조합은 존재하지 않는다. 클라이언트가 이 전제로 분기한다.
	Granted        bool
	AlreadyGranted bool

	EntitlementID string
	Entitlements  []string

	// BlockedBySandboxReset은 sandbox 초기화로 재지급이 차단된 경우다.
	BlockedBySandboxReset bool

	// TransferredFrom은 같은 마켓 계정의 다른 platform_user에게서
	// 이 구매를 옮겨왔을 때 그 이전 소유자다. 비어 있으면 이전이 없었다.
	//
	// 감사 원장에 남기려고 돌려준다. 되돌릴 수 없는 조작이라
	// 누가 누구에게서 무엇을 옮겼는지가 남아야 한다.
	TransferredFrom string
}

// Valid는 불변식 2를 확인한다.
//
// 원장 구현이 이 조건을 깨지 않는지 테스트와 런타임에서 함께 검사한다.
func (r GrantResult) Valid() bool {
	// 초기화 차단은 지급도 중복 지급도 아니다.
	// 이때만 둘 다 false가 옳고, 그 밖에는 배타적이어야 한다.
	if r.BlockedBySandboxReset {
		return !r.Granted && !r.AlreadyGranted
	}
	return r.Granted != r.AlreadyGranted
}

// Environment는 원장 환경이다.
//
// production과 sandbox를 자동으로 넘나들지 않는다. 불변식 9다.
type Environment string

const (
	EnvProduction Environment = "production"
	EnvSandbox    Environment = "sandbox"
)

// PathPrefix는 이 환경의 Firestore 경로 접두사다.
//
// production은 접두사가 없고 sandbox만 붙는다.
// 모든 경로 생성이 이 함수를 통과해야 환경이 섞이지 않는다.
func (e Environment) PathPrefix() string {
	if e == EnvSandbox {
		return "iap_environments/sandbox/"
	}
	return ""
}
