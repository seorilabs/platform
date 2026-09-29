package identity

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	firebase "firebase.google.com/go/v4"
	"firebase.google.com/go/v4/appcheck"
	"github.com/golang-jwt/jwt/v4"
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
		slog.WarnContext(ctx, "Firebase App Check 검증기 준비 실패",
			"project_id", firebaseProjectID, "reason", "verifier_setup_failed")
		return err
	}
	if _, err := client.VerifyToken(token); err != nil {
		// 원문 토큰과 원인 문자열은 로그에 남기지 않는다. 기기에서 발급한
		// 증명이 왜 거부됐는지만 분류해 계정 연결 문제를 진단한다.
		slog.WarnContext(ctx, "Firebase App Check 토큰 거부",
			"project_id", firebaseProjectID, "reason", appCheckFailureReason(err))
		return fmt.Errorf("firebase App Check token 검증 실패: %w", err)
	}
	return nil
}

func appCheckFailureReason(err error) string {
	switch {
	case errors.Is(err, appcheck.ErrIncorrectAlgorithm):
		return "algorithm"
	case errors.Is(err, appcheck.ErrTokenType):
		return "token_type"
	case errors.Is(err, appcheck.ErrTokenClaims):
		return "claims"
	case errors.Is(err, appcheck.ErrTokenAudience):
		return "audience"
	case errors.Is(err, appcheck.ErrTokenIssuer):
		return "issuer"
	case errors.Is(err, appcheck.ErrTokenSubject):
		return "subject"
	}
	var validation *jwt.ValidationError
	if errors.As(err, &validation) {
		switch {
		case validation.Errors&jwt.ValidationErrorExpired != 0:
			return "expired"
		case validation.Errors&jwt.ValidationErrorNotValidYet != 0:
			return "not_valid_yet"
		case validation.Errors&jwt.ValidationErrorSignatureInvalid != 0:
			return "signature"
		case validation.Errors&jwt.ValidationErrorMalformed != 0:
			return "malformed"
		case validation.Errors&jwt.ValidationErrorUnverifiable != 0:
			return "unverifiable"
		}
	}
	return "verification_failed"
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
