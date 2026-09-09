package events

import (
	"strings"
	"testing"
)

func TestDeletionSQLKeepsIAPAndScopesEveryCopy(t *testing.T) {
	sql := deletionSQL("platform-project", "platform", []string{"game.analytics_123.events_20260909"})
	for _, part := range []string{"app_id=@app AND platform_user_id=@user", "NOT STARTS_WITH(action, 'iap.')", "WHERE user_id=@user"} {
		if !strings.Contains(sql, part) {
			t.Fatal("missing scope", part)
		}
	}
	if strings.Count(sql, "DELETE FROM") != 3 {
		t.Fatal("not all data copies are covered")
	}
}
