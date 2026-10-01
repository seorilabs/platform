package ledger

import (
	"context"
	"errors"
	"regexp"
	"sort"
	"time"

	"github.com/seorilabs/platform/server/internal/iap/boxes"
	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/platformerr"
	"github.com/seorilabs/platform/server/internal/store"
)

// boxRequestIDPattern은 OpenAPI BoxOpenRequest.requestId와 같다.
var boxRequestIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{8,64}$`)

type boxStateDoc struct {
	Version   int64          `firestore:"version"`
	Opened    int            `firestore:"opened"`
	SinceRare int            `firestore:"sinceRare"`
	Copies    map[string]int `firestore:"copies"`
	UpdatedAt time.Time      `firestore:"updatedAt"`
}

// boxOpenDoc은 개봉 증거다. 어떤 구매 source에서 차감했는지와 사용한
// 카탈로그 버전을 남겨 확률 실측·환불 대응·문의 재현에 쓴다.
type boxOpenDoc struct {
	PlatformUserID string    `firestore:"platformUserId"`
	RequestID      string    `firestore:"requestId"`
	CatalogVersion string    `firestore:"catalogVersion"`
	EntitlementID  string    `firestore:"entitlementId"`
	SourceKey      string    `firestore:"sourceKey"`
	FriendID       string    `firestore:"friendId"`
	Rarity         string    `firestore:"rarity"`
	Copies         int       `firestore:"copies"`
	Level          int       `firestore:"level"`
	NewFriend      bool      `firestore:"newFriend"`
	LevelUp        bool      `firestore:"levelUp"`
	Pity           bool      `firestore:"pity"`
	CreatedAt      time.Time `firestore:"createdAt"`
}

// BoxState는 OpenAPI BoxState다.
type BoxState struct {
	Version         int64          `json:"version"`
	Available       int            `json:"available"`
	ActiveRemaining int            `json:"activeRemaining"`
	Debt            int            `json:"debt"`
	Opened          int            `json:"opened"`
	SinceRare       int            `json:"sinceRare"`
	Copies          map[string]int `json:"copies"`
	Levels          map[string]int `json:"levels"`
}

// BoxReceipt는 OpenAPI BoxOpenReceipt다.
type BoxReceipt struct {
	RequestID      string   `json:"requestId"`
	Applied        bool     `json:"applied"`
	CatalogVersion string   `json:"catalogVersion"`
	FriendID       string   `json:"friendId"`
	Rarity         string   `json:"rarity"`
	Copies         int      `json:"copies"`
	Level          int      `json:"level"`
	NewFriend      bool     `json:"newFriend"`
	LevelUp        bool     `json:"levelUp"`
	Pity           bool     `json:"pity"`
	State          BoxState `json:"state"`
}

// boxUnits는 구매 source에서 남은 상자와 환불 부채를 센다. ADR 0029 6항.
//
//	activeRemaining = Σ(active source: units − consumed)
//	debt            = Σ(revoked source: consumed)
//
// 사용량 필드는 운글 열람권과 같은 ContentUnitsConsumed를 쓴다. 이 필드는
// source와 함께 소유권 이전·환불을 거쳐도 보존되므로(ledger.go grant·revoke)
// 재설치로 사용량이 되살아나지 않는다.
func boxUnits(cat *boxes.Catalog, ents map[string]entitlementDoc) (remaining, debt int, err error) {
	for entID, ent := range ents {
		units, ok := cat.Units(entID)
		if !ok {
			return 0, 0, platformerr.New(platformerr.CodeLedgerStateInvalid, "상자 상품 단위를 알 수 없어요")
		}
		for _, src := range ent.Sources {
			used := src.ContentUnitsConsumed
			if used < 0 || used > units {
				return 0, 0, platformerr.New(platformerr.CodeLedgerStateInvalid, "상자 source 사용 원장이 올바르지 않아요")
			}
			switch src.State {
			case domain.StateActive:
				remaining += units - used
			case domain.StateRevoked:
				debt += used
			}
		}
	}
	return remaining, debt, nil
}

// chooseBoxSource는 차감할 source를 고른다. entitlement·source key 정렬 순서의
// 첫 활성 source다. 정렬은 재시도와 테스트에서 결과를 고정하기 위해서다.
func chooseBoxSource(cat *boxes.Catalog, ents map[string]entitlementDoc) (string, string, bool) {
	entIDs := make([]string, 0, len(ents))
	for id := range ents {
		entIDs = append(entIDs, id)
	}
	sort.Strings(entIDs)
	for _, entID := range entIDs {
		units, _ := cat.Units(entID)
		keys := make([]string, 0, len(ents[entID].Sources))
		for k := range ents[entID].Sources {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			src := ents[entID].Sources[k]
			if src.State == domain.StateActive && src.ContentUnitsConsumed < units {
				return entID, k, true
			}
		}
	}
	return "", "", false
}

func boxView(doc boxStateDoc, remaining, debt int) BoxState {
	copies := make(map[string]int, len(doc.Copies))
	levels := make(map[string]int, len(doc.Copies))
	for id, n := range doc.Copies {
		if n > 0 {
			copies[id] = n
			levels[id] = boxes.LevelForCopies(n)
		}
	}
	return BoxState{
		Version: doc.Version, Available: remaining - debt, ActiveRemaining: remaining, Debt: debt,
		Opened: doc.Opened, SinceRare: doc.SinceRare, Copies: copies, Levels: levels,
	}
}

// BoxSnapshot은 차감하지 않고 상자 상태를 읽는다.
func (l *Ledger) BoxSnapshot(ctx context.Context, puid string, cat *boxes.Catalog) (BoxState, error) {
	if puid == "" || cat == nil {
		return BoxState{}, platformerr.New(platformerr.CodeInternal, "상자 조회 정보가 올바르지 않아요")
	}
	var out BoxState
	err := l.store.RunTransaction(ctx, func(_ context.Context, tx *store.Tx) error {
		doc, ents, err := l.readBoxes(tx, puid, cat)
		if err != nil {
			return err
		}
		remaining, debt, err := boxUnits(cat, ents)
		if err != nil {
			return err
		}
		out = boxView(doc, remaining, debt)
		return nil
	})
	return out, err
}

// readBoxes는 상자 상태 문서와 상자 entitlement 원장을 읽는다.
// Firestore 트랜잭션은 모든 읽기가 쓰기보다 먼저여야 하므로 한 번에 읽는다.
func (l *Ledger) readBoxes(tx *store.Tx, puid string, cat *boxes.Catalog) (boxStateDoc, map[string]entitlementDoc, error) {
	var doc boxStateDoc
	sp, err := l.paths.boxState(puid)
	if err != nil {
		return doc, nil, err
	}
	exists, snap, err := tx.Exists(sp)
	if err != nil {
		return doc, nil, err
	}
	if exists {
		if err := snap.DataTo(&doc); err != nil {
			return doc, nil, err
		}
	}
	ents := map[string]entitlementDoc{}
	for _, entID := range cat.EntitlementIDs() {
		p, err := l.paths.internalEntitlement(puid, entID)
		if err != nil {
			return doc, nil, err
		}
		ok, s, err := tx.Exists(p)
		if err != nil {
			return doc, nil, err
		}
		if !ok {
			continue
		}
		var ent entitlementDoc
		if err := s.DataTo(&ent); err != nil {
			return doc, nil, err
		}
		if ent.EntitlementID != "" && ent.EntitlementID != entID {
			return doc, nil, platformerr.New(platformerr.CodeLedgerStateInvalid, "상자 entitlement 원장이 올바르지 않아요")
		}
		ents[entID] = ent
	}
	return doc, ents, nil
}

// OpenBox는 상자 하나를 연다. ADR 0029 3항.
//
// 한 트랜잭션에서: 개봉 증거 확인(멱등) → 사용 가능 수 확인 → 활성 source
// 1단위 차감 → 서버 추첨 → 장수·보장 카운터 → 증거 create.
// 같은 requestId 재시도는 applied=false로 최초 결과와 현재 상태를 돌려준다.
func (l *Ledger) OpenBox(
	ctx context.Context,
	puid string,
	cat *boxes.Catalog,
	version *boxes.Version,
	requestID string,
	rnd boxes.Random,
) (BoxReceipt, error) {
	if puid == "" || cat == nil || version == nil || rnd == nil {
		return BoxReceipt{}, platformerr.New(platformerr.CodeInternal, "상자 개봉 정보가 올바르지 않아요")
	}
	if !boxRequestIDPattern.MatchString(requestID) {
		return BoxReceipt{}, platformerr.New(platformerr.CodeRequestInvalid, "상자 요청 id가 올바르지 않아요")
	}
	openPath, err := l.paths.boxOpen(contentRequestDigest(
		l.appID + "\x00" + string(l.env) + "\x00" + puid + "\x00" + requestID,
	))
	if err != nil {
		return BoxReceipt{}, err
	}
	statePath, err := l.paths.boxState(puid)
	if err != nil {
		return BoxReceipt{}, err
	}
	var out BoxReceipt
	err = l.store.RunTransaction(ctx, func(_ context.Context, tx *store.Tx) error {
		replayed, snap, err := tx.Exists(openPath)
		if err != nil {
			return err
		}
		doc, ents, err := l.readBoxes(tx, puid, cat)
		if err != nil {
			return err
		}
		remaining, debt, err := boxUnits(cat, ents)
		if err != nil {
			return err
		}
		if replayed {
			var prev boxOpenDoc
			if err := snap.DataTo(&prev); err != nil {
				return err
			}
			if prev.PlatformUserID != puid || prev.RequestID != requestID {
				return platformerr.New(platformerr.CodeBoxReplayMismatch, "상자 재시도 내용이 기존 개봉과 달라요")
			}
			out = receiptFrom(prev, false, boxView(doc, remaining, debt))
			return nil
		}
		if remaining-debt <= 0 {
			if debt > 0 {
				return platformerr.New(platformerr.CodeBoxDebt, "환불된 상자 때문에 지금은 열 수 없어요")
			}
			return platformerr.New(platformerr.CodeBoxEmpty, "열 수 있는 상자가 없어요")
		}
		entID, sourceKey, ok := chooseBoxSource(cat, ents)
		if !ok {
			return platformerr.New(platformerr.CodeLedgerStateInvalid, "상자 source를 고르지 못했어요")
		}

		draw, err := version.Draw(rnd, doc.Copies, doc.SinceRare)
		if err != nil {
			return err
		}
		now := l.now().UTC()
		before := doc.Copies[draw.FriendID]
		if doc.Copies == nil {
			doc.Copies = map[string]int{}
		}
		doc.Copies[draw.FriendID] = before + 1
		doc.SinceRare = version.NextSinceRare(doc.SinceRare, draw)
		doc.Opened++
		doc.Version++
		doc.UpdatedAt = now

		ent := ents[entID]
		src := ent.Sources[sourceKey]
		src.ContentUnitsConsumed++
		ent.Sources[sourceKey] = src
		ents[entID] = ent

		entPath, err := l.paths.internalEntitlement(puid, entID)
		if err != nil {
			return err
		}
		if err := tx.Set(entPath, ent); err != nil {
			return err
		}
		if err := tx.Set(statePath, doc); err != nil {
			return err
		}
		evidence := boxOpenDoc{
			PlatformUserID: puid, RequestID: requestID, CatalogVersion: version.Version,
			EntitlementID: entID, SourceKey: sourceKey,
			FriendID: draw.FriendID, Rarity: draw.Rarity,
			Copies: before + 1, Level: boxes.LevelForCopies(before + 1),
			NewFriend: before == 0,
			LevelUp:   boxes.LevelForCopies(before+1) > boxes.LevelForCopies(before),
			Pity:      draw.Pity, CreatedAt: now,
		}
		if err := tx.Create(openPath, evidence); err != nil {
			return err
		}
		out = receiptFrom(evidence, true, boxView(doc, remaining-1, debt))
		return nil
	})
	if err != nil {
		var pe *platformerr.Error
		if errors.As(err, &pe) {
			return BoxReceipt{}, err
		}
		return BoxReceipt{}, platformerr.Wrap(err, platformerr.CodeLedgerStateInvalid, "상자를 열지 못했어요")
	}
	return out, nil
}

func receiptFrom(d boxOpenDoc, applied bool, state BoxState) BoxReceipt {
	return BoxReceipt{
		RequestID: d.RequestID, Applied: applied, CatalogVersion: d.CatalogVersion,
		FriendID: d.FriendID, Rarity: d.Rarity, Copies: d.Copies, Level: d.Level,
		NewFriend: d.NewFriend, LevelUp: d.LevelUp, Pity: d.Pity, State: state,
	}
}
