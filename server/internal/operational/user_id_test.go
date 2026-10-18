package operational

import (
	"testing"
	"time"
)

func TestOperationalPlatformUserID(t *testing.T) {
	event := Event{EventID: StableEventID("identity", "demo", "synthetic"), OccurredAt: time.Now(), Type: "identity.created", AppID: "demo", Outcome: "created", Attributes: map[string]any{"authType": "firebase", "platformUserId": "pu_01ARZ3NDEKTSV4RRFFQ69G5FAV"}}
	if err := validateEvent(event); err != nil {
		t.Fatalf("approved operational ID rejected: %v", err)
	}
	for _, value := range []any{"email@example.com", "pu_fake", "pu_01ARZ3NDEKTSV4RRFFQ69G5FAI", 42} {
		event.Attributes["platformUserId"] = value
		if err := validateEvent(event); err == nil {
			t.Fatal("invalid platform user ID accepted")
		}
	}
	delete(event.Attributes, "platformUserId")
	if err := validateEvent(event); err != nil {
		t.Fatal("legacy event rejected")
	}
	event.Attributes["uid"] = "firebase-user"
	if err := validateEvent(event); err == nil {
		t.Fatal("app login UID accepted")
	}
}
