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
	ID   string `json:"id"`
	Text string `json:"text"`
	// More는 같은 좌표의 두 번째 본문이다. 리딩 본문(Text)이 짧은 압축본일 때 앱이
	// `찬찬히 읽기` 접힘과 사전 본문으로 쓰는 원문이다. 없으면 생략한다 — 선택과
	// 권한 판정은 Text 와 같은 항목에서 이미 끝났으므로 여기서 다시 보지 않는다.
	More     string    `json:"more,omitempty"`
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
	More   string `json:"more,omitempty"`
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

// PairingSideFacts는 궁합 한쪽의 파생 명식이다. 생년월일·시각·이름은 없다 — 서버가
// 유도에 쓰는 것은 종류와 기둥 간지뿐이다.
type PairingSideFacts struct {
	Kind  string     `json:"kind"`
	Chart ChartFacts `json:"chart"`
}

// PairIlganFacts의 AToB는 A가 B에게 무엇인가 — B의 일간에서 본 A의 일간 십성이다.
type PairIlganFacts struct {
	AToB string `json:"aToB"`
	BToA string `json:"bToA"`
	Hap  bool   `json:"hap"`
}

type PairIljiFacts struct {
	Tags    []string `json:"tags"`
	Primary string   `json:"primary"`
}

type PairOhaengFact struct {
	Name string `json:"name"`
	A    string `json:"a"`
	B    string `json:"b"`
	Kind string `json:"kind"`
}

type PairCloseFacts struct {
	Stem   string `json:"stem"`
	Branch string `json:"branch"`
}

// PairFacts는 앱 계산 코어가 두 명식에서 뽑은 짝 사실이다. 서버는 이것을 좌표로 믿지 않고
// 두 명식에서 같은 값을 다시 유도해 대조한다(pairing_selector.go).
type PairFacts struct {
	Ilgan  PairIlganFacts   `json:"ilgan"`
	Ilji   PairIljiFacts    `json:"ilji"`
	Ohaeng []PairOhaengFact `json:"ohaeng"`
	Close  PairCloseFacts   `json:"close"`
}

type ResolvePairingRequest struct {
	SchemaVersion int              `json:"schemaVersion"`
	A             PairingSideFacts `json:"a"`
	B             PairingSideFacts `json:"b"`
	Pair          PairFacts        `json:"pair"`
	Unlock        *UnlockRequest   `json:"unlock,omitempty"`
}

// LockedPairing에는 연도가 없다. 궁합은 명식 쌍 하나가 열람 단위라 deepKey가 고정이다.
type LockedPairing struct {
	DeepKey string `json:"deepKey"`
	Section string `json:"section"`
}

type ResolvePairingResult struct {
	SchemaVersion  int             `json:"schemaVersion"`
	ContentVersion string          `json:"contentVersion"`
	PairKey        string          `json:"pairKey"`
	Articles       []Article       `json:"articles"`
	Locked         []LockedPairing `json:"locked"`
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
