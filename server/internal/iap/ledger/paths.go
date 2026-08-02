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

	processedOrders = "processed_orders"
	processedEvents = "processed_iap_events"
	completionOutbx = "iap_completion_outbox"
	refundReviews   = "pending_refund_reviews"

	operatorGrants       = "operator_entitlement_grants"
	operatorRevocations  = "operator_entitlement_revocations"
	sandboxResetRequests = "sandbox_reset_requests"
	adminMutationLimits  = "admin_mutation_limits"
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

// refundReview는 Play 환불 검토 대기 원장이다.
//
// 자동으로 응답하지 않고 기록만 한다. 24시간 안에 사람이 판단해야 한다.
func (b pathBuilder) refundReview(tokenHash string) (fspath.Path, error) {
	return b.parse(refundReviews + "/" + tokenHash)
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

// operatorRecord는 컬렉션 이름으로 감사 원장 경로를 만든다.
//
// 지급과 회수가 같은 흐름을 타므로 컬렉션만 갈아끼운다.
func (b pathBuilder) operatorRecord(collection, requestID string) (fspath.Path, error) {
	return b.parse(collection + "/" + requestID)
}

func (b pathBuilder) operatorCollection(collection string) (fspath.Path, error) {
	return b.parse(collection)
}

// sandboxResetRequest는 App Store sandbox 초기화 요청의 영구 멱등 기록이다.
func (b pathBuilder) sandboxResetRequest(requestID string) (fspath.Path, error) {
	return b.parse(sandboxResetRequests + "/" + requestID)
}

// adminMutationLimit는 인증된 OIDC principal별 durable rate gate다.
func (b pathBuilder) adminMutationLimit(principalHash string) (fspath.Path, error) {
	return b.parse(adminMutationLimits + "/" + principalHash)
}
