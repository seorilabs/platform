package ads

import (
	"encoding/json"
	"testing"
	"time"
)

// Claim JSON은 소유자 응답(생성·조회·확정·ack)과 운영 목록에 그대로 쓰인다. 게임이 대조할 requestId는 싣고,
// 사용자·지원 코드·거래 해시처럼 다른 경계의 값은 싣지 않는다.
func TestClaimJSONExposesRequestIDOnly(t *testing.T) {
	claim := Claim{
		ClaimID: "cl_1", RequestID: "req-1", AppID: "reascend",
		PlatformUserID: "puid-secret", SupportCode: "SUPPORT-1", TransactionHash: "hash-secret",
		CreatedAt: time.Unix(0, 0), ExpiresAt: time.Unix(0, 0), TTLAt: time.Unix(0, 0),
	}
	raw, err := json.Marshal(claim)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if body["requestId"] != "req-1" {
		t.Fatalf("requestId = %v, want req-1", body["requestId"])
	}
	for _, hidden := range []string{"platformUserId", "PlatformUserID", "supportCode", "SupportCode", "transactionHash", "TransactionHash", "ttlAt", "TTLAt"} {
		if _, ok := body[hidden]; ok {
			t.Fatalf("%s must not be serialized", hidden)
		}
	}
}
