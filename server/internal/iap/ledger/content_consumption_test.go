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
