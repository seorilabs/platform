package ledger

import (
	"testing"
)

func TestInboxRejectsUnsupportedRewards(t *testing.T) {
	in := InboxIssue{RequestID: "test", PlatformUserID: "pu_01ARZ3NDEKTSV4RRFFQ69G5FAV", Title: "Welcome", Body: "Reward", Actor: "test", Reason: AdminReasonInternalValidation}
	for _, reward := range []InboxReward{{Kind: "currency", EntitlementID: "coin", Quantity: 1}, {Kind: "entitlement", EntitlementID: "starter", Quantity: 2}, {Kind: "entitlement", EntitlementID: "../other", Quantity: 1}} {
		in.Rewards = []InboxReward{reward}
		if in.Validate() == nil {
			t.Fatalf("accepted %+v", reward)
		}
	}
	in.Rewards = []InboxReward{{Kind: "entitlement", EntitlementID: "starter", Quantity: 1}}
	if e := in.Validate(); e != nil {
		t.Fatal(e)
	}
	in.Rewards = append(in.Rewards, in.Rewards[0])
	if in.Validate() == nil {
		t.Fatal("duplicate accepted")
	}
}
