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
