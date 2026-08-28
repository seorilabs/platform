package content

import (
	"errors"
	"testing"
	"time"

	"github.com/seorilabs/platform/server/internal/platformerr"
	"github.com/seorilabs/platform/server/internal/registry"
)

func ticketApp() registry.App {
	app := testContentApp()
	app.Content.TicketEntitlementID = "deep_reading_ticket"
	app.Content.TicketUnitsPerPurchase = 5
	return app
}

// 열람권 잔여와 이미 연 항목은 서버 원장에만 있다. 화면이 자기 캐시로
// 답하면 환불과 기기 교체에서 어긋나므로 이 경로가 값을 그대로 옮기는지 본다.
func TestDeepAccessReportsTicketBalanceAndUnlocks(t *testing.T) {
	unlocks := &fakeUnlocks{listed: []UnlockRecord{
		{
			ReadingKey: "reading-a", DeepKey: "flow:2026", Source: "ticket",
			CreatedAt: time.Date(2026, 8, 29, 1, 2, 3, 0, time.UTC),
		},
	}}
	access := NewAccessService(unlocks, nil, &fakeEntitlements{remaining: 3})

	got, err := access.DeepAccess(t.Context(), ticketApp(), "puid", 0)
	if err != nil {
		t.Fatal(err)
	}
	if got.Ticket == nil {
		t.Fatal("ticket이 없다")
	}
	if got.Ticket.EntitlementID != "deep_reading_ticket" ||
		got.Ticket.Remaining != 3 || got.Ticket.UnitsPerPurchase != 5 {
		t.Fatalf("ticket=%+v", *got.Ticket)
	}
	if len(got.Unlocks) != 1 || got.Unlocks[0].ReadingKey != "reading-a" {
		t.Fatalf("unlocks=%+v", got.Unlocks)
	}
}

// 열람권을 운영하지 않는 앱에서는 ticket을 만들지 않는다. 0장으로 그리면
// "구매하면 열린다"는 화면이 서지 않아야 할 앱에 선다.
func TestDeepAccessOmitsTicketWhenAppHasNoTicketConfig(t *testing.T) {
	access := NewAccessService(&fakeUnlocks{}, nil, &fakeEntitlements{remaining: 9})

	got, err := access.DeepAccess(t.Context(), testContentApp(), "puid", 0)
	if err != nil {
		t.Fatal(err)
	}
	if got.Ticket != nil {
		t.Fatalf("ticket=%+v, 열람권 미설정 앱에는 없어야 한다", *got.Ticket)
	}
	if got.Unlocks == nil {
		t.Fatal("unlocks는 빈 배열이어야 한다")
	}
}

// 열람권 앱인데 원장 접합면이 없으면 조용히 넘어가지 않는다. 그대로 두면
// 배선 버그가 "열람권 없는 앱" 화면으로 위장된다.
func TestDeepAccessFailsWhenTicketAppHasNoLedger(t *testing.T) {
	access := NewAccessService(&fakeUnlocks{}, nil, nil)

	_, err := access.DeepAccess(t.Context(), ticketApp(), "puid", 0)
	if platformerr.CodeOf(err) != platformerr.CodeContentLocked {
		t.Fatalf("code=%q err=%v", platformerr.CodeOf(err), err)
	}
}

func TestDeepAccessPropagatesLedgerFailure(t *testing.T) {
	boom := errors.New("ledger down")
	access := NewAccessService(&fakeUnlocks{}, nil, &fakeEntitlements{remainingErr: boom})

	if _, err := access.DeepAccess(t.Context(), ticketApp(), "puid", 0); !errors.Is(err, boom) {
		t.Fatalf("err=%v", err)
	}
}

// 앱이 deepKey 문자열을 다시 파싱하지 않도록 서버가 연도를 뽑아 준다.
func TestDeepAccessDerivesYearFromDeepKey(t *testing.T) {
	req := validResolveRequest()
	access := &serviceAccess{deep: DeepAccess{
		Ticket: &TicketBalance{EntitlementID: "deep_reading_ticket", Remaining: 2, UnitsPerPurchase: 5},
		Unlocks: []UnlockRecord{
			{ReadingKey: "a", DeepKey: "flow:2026", Source: "ticket", CreatedAt: time.Unix(200, 0)},
			{ReadingKey: "b", DeepKey: "이상한키", Source: "reward_claim", CreatedAt: time.Unix(100, 0)},
		},
	}}

	got, err := newTestService(t, req, serviceUsage{}, access).
		DeepAccess(t.Context(), "ungeul", "puid", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Unlocks) != 2 {
		t.Fatalf("unlocks=%+v", got.Unlocks)
	}
	if got.Unlocks[0].Year != 2026 {
		t.Fatalf("year=%d, want 2026", got.Unlocks[0].Year)
	}
	// 형식이 다른 키는 0이 되고 omitempty로 빠진다. 목록 자체는 계속 그려야 한다.
	if got.Unlocks[1].Year != 0 {
		t.Fatalf("알 수 없는 deepKey의 year=%d, want 0", got.Unlocks[1].Year)
	}
	if got.Unlocks[0].UnlockedAt == "" {
		t.Fatal("unlockedAt이 비었다")
	}
}

func TestDeepAccessRequiresAccessController(t *testing.T) {
	req := validResolveRequest()
	_, err := newTestService(t, req, serviceUsage{}, nil).
		DeepAccess(t.Context(), "ungeul", "puid", 0)
	if platformerr.CodeOf(err) != platformerr.CodeContentLocked {
		t.Fatalf("code=%q", platformerr.CodeOf(err))
	}
}
