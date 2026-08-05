// Package ledger는 IAP entitlement 원장을 Firestore에 기록한다.
//
// 불변식 대부분이 여기서 실제로 강제된다. docs/03-architecture/iap.md 참고.
//
// 경로 prefix가 두 층이다. 배포 환경(staging)은 store가 붙이고,
// IAP 원장 환경(sandbox)은 여기서 붙인다. 둘은 독립적이다.
//
//	production 배포 + sandbox 원장 → iap_environments/sandbox/processed_orders/...
//	staging 배포  + sandbox 원장 → stg_iap_environments/sandbox/processed_orders/...
package ledger

import (
	"fmt"

	"github.com/seorilabs/platform/server/internal/fspath"
	"github.com/seorilabs/platform/server/internal/iap/domain"
)

// 컬렉션 이름.
//
// users는 클라이언트가 읽는 projection이고 iap_users가 내부 원장이다.
// 둘을 나누는 이유는 sources 맵에 마켓 계정 해시 같은 내부 정보가 있어
// 클라이언트에게 그대로 노출하면 안 되기 때문이다.
const (
	publicUsers   = "users"
	internalUsers = "iap_users"
	entitlements  = "entitlements"

	processedOrders         = "processed_orders"
	processedEvents         = "processed_iap_events"
	completionOutbx         = "iap_completion_outbox"
	operatorGrants          = "operator_entitlement_grants"
	operatorRevocations     = "operator_entitlement_revocations"
	sandboxResetRequests    = "sandbox_reset_requests"
	sandboxResetCompletions = "sandbox_reset_completions"
	sandboxResetClosures    = "sandbox_reset_closures"
	sandboxResetBarriers    = "sandbox_reset_barriers"
	ownershipTransfers      = "iap_ownership_transfers"
	adminMutationLimits     = "admin_mutation_limits"
	pendingRefundReviews    = "pending_refund_reviews"
	refundReviewDecisions   = "refund_review_decisions"
)

// pathBuilder는 환경 prefix를 붙여 경로를 만든다.
//
// 모든 경로 생성이 이 타입을 통과한다. 직접 문자열을 조립하면
// sandbox 데이터가 production에 섞일 수 있다.
type pathBuilder struct {
	prefix string
}

func newPathBuilder(env domain.Environment) pathBuilder {
	return pathBuilder{prefix: env.PathPrefix()}
}

func (b pathBuilder) parse(rel string) (fspath.Path, error) {
	return fspath.Parse(b.prefix + rel)
}

// publicEntitlement는 클라이언트가 읽는 projection 경로다.
func (b pathBuilder) publicEntitlement(puid, entID string) (fspath.Path, error) {
	return b.parse(publicUsers + "/" + puid + "/" + entitlements + "/" + entID)
}

// internalEntitlement는 sources를 담은 내부 원장 경로다.
func (b pathBuilder) internalEntitlement(puid, entID string) (fspath.Path, error) {
	return b.parse(internalUsers + "/" + puid + "/" + entitlements + "/" + entID)
}

// internalEntitlements는 한 사용자의 내부 원장 컬렉션이다.
func (b pathBuilder) internalEntitlements(puid string) (fspath.Path, error) {
	return b.parse(internalUsers + "/" + puid + "/" + entitlements)
}

// order는 주문 원장 경로다. 불변식 5에 따라 절대 삭제하지 않는다.
func (b pathBuilder) order(orderKey string) (fspath.Path, error) {
	return b.parse(processedOrders + "/" + orderKey)
}

func (b pathBuilder) orders() (fspath.Path, error) {
	return b.parse(processedOrders)
}

// event는 웹훅 이벤트 원장 경로다. 삭제하지 않는다.
func (b pathBuilder) event(eventKey string) (fspath.Path, error) {
	return b.parse(processedEvents + "/" + eventKey)
}

// outbox는 마켓 완료 처리 대기열이다.
//
// 원장 중 유일하게 삭제가 허용된다. 완료되거나 회수되면 지운다.
func (b pathBuilder) outbox(orderKey string) (fspath.Path, error) {
	return b.parse(completionOutbx + "/" + orderKey)
}

func (b pathBuilder) outboxes() (fspath.Path, error) {
	return b.parse(completionOutbx)
}

// operatorGrant는 운영자 지급 감사 원장이다. 영구 보존한다.
//
// 다른 원장과 같이 환경 prefix를 붙인다. 한때 환경과 무관하게
// 한 곳에 모으려 했지만 그러면 requestId 멱등이 환경을 가로지른다.
// sandbox에서 테스트로 쓴 requestId를 production에서 재사용하면
// 감사 기록이 이미 있다는 이유로 실제 보상이 건너뛰어진다.
// 사용자는 받아야 할 것을 못 받고, 로그에는 "이미 처리됨"만 남는다.
func (b pathBuilder) operatorGrant(requestID string) (fspath.Path, error) {
	return b.parse(operatorGrants + "/" + requestID)
}

func (b pathBuilder) operatorRevocation(requestID string) (fspath.Path, error) {
	return b.parse(operatorRevocations + "/" + requestID)
}

func (b pathBuilder) operatorCollection(collection string) (fspath.Path, error) {
	return b.parse(collection)
}

// sandboxResetRequest는 App Store sandbox 초기화의 immutable intent다.
func (b pathBuilder) sandboxResetRequest(requestID string) (fspath.Path, error) {
	return b.parse(sandboxResetRequests + "/" + requestID)
}

// sandboxResetCompletion은 초기화 결과의 immutable 완료 기록이다.
// intent와 분리해 prepared 상태를 잃지 않고 같은 requestId로 재개한다.
func (b pathBuilder) sandboxResetCompletion(requestID string) (fspath.Path, error) {
	return b.parse(sandboxResetCompletions + "/" + requestID)
}

// sandboxResetClosure는 상태 조회에서 intent 부재를 확인한 운영자가 같은
// requestId를 영구 종결했다는 PII-free create-only 기록이다.
func (b pathBuilder) sandboxResetClosure(requestID string) (fspath.Path, error) {
	return b.parse(sandboxResetClosures + "/" + requestID)
}

// sandboxResetBarrier는 sandbox App Store Grant와 reset을 사용자 단위로
// 직렬화하는 영구 coordination 문서다. reset 요청 이력은 별도 append-only
// request 문서에 남기고, 이 문서는 최신 cutoff와 revision만 유지한다.
func (b pathBuilder) sandboxResetBarrier(puid string) (fspath.Path, error) {
	return b.parse(sandboxResetBarriers + "/" + puid)
}

// ownershipTransfer는 소유권 이전의 append-only 복구 증거다.
// order별 sequence를 ID에 넣어 transaction 재시도에는 멱등이고, 같은 주문이
// 여러 번 이동해도 이전 증거를 덮어쓰지 않는다.
func (b pathBuilder) ownershipTransfer(orderKey string, sequence int64) (fspath.Path, error) {
	return b.parse(ownershipTransfers + "/" + fmt.Sprintf("%s-%020d", orderKey, sequence))
}

// adminMutationLimit는 인증된 OIDC principal별 durable rate gate다.
func (b pathBuilder) adminMutationLimit(principalHash string) (fspath.Path, error) {
	return b.parse(adminMutationLimits + "/" + principalHash)
}

// pendingRefundReview는 RTDN에서 받은 환불 검토 원장이다. 문서는 영구
// 보존하고 외부 호출용 암호문만 terminal 상태에서 제거한다. ADR 0014.
func (b pathBuilder) pendingRefundReview(reviewID string) (fspath.Path, error) {
	return b.parse(pendingRefundReviews + "/" + reviewID)
}

func (b pathBuilder) pendingRefundReviewCollection() (fspath.Path, error) {
	return b.parse(pendingRefundReviews)
}

// refundReviewDecision은 외부 호출 전에 확정하는 immutable 운영자 결정이다.
func (b pathBuilder) refundReviewDecision(requestID string) (fspath.Path, error) {
	return b.parse(refundReviewDecisions + "/" + requestID)
}
