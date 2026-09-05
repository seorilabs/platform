// Package ads는 광고 정책과 보상 claim 유스케이스를 제공한다.
package ads

import "time"

type ClaimState string
type Assurance string

const (
	StateAccepted  ClaimState = "accepted"
	StateConfirmed ClaimState = "confirmed"
	StateDelivered ClaimState = "delivered"
	StateExpired   ClaimState = "expired"

	AssurancePending         Assurance = "pending"
	AssuranceServerVerified  Assurance = "server_verified"
	AssuranceClientConfirmed Assurance = "client_confirmed"
)

// 지금 서버가 아는 보상 provider.
const (
	ProviderAdMob      = "admob"
	ProviderAppsInToss = "apps_in_toss"
)

// settledAssurance는 provider마다 「보상이 확정됐다」로 인정하는 수준이다.
//
// **서로의 것을 인정하지 않는다.** AdMob은 서버가 Google SSV 서명을 검증한
// server_verified만 확정이다. AppsInToss에는 SSV 자체가 없어서 서버가 지면의 일일
// 한도와 cooldown을 원자적으로 걸고 받은 client_confirmed가 확정이고, 그 위로
// 승격되는 경로가 없다.
//
// 한쪽 기준을 양쪽에 쓰면 둘 중 하나가 깨진다 — AdMob 기준을 AIT에 쓰면 확정된
// 보상이 영원히 거절되고, 반대로 하면 SSV 없는 보상이 AdMob 경로를 통과한다.
var settledAssurance = map[string]Assurance{
	ProviderAdMob:      AssuranceServerVerified,
	ProviderAppsInToss: AssuranceClientConfirmed,
}

// SettledClaim은 보상이 그 provider의 기준으로 확정됐는지 답한다.
//
// 보상을 값으로 바꾸는 쪽(콘텐츠 해제)이 판정을 스스로 적지 않고 이것을 부르게 해서,
// provider가 늘어날 때 기준이 두 곳으로 갈라지지 않게 한다.
func SettledClaim(claim Claim) bool {
	settled, known := settledAssurance[claim.Provider]
	if !known {
		return false
	}
	return claim.Assurance == settled &&
		(claim.State == StateConfirmed || claim.State == StateDelivered)
}

type Reward struct {
	Key    string `json:"key" firestore:"key"`
	Amount int    `json:"amount" firestore:"amount"`
}

type Claim struct {
	ClaimID         string     `json:"claimId" firestore:"claimId"`
	RequestID       string     `json:"-" firestore:"requestId"`
	AppID           string     `json:"appId" firestore:"appId"`
	PlatformUserID  string     `json:"-" firestore:"platformUserId"`
	SupportCode     string     `json:"-" firestore:"supportCode"`
	PlacementID     string     `json:"placement" firestore:"placement"`
	Provider        string     `json:"provider" firestore:"provider"`
	ClientPlatform  string     `json:"clientPlatform" firestore:"clientPlatform"`
	Reward          Reward     `json:"reward" firestore:"reward"`
	State           ClaimState `json:"state" firestore:"state"`
	Assurance       Assurance  `json:"assurance" firestore:"assurance"`
	TransactionHash string     `json:"-" firestore:"transactionHash,omitempty"`
	CreatedAt       time.Time  `json:"createdAt" firestore:"createdAt"`
	ConfirmedAt     *time.Time `json:"confirmedAt,omitempty" firestore:"confirmedAt,omitempty"`
	AcknowledgedAt  *time.Time `json:"acknowledgedAt,omitempty" firestore:"acknowledgedAt,omitempty"`
	ExpiresAt       time.Time  `json:"expiresAt" firestore:"expiresAt"`
	TTLAt           time.Time  `json:"-" firestore:"ttlAt"`
}

type Policy struct {
	AppUsesAds bool      `json:"appUsesAds"`
	AdsEnabled bool      `json:"adsEnabled"`
	DisabledBy []string  `json:"disabledBy"`
	CheckedAt  time.Time `json:"checkedAt"`
}

type SuppressionRecord struct {
	RequestID      string    `json:"requestId" firestore:"requestId"`
	GrantRequestID string    `json:"grantRequestId,omitempty" firestore:"grantRequestId,omitempty"`
	AppID          string    `json:"appId" firestore:"appId"`
	PlatformUserID string    `json:"platformUserId" firestore:"platformUserId"`
	ActorLogin     string    `json:"actorLogin" firestore:"actorLogin"`
	Reason         string    `json:"reason" firestore:"reason"`
	Operation      string    `json:"operation" firestore:"operation"`
	Applied        bool      `json:"applied" firestore:"applied"`
	CreatedAt      time.Time `json:"createdAt" firestore:"createdAt"`
}

type SuppressionResult struct {
	Applied              bool   `json:"applied"`
	RequestID            string `json:"requestId"`
	ActiveGrantRequestID string `json:"activeGrantRequestId,omitempty"`
}

type UserAds struct {
	AppID          string              `json:"appId"`
	PlatformUserID string              `json:"platformUserId"`
	SupportCode    string              `json:"supportCode"`
	IsAnonymous    bool                `json:"isAnonymous"`
	AuthType       string              `json:"authType"`
	LastSeenAt     time.Time           `json:"lastSeenAt"`
	Policy         Policy              `json:"policy"`
	AuditHistory   []SuppressionRecord `json:"auditHistory"`
}

type Health struct {
	Status                string     `json:"status"`
	LastSSVSuccessAt      *time.Time `json:"lastSsvSuccessAt,omitempty"`
	InvalidSignatureCount int64      `json:"invalidSignatureCount"`
	StalePendingCount     int64      `json:"stalePendingClaimCount"`
	PolicyFailureCount    int64      `json:"policyFailureCount"`
	CheckedAt             time.Time  `json:"checkedAt"`
}

type SSVEvent string

const (
	SSVCallbackSuccess  SSVEvent = "callback_success"
	SSVProbeSuccess     SSVEvent = "probe_success"
	SSVSignatureInvalid SSVEvent = "signature_invalid"
)

// AppHealth는 공통 Ads 서비스에서 한 앱의 callback 상태만 분리해 보여준다.
// Google Console probe와 실제 보상 callback을 구분해야 probe 성공을
// 실사용 보상 성공으로 오해하지 않는다.
type AppHealth struct {
	AppID                 string     `json:"appId"`
	Status                string     `json:"status"`
	LastCallbackSuccessAt *time.Time `json:"lastCallbackSuccessAt,omitempty"`
	LastProbeSuccessAt    *time.Time `json:"lastProbeSuccessAt,omitempty"`
	InvalidSignatureCount int64      `json:"invalidSignatureCount"`
	StalePendingCount     int64      `json:"stalePendingClaimCount"`
	PolicyFailureCount    int64      `json:"policyFailureCount"`
	CheckedAt             time.Time  `json:"checkedAt"`
}

type ClaimFilter struct {
	AppID, Provider, State, Assurance, Placement, Reference string
	Limit                                                   int
}

type ConfirmInput struct {
	ClaimID, AppID, PlatformUserID, Provider, TransactionHash string
	Assurance                                                 Assurance
	Now                                                       time.Time
	DailyLimit, CooldownSeconds                               int
}

type SSVResult struct {
	AdNetworkID, AdUnitID, ClaimID, TransactionID, PlatformUserID, RewardItem string
	RewardAmount                                                              int
	Timestamp                                                                 time.Time
}
