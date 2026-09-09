package identity

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"golang.org/x/oauth2"
	"google.golang.org/api/impersonate"

	"github.com/seorilabs/platform/server/internal/registry"
)

type FirebaseAccountDeleter struct{}

func (FirebaseAccountDeleter) DeleteFirebaseIdentity(ctx context.Context, app registry.App, uid string) error {
	if uid == "" || app.FirebaseProjectID == "" {
		return fmt.Errorf("identity: Firebase deletion target missing")
	}
	// 기존 앱별 서명 SA를 재사용한다. 워커 기본 SA에 전체 앱의 Auth 권한을
	// 부여하지 않고 해당 앱 SA로 짧은 수명의 토큰만 발급한다.
	ts, err := impersonate.CredentialsTokenSource(ctx, impersonate.CredentialsConfig{TargetPrincipal: app.FirebaseCustomTokenServiceAccount, Scopes: []string{"https://www.googleapis.com/auth/cloud-platform"}, Lifetime: 5 * time.Minute})
	if err != nil {
		return fmt.Errorf("identity: deletion credentials unavailable: %w", err)
	}
	client := oauth2.NewClient(ctx, ts)
	client.Timeout = 30 * time.Second
	payload, _ := json.Marshal(map[string]string{"localId": uid})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://identitytoolkit.googleapis.com/v1/projects/"+app.FirebaseProjectID+"/accounts:delete", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("identity: Firebase deletion transport failed")
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(response.Body, 64*1024))
	if err != nil {
		return fmt.Errorf("identity: Firebase deletion response unavailable")
	}
	if response.StatusCode == http.StatusOK {
		return nil
	}
	var result struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &result)
	if response.StatusCode == http.StatusBadRequest && result.Error.Message == "USER_NOT_FOUND" {
		return nil
	}
	return fmt.Errorf("identity: Firebase deletion HTTP %d", response.StatusCode)
}
