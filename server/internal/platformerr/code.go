package platformerr

import "net/http"

// Code는 클라이언트가 분기하는 기계 판독용 에러 코드다.
//
// 클라이언트는 Message가 아니라 이 값으로 분기한다.
// 메시지는 언제든 바뀔 수 있지만 코드는 계약이다.
type Code string

// 요청 형식
const (
	CodeRequestInvalid     Code = "request_invalid"
	CodeRequestTooLarge    Code = "request_too_large"
	CodeContentTypeInvalid Code = "content_type_invalid"
	CodeMethodNotAllowed   Code = "method_not_allowed"
	CodePlatformInvalid    Code = "platform_invalid"
	CodePlatformMismatch   Code = "platform_mismatch"
	CodeProofInvalid       Code = "purchase_proof_invalid"
)

// 인증과 세션
const (
	CodeAuthRequired Code = "auth_required"
	CodeAuthInvalid  Code = "auth_invalid"
	// CodeAuthForbidden은 신원은 확인됐지만 권한이 없을 때다.
	//
	// auth_invalid와 나눈다. 백오피스가 "토큰이 잘못됐다"와
	// "이 계정은 못 부른다"를 구분해 대응해야 한다.
	CodeAuthForbidden       Code = "auth_forbidden"
	CodeSessionInvalid      Code = "session_invalid"
	CodeSessionExpired      Code = "session_expired"
	CodeRefreshInvalid      Code = "refresh_invalid"
	CodeAnonymousNotAllowed Code = "anonymous_not_allowed"
)

// App Check
//
// 앱별 require_app_check가 켜진 경로에서 사용한다. Go Admin SDK는
// 표준 token 검증을 지원하지만 consume 기반 replay 방지는 지원하지
// 않으므로, 일회성 보호가 필요한 IAP 경로는 별도 경계를 유지한다.
const (
	CodeAppCheckRequired    Code = "app_check_required"
	CodeAppCheckInvalid     Code = "app_check_invalid"
	CodeAppCheckReplayed    Code = "app_check_replayed"
	CodeAppCheckAppMismatch Code = "app_check_app_mismatch"
)

// 앱 레지스트리
const (
	CodeAppUnknown   Code = "app_unknown"
	CodeAppPaused    Code = "app_paused"
	CodeUserBlocked  Code = "user_blocked"
	CodeUserNotFound Code = "user_not_found"
)

// 한도
const (
	CodeRateLimited Code = "rate_limited"
)

// 계정 바인딩
const (
	CodeAccountBindingMissing  Code = "account_binding_missing"
	CodeAccountBindingMismatch Code = "account_binding_mismatch"
)

// 상품과 카탈로그
const (
	CodeProductNotAllowed   Code = "product_not_allowed"
	CodeProductMismatch     Code = "product_mismatch"
	CodeProductTypeMismatch Code = "product_type_mismatch"
	CodeBundleMismatch      Code = "bundle_mismatch"
	CodeEnvironmentMismatch Code = "environment_mismatch"
	CodeTransactionMismatch Code = "transaction_mismatch"
	CodeCatalogInvalid      Code = "catalog_invalid"
	CodeCatalogDuplicate    Code = "catalog_duplicate"
	CodeCatalogUnavailable  Code = "catalog_unavailable"
	CodeCatalogIncomplete   Code = "catalog_incomplete"
)

// 구매 상태 — 불변식 2·3·4와 직접 연결된다
const (
	CodePurchaseInvalid            Code = "purchase_invalid"
	CodePurchaseNotFound           Code = "purchase_not_found"
	CodePurchaseOwnedByAnotherUser Code = "purchase_owned_by_another_user" // 불변식 4
	CodePurchaseReplayMismatch     Code = "purchase_replay_mismatch"
	CodeOperatorReplayMismatch     Code = "operator_replay_mismatch"
	CodeSandboxResetBusy           Code = "sandbox_reset_busy"
	CodeSandboxResetPending        Code = "sandbox_reset_pending"
	CodeSandboxResetNotFound       Code = "sandbox_reset_not_found"
	CodeSandboxResetClosed         Code = "sandbox_reset_closed"
	CodeSandboxResetAlreadyStarted Code = "sandbox_reset_already_started"
)

// 웹훅 이벤트
const (
	CodeNotificationInvalid        Code = "notification_invalid"
	CodePackageMismatch            Code = "package_mismatch"
	CodePartialRefundUnsupported   Code = "partial_refund_unsupported"
	CodeEventBusy                  Code = "event_busy"
	CodeEventReplayMismatch        Code = "event_replay_mismatch"
	CodeEventClaimLost             Code = "event_claim_lost"
	CodeEventCommitFailed          Code = "event_commit_failed"
	CodeRefundReviewReplayMismatch Code = "refund_review_replay_mismatch"
	CodeRefundReviewNotFound       Code = "refund_review_not_found"
	CodeRefundReviewAlreadyDecided Code = "refund_review_already_decided"
	CodeRefundReviewExpired        Code = "refund_review_expired"
	CodeRefundReviewClaimLost      Code = "refund_review_claim_lost"
)

// 마켓 완료 처리 — 불변식 7과 연결된다
const (
	CodeProviderCompletionPending Code = "provider_completion_pending"
	CodeCompletionMismatch        Code = "completion_mismatch"
	CodeCompletionReplayMismatch  Code = "completion_replay_mismatch"
	CodeCompletionClaimLost       Code = "completion_claim_lost"
)

// 외부 마켓 provider
const (
	CodeProviderAuthFailed      Code = "provider_auth_failed"
	CodeProviderUnavailable     Code = "provider_unavailable"
	CodeProviderResponseInvalid Code = "provider_response_invalid"
	CodeProviderTimeout         Code = "provider_timeout"
	CodeProviderConfigInvalid   Code = "provider_config_invalid"
	CodePlatformUnavailable     Code = "platform_unavailable"
)

// 런타임 설정
const (
	CodeRuntimeConfigInvalid Code = "runtime_config_invalid"
	CodeSecretConfigInvalid  Code = "secret_config_invalid"
	CodeLedgerStateInvalid   Code = "ledger_state_invalid"
	CodeConfigUnavailable    Code = "config_unavailable"
)

// 이벤트 수집
const (
	CodeEventBatchTooLarge Code = "event_batch_too_large"
)

// 내부
const (
	CodeInternal Code = "internal"
)

// statusByCode가 코드와 HTTP 상태의 단일 대응표다.
//
// 새 코드를 추가하면 여기 등록해야 한다. 빠뜨리면 statusOf가 500을 준다.
// 한 곳에 모으는 이유가 이것이다 — 누락이 조용히 새는 걸 줄인다.
//
// 상황에 따라 상태가 달라지는 코드는 여기 기본값을 두고
// 호출부에서 WithStatus로 덮어쓴다. platform_mismatch가 그 예로,
// 클라이언트 실수면 400이지만 서버 조립 실수면 500이다.
var statusByCode = map[Code]int{
	// 요청 형식
	CodeRequestInvalid:     http.StatusBadRequest,
	CodeRequestTooLarge:    http.StatusRequestEntityTooLarge,
	CodeContentTypeInvalid: http.StatusUnsupportedMediaType,
	CodeMethodNotAllowed:   http.StatusMethodNotAllowed,
	CodePlatformInvalid:    http.StatusBadRequest,
	CodePlatformMismatch:   http.StatusBadRequest,
	CodeProofInvalid:       http.StatusBadRequest,

	// 인증과 세션
	CodeAuthRequired:        http.StatusUnauthorized,
	CodeAuthInvalid:         http.StatusUnauthorized,
	CodeAuthForbidden:       http.StatusForbidden,
	CodeSessionInvalid:      http.StatusUnauthorized,
	CodeSessionExpired:      http.StatusUnauthorized,
	CodeRefreshInvalid:      http.StatusUnauthorized,
	CodeAnonymousNotAllowed: http.StatusForbidden,

	// App Check
	CodeAppCheckRequired:    http.StatusUnauthorized,
	CodeAppCheckInvalid:     http.StatusUnauthorized,
	CodeAppCheckReplayed:    http.StatusUnauthorized,
	CodeAppCheckAppMismatch: http.StatusForbidden,

	// 앱 레지스트리
	CodeAppUnknown:   http.StatusForbidden,
	CodeAppPaused:    http.StatusForbidden,
	CodeUserBlocked:  http.StatusForbidden,
	CodeUserNotFound: http.StatusNotFound,

	// 한도
	CodeRateLimited: http.StatusTooManyRequests,

	// 계정 바인딩
	CodeAccountBindingMissing:  http.StatusUnprocessableEntity,
	CodeAccountBindingMismatch: http.StatusConflict,

	// 상품과 카탈로그
	CodeProductNotAllowed:   http.StatusUnprocessableEntity,
	CodeProductMismatch:     http.StatusUnprocessableEntity,
	CodeProductTypeMismatch: http.StatusUnprocessableEntity,
	CodeBundleMismatch:      http.StatusUnprocessableEntity,
	CodeEnvironmentMismatch: http.StatusUnprocessableEntity,
	CodeTransactionMismatch: http.StatusUnprocessableEntity,
	CodeCatalogInvalid:      http.StatusInternalServerError,
	CodeCatalogDuplicate:    http.StatusInternalServerError,
	CodeCatalogUnavailable:  http.StatusServiceUnavailable,
	CodeCatalogIncomplete:   http.StatusServiceUnavailable,

	// 구매 상태
	CodePurchaseInvalid:            http.StatusUnprocessableEntity,
	CodePurchaseNotFound:           http.StatusUnprocessableEntity,
	CodePurchaseOwnedByAnotherUser: http.StatusConflict,
	CodePurchaseReplayMismatch:     http.StatusConflict,
	CodeOperatorReplayMismatch:     http.StatusConflict,
	CodeSandboxResetBusy:           http.StatusConflict,
	CodeSandboxResetPending:        http.StatusServiceUnavailable,
	CodeSandboxResetNotFound:       http.StatusNotFound,
	CodeSandboxResetClosed:         http.StatusConflict,
	CodeSandboxResetAlreadyStarted: http.StatusConflict,

	// 웹훅 이벤트
	CodeNotificationInvalid:        http.StatusUnauthorized,
	CodePackageMismatch:            http.StatusUnprocessableEntity,
	CodePartialRefundUnsupported:   http.StatusUnprocessableEntity,
	CodeEventBusy:                  http.StatusServiceUnavailable,
	CodeEventReplayMismatch:        http.StatusConflict,
	CodeEventClaimLost:             http.StatusConflict,
	CodeEventCommitFailed:          http.StatusServiceUnavailable,
	CodeRefundReviewReplayMismatch: http.StatusConflict,
	CodeRefundReviewNotFound:       http.StatusNotFound,
	CodeRefundReviewAlreadyDecided: http.StatusConflict,
	CodeRefundReviewExpired:        http.StatusConflict,
	CodeRefundReviewClaimLost:      http.StatusConflict,

	// 마켓 완료 처리
	CodeProviderCompletionPending: http.StatusServiceUnavailable,
	CodeCompletionMismatch:        http.StatusInternalServerError,
	CodeCompletionReplayMismatch:  http.StatusConflict,
	CodeCompletionClaimLost:       http.StatusConflict,

	// 외부 마켓 provider
	CodeProviderAuthFailed:      http.StatusBadGateway,
	CodeProviderUnavailable:     http.StatusBadGateway,
	CodeProviderResponseInvalid: http.StatusBadGateway,
	CodeProviderTimeout:         http.StatusServiceUnavailable,
	CodeProviderConfigInvalid:   http.StatusInternalServerError,
	CodePlatformUnavailable:     http.StatusServiceUnavailable,

	// 런타임 설정
	CodeRuntimeConfigInvalid: http.StatusServiceUnavailable,
	CodeSecretConfigInvalid:  http.StatusServiceUnavailable,
	CodeLedgerStateInvalid:   http.StatusInternalServerError,
	CodeConfigUnavailable:    http.StatusServiceUnavailable,

	// 이벤트 수집
	CodeEventBatchTooLarge: http.StatusBadRequest,

	// 내부
	CodeInternal: http.StatusInternalServerError,
}

// statusOf는 코드에 대응하는 HTTP 상태를 돌려준다.
// 등록되지 않은 코드는 500이다.
func statusOf(c Code) int {
	if s, ok := statusByCode[c]; ok {
		return s
	}
	return http.StatusInternalServerError
}

// IsRetryable은 같은 요청을 다시 보내면 성공할 가능성이 있는지 판정한다.
//
// 웹훅 처리와 완료 outbox 재시도가 이 판정으로 lease를 풀지 결정한다.
// 4xx는 다시 보내도 결과가 같으므로 재시도하지 않는다.
func IsRetryable(c Code) bool {
	switch c {
	case CodeEventBusy,
		CodeEventCommitFailed,
		CodeEventClaimLost,
		CodePurchaseNotFound, // 마켓 반영 지연일 수 있다
		CodeProviderUnavailable,
		CodeProviderTimeout,
		CodeProviderCompletionPending,
		CodePlatformUnavailable,
		CodeCatalogUnavailable,
		CodeConfigUnavailable,
		CodeRuntimeConfigInvalid,
		CodeSecretConfigInvalid,
		CodeSandboxResetPending:
		return true
	default:
		return false
	}
}
