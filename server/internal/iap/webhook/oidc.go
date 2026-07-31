package webhook

import (
	"context"

	"google.golang.org/api/idtoken"

	"github.com/seorilabs/platform/server/internal/platformerr"
)

// GoogleTokenValidator는 Pub/Sub이 붙인 OIDC 토큰을 검증한다.
//
// Firebase Functions의 onMessagePublished 트리거에는 이 단계가 없었다.
// 인프라가 인증을 대신해줬기 때문이다. push subscription으로 바꾸면서
// 엔드포인트가 공개되므로 우리가 검증해야 한다.
type GoogleTokenValidator struct {
	// audience는 push subscription에 설정한 값이다.
	//
	// 보통 이 서비스의 Cloud Run URL이다. 다른 서비스로 발급된
	// 토큰을 재사용하지 못하게 막는 것이 목적이다.
	audience string
}

// NewGoogleTokenValidator는 검증기를 만든다.
func NewGoogleTokenValidator(audience string) (*GoogleTokenValidator, error) {
	if audience == "" {
		return nil, platformerr.New(platformerr.CodeRuntimeConfigInvalid,
			"알림 인증 대상이 필요해요")
	}
	return &GoogleTokenValidator{audience: audience}, nil
}

// Validate는 토큰을 검증하고 발급 주체 이메일을 돌려준다.
//
// idtoken.Validate가 서명, 만료, audience를 전부 본다.
// Google의 공개 키셋을 알아서 받아오고 캐시한다.
func (v *GoogleTokenValidator) Validate(ctx context.Context, token string) (string, error) {
	payload, err := idtoken.Validate(ctx, token, v.audience)
	if err != nil {
		return "", platformerr.Wrap(err, platformerr.CodeAuthInvalid,
			"알림 인증 토큰이 올바르지 않아요")
	}

	// 이메일은 claim에 있다. 없으면 서비스 계정 토큰이 아니다.
	email, _ := payload.Claims["email"].(string)
	if email == "" {
		return "", platformerr.New(platformerr.CodeAuthInvalid,
			"알림 인증 토큰에 발신자 정보가 없어요")
	}

	// 검증되지 않은 이메일은 신뢰할 수 없다.
	if verified, ok := payload.Claims["email_verified"].(bool); ok && !verified {
		return "", platformerr.New(platformerr.CodeAuthInvalid,
			"알림 발신자가 확인되지 않았어요")
	}

	return email, nil
}
