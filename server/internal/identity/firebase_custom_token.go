package identity

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"google.golang.org/api/iamcredentials/v1"

	"github.com/seorilabs/platform/server/internal/registry"
)

const firebaseCustomTokenAudience = "https://identitytoolkit.googleapis.com/google.identity.identitytoolkit.v1.IdentityToolkit"

// CustomTokenIssuer는 Firebase custom token을 발급한다.
// Service가 소비하므로 인터페이스도 identity 패키지에 둔다.
type CustomTokenIssuer interface {
	Mint(ctx context.Context, app registry.App, uid string) (string, error)
}

type jwtSigner interface {
	SignJWT(ctx context.Context, serviceAccount, payload string) (string, error)
}

// IAMCustomTokenIssuer는 private key 없이 IAM Credentials API로 JWT를 서명한다.
// platform-api SA는 레지스트리에 지정된 앱 SA에 대해서만 signJwt 권한을 받는다.
type IAMCustomTokenIssuer struct {
	signer jwtSigner
	now    func() time.Time
}

func NewIAMCustomTokenIssuer(ctx context.Context) (*IAMCustomTokenIssuer, error) {
	service, err := iamcredentials.NewService(ctx)
	if err != nil {
		return nil, fmt.Errorf("identity: IAM Credentials client 생성 실패: %w", err)
	}
	return &IAMCustomTokenIssuer{
		signer: &googleIAMJWTSigner{service: service},
		now:    time.Now,
	}, nil
}

func (i *IAMCustomTokenIssuer) WithClock(now func() time.Time) *IAMCustomTokenIssuer {
	i.now = now
	return i
}

func (i *IAMCustomTokenIssuer) Mint(
	ctx context.Context,
	app registry.App,
	uid string,
) (string, error) {
	if len(uid) == 0 || len(uid) > 128 {
		return "", fmt.Errorf("identity: Firebase uid 길이가 올바르지 않다")
	}
	serviceAccount := app.FirebaseCustomTokenServiceAccount
	if serviceAccount == "" {
		return "", fmt.Errorf("identity: custom token service account가 없다")
	}

	now := i.now().UTC()
	payload, err := json.Marshal(map[string]any{
		"iss": serviceAccount,
		"sub": serviceAccount,
		"aud": firebaseCustomTokenAudience,
		"iat": now.Unix(),
		"exp": now.Add(time.Hour).Unix(),
		"uid": uid,
	})
	if err != nil {
		return "", fmt.Errorf("identity: custom token payload 생성 실패: %w", err)
	}

	token, err := i.signer.SignJWT(ctx, serviceAccount, string(payload))
	if err != nil {
		return "", fmt.Errorf("identity: custom token 원격 서명 실패: %w", err)
	}
	if token == "" {
		return "", fmt.Errorf("identity: custom token 서명 결과가 비어 있다")
	}
	return token, nil
}

type googleIAMJWTSigner struct {
	service *iamcredentials.Service
}

func (s *googleIAMJWTSigner) SignJWT(
	ctx context.Context,
	serviceAccount, payload string,
) (string, error) {
	name := "projects/-/serviceAccounts/" + serviceAccount
	response, err := s.service.Projects.ServiceAccounts.SignJwt(
		name,
		&iamcredentials.SignJwtRequest{Payload: payload},
	).Context(ctx).Do()
	if err != nil {
		return "", err
	}
	return response.SignedJwt, nil
}
