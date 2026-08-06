package identity

import (
	"context"
	"fmt"
	"sync"

	firebase "firebase.google.com/go/v4"
	"firebase.google.com/go/v4/appcheck"
)

// FirebaseAppCheckVerifier는 프로젝트별 Admin SDK client를 한 번만 만든다.
// 레지스트리에 여러 Firebase 프로젝트가 있으므로 기본 App 하나를 전역으로
// 쓰면 audience가 첫 프로젝트에 고정된다.
type FirebaseAppCheckVerifier struct {
	mu      sync.RWMutex
	clients map[string]*appcheck.Client
}

func NewFirebaseAppCheckVerifier() *FirebaseAppCheckVerifier {
	return &FirebaseAppCheckVerifier{clients: make(map[string]*appcheck.Client)}
}

func (v *FirebaseAppCheckVerifier) Verify(
	ctx context.Context,
	token string,
	firebaseProjectID string,
) error {
	client, err := v.client(ctx, firebaseProjectID)
	if err != nil {
		return err
	}
	if _, err := client.VerifyToken(token); err != nil {
		return fmt.Errorf("firebase App Check token 검증 실패: %w", err)
	}
	return nil
}

func (v *FirebaseAppCheckVerifier) client(
	ctx context.Context,
	projectID string,
) (*appcheck.Client, error) {
	v.mu.RLock()
	client := v.clients[projectID]
	v.mu.RUnlock()
	if client != nil {
		return client, nil
	}

	v.mu.Lock()
	defer v.mu.Unlock()
	if client = v.clients[projectID]; client != nil {
		return client, nil
	}
	app, err := firebase.NewApp(ctx, &firebase.Config{ProjectID: projectID})
	if err != nil {
		return nil, fmt.Errorf("firebase Admin App 초기화 실패: %w", err)
	}
	client, err = app.AppCheck(ctx)
	if err != nil {
		return nil, fmt.Errorf("firebase App Check client 초기화 실패: %w", err)
	}
	v.clients[projectID] = client
	return client, nil
}
