// Package content는 private GCS 콘텐츠 릴리스를 인증된 사용자에게 선택 전달한다.
package content

import "time"

const SupportedSchemaVersion = 1

type Access string

const (
	AccessFree Access = "free"
	AccessDeep Access = "deep"
)

type Context string

const (
	ContextReading Context = "reading"
	ContextTerm    Context = "term"
	// ContextInternal은 릴리스 무결성에는 포함되지만 공개 selector나 사전
	// 경로로 전달하지 않는 보존 좌표다.
	ContextInternal Context = "internal"
)

type Item struct {
	ID       string    `json:"id"`
	Text     string    `json:"text"`
	Access   Access    `json:"access"`
	Contexts []Context `json:"contexts"`
}

func (i Item) HasContext(want Context) bool {
	for _, got := range i.Contexts {
		if got == want {
			return true
		}
	}
	return false
}

type Release struct {
	SchemaVersion  int
	ContentVersion string
	Items          map[string]Item
	LoadedAt       time.Time
}

type ContentVersion struct {
	SchemaVersion  int    `json:"schemaVersion"`
	ContentVersion string `json:"contentVersion"`
}

type Article struct {
	ID     string `json:"id"`
	Text   string `json:"text"`
	Access Access `json:"access"`
}

type ChartFacts struct {
	Year  string `json:"year"`
	Month string `json:"month"`
	Day   string `json:"day"`
	Hour  string `json:"hour,omitempty"`
}

type JohapFact struct {
	Sipseong string `json:"sipseong"`
	Unseong  string `json:"unseong"`
}

type SinsalFact struct {
	Name    string `json:"name"`
	Variant string `json:"variant,omitempty"`
}

type RelationFact struct {
	Kind string `json:"kind"`
	Pair string `json:"pair,omitempty"`
}

type FlowFact struct {
	Sipseong string `json:"sipseong"`
	State    string `json:"state"`
}

type SeunFacts struct {
	Year          int      `json:"year"`
	Flow          FlowFact `json:"flow"`
	DaeunSipseong []string `json:"daeunSipseong"`
	Samjae        string   `json:"samjae,omitempty"`
}

type DerivedReadingFacts struct {
	Kind      string         `json:"kind"`
	Chart     ChartFacts     `json:"chart"`
	Ilju      string         `json:"ilju"`
	Johap     []JohapFact    `json:"johap"`
	Sinsal    []SinsalFact   `json:"sinsal"`
	Relations []RelationFact `json:"relations"`
	Daeun     []FlowFact     `json:"daeun"`
	Seun      SeunFacts      `json:"seun"`
	Wolun     []FlowFact     `json:"wolun"`
}

type UnlockRequest struct {
	Section string `json:"section"`
	Kind    string `json:"kind"`
	ClaimID string `json:"claimId,omitempty"`
}

type ResolveRequest struct {
	SchemaVersion int                 `json:"schemaVersion"`
	Reading       DerivedReadingFacts `json:"reading"`
	Scope         []string            `json:"scope"`
	Unlock        *UnlockRequest      `json:"unlock,omitempty"`
}

type LockedDeep struct {
	DeepKey string `json:"deepKey"`
	Section string `json:"section"`
	Year    int    `json:"year"`
}

type ResolveResult struct {
	SchemaVersion  int          `json:"schemaVersion"`
	ContentVersion string       `json:"contentVersion"`
	ReadingKey     string       `json:"readingKey"`
	Articles       []Article    `json:"articles"`
	Locked         []LockedDeep `json:"locked"`
}

type TermResult struct {
	SchemaVersion  int     `json:"schemaVersion"`
	ContentVersion string  `json:"contentVersion"`
	Article        Article `json:"article"`
}

// TicketBalance는 남은 열람권이다.
//
// UnitsPerPurchase를 함께 주는 것은 화면이 "5개 중 2개 남음"처럼 분모를
// 보여줄 수 있게 하기 위해서다. 앱이 이 값을 상수로 박으면 레지스트리를
// 고쳤을 때 화면만 옛 숫자로 남는다.
type TicketBalance struct {
	EntitlementID    string `json:"entitlementId"`
	Remaining        int    `json:"remaining"`
	UnitsPerPurchase int    `json:"unitsPerPurchase"`
}

// DeepUnlock은 이미 열린 심화 항목 하나의 응답 형태다.
//
// Year는 deepKey에서 뽑는다. 앱이 문자열을 다시 파싱하지 않게 한다.
type DeepUnlock struct {
	ReadingKey string `json:"readingKey"`
	DeepKey    string `json:"deepKey"`
	Year       int    `json:"year,omitempty"`
	Source     string `json:"source"`
	UnlockedAt string `json:"unlockedAt"`
}

// DeepAccessResult는 심화 열람 현황 응답이다.
type DeepAccessResult struct {
	Ticket  *TicketBalance `json:"ticket,omitempty"`
	Unlocks []DeepUnlock   `json:"unlocks"`
}

// DeepAccess는 유스케이스 반환값이다. HTTP 표현과 분리해 둔다.
type DeepAccess struct {
	Ticket  *TicketBalance
	Unlocks []UnlockRecord
}
