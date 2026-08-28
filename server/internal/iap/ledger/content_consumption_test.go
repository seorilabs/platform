package ledger

import (
	"testing"

	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/platformerr"
)

func TestChooseContentSourceUsesUsageEmbeddedInEntitlement(t *testing.T) {
	sources := map[string]domain.Source{
		"source-b": {State: domain.StateActive},
		"source-a": {State: domain.StateActive, ContentUnitsConsumed: 1},
		"revoked":  {State: domain.StateRevoked},
	}
	chosen, remaining, err := chooseContentSource(sources, 2)
	if err != nil || chosen != "source-a" || remaining != 3 {
		t.Fatalf("chosen=%q remaining=%d err=%v", chosen, remaining, err)
	}

	source := sources[chosen]
	source.ContentUnitsConsumed = 2
	sources[chosen] = source
	chosen, remaining, err = chooseContentSource(sources, 2)
	if err != nil || chosen != "source-b" || remaining != 2 {
		t.Fatalf("after exhaustion chosen=%q remaining=%d err=%v", chosen, remaining, err)
	}
}

func TestChooseContentSourceRejectsInvalidUsage(t *testing.T) {
	_, _, err := chooseContentSource(map[string]domain.Source{
		"source": {State: domain.StateActive, ContentUnitsConsumed: 3},
	}, 2)
	if platformerr.CodeOf(err) != platformerr.CodeLedgerStateInvalid {
		t.Fatalf("code=%q err=%v", platformerr.CodeOf(err), err)
	}
}

func TestChooseContentSourceRejectsExhaustedSources(t *testing.T) {
	_, _, err := chooseContentSource(map[string]domain.Source{
		"source": {State: domain.StateActive, ContentUnitsConsumed: 2},
	}, 2)
	if platformerr.CodeOf(err) != platformerr.CodeContentTicketEmpty {
		t.Fatalf("code=%q err=%v", platformerr.CodeOf(err), err)
	}
}

// 조회는 차감과 같은 규칙으로 세되 0을 에러로 만들지 않는다.
// 화면에 "0장 남음"을 그리는 것은 정상 상태다.
func TestContentUnitsRemainingCountsActiveSourcesOnly(t *testing.T) {
	remaining, err := contentUnitsRemaining(map[string]domain.Source{
		"source-a": {State: domain.StateActive, ContentUnitsConsumed: 1},
		"source-b": {State: domain.StateActive},
		"revoked":  {State: domain.StateRevoked},
	}, 5)
	if err != nil || remaining != 9 {
		t.Fatalf("remaining=%d err=%v", remaining, err)
	}
}

func TestContentUnitsRemainingReportsZeroWithoutError(t *testing.T) {
	remaining, err := contentUnitsRemaining(map[string]domain.Source{
		"source": {State: domain.StateActive, ContentUnitsConsumed: 5},
	}, 5)
	if err != nil || remaining != 0 {
		t.Fatalf("remaining=%d err=%v", remaining, err)
	}

	remaining, err = contentUnitsRemaining(nil, 5)
	if err != nil || remaining != 0 {
		t.Fatalf("source 없음 remaining=%d err=%v", remaining, err)
	}
}

func TestContentUnitsRemainingRejectsInvalidUsage(t *testing.T) {
	_, err := contentUnitsRemaining(map[string]domain.Source{
		"source": {State: domain.StateActive, ContentUnitsConsumed: 6},
	}, 5)
	if platformerr.CodeOf(err) != platformerr.CodeLedgerStateInvalid {
		t.Fatalf("code=%q err=%v", platformerr.CodeOf(err), err)
	}
}
