package identity

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/seorilabs/platform/server/internal/platformerr"
)

const defaultAITBaseURL = "https://apps-in-toss-api.toss.im"

type AITLoginClient struct {
	client  *http.Client
	baseURL string
}

func NewAITLoginClient(cert tls.Certificate, baseURL string) (*AITLoginClient, error) {
	if len(cert.Certificate) == 0 {
		return nil, platformerr.New(platformerr.CodeProviderConfigInvalid, "AppsInToss 로그인 인증서가 필요해요")
	}
	if strings.TrimSpace(baseURL) == "" {
		baseURL = defaultAITBaseURL
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme != "https" {
		return nil, platformerr.New(platformerr.CodeProviderConfigInvalid, "AppsInToss 로그인 주소가 올바르지 않아요")
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}}
	return &AITLoginClient{client: &http.Client{Timeout: 10 * time.Second, Transport: transport}, baseURL: strings.TrimSuffix(baseURL, "/")}, nil
}

func (c *AITLoginClient) Verify(ctx context.Context, authorizationCode, referrer string) (string, error) {
	body, err := json.Marshal(map[string]string{"authorizationCode": authorizationCode, "referrer": referrer})
	if err != nil {
		return "", platformerr.Wrap(err, platformerr.CodeInternal, "AppsInToss 로그인 요청을 만들지 못했어요")
	}
	var token struct {
		ResultType string `json:"resultType"`
		Success    *struct {
			AccessToken string `json:"accessToken"`
		} `json:"success"`
		Error *aitError `json:"error"`
	}
	if err := c.call(ctx, http.MethodPost, "/api-partner/v1/apps-in-toss/user/oauth2/generate-token", body, "", &token); err != nil {
		return "", err
	}
	if token.ResultType != "SUCCESS" || token.Success == nil || token.Success.AccessToken == "" {
		logAITFailure(ctx, "generate-token", token.ResultType, token.Error)
		return "", platformerr.New(platformerr.CodeAuthInvalid, "AppsInToss 로그인을 확인하지 못했어요")
	}
	accessToken := token.Success.AccessToken
	var me struct {
		ResultType string `json:"resultType"`
		Success    *struct {
			UserKey json.RawMessage `json:"userKey"`
		} `json:"success"`
		Error *aitError `json:"error"`
	}
	if err := c.call(ctx, http.MethodGet, "/api-partner/v1/apps-in-toss/user/oauth2/login-me", nil, accessToken, &me); err != nil {
		return "", err
	}
	if me.ResultType != "SUCCESS" || me.Success == nil {
		logAITFailure(ctx, "login-me", me.ResultType, me.Error)
		return "", platformerr.New(platformerr.CodeAuthInvalid, "AppsInToss 사용자를 확인하지 못했어요")
	}
	userKey := strings.Trim(strings.TrimSpace(string(me.Success.UserKey)), "\"")
	if userKey == "" || len(userKey) > 128 {
		return "", platformerr.New(platformerr.CodeProviderResponseInvalid, "AppsInToss 사용자 응답이 올바르지 않아요")
	}
	for _, r := range userKey {
		if r < '0' || r > '9' {
			return "", platformerr.New(platformerr.CodeProviderResponseInvalid, "AppsInToss 사용자 응답이 올바르지 않아요")
		}
	}
	sum := sha256.Sum256([]byte(userKey))
	return hex.EncodeToString(sum[:]), nil
}

func (c *AITLoginClient) call(ctx context.Context, method, path string, body []byte, bearer string, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return platformerr.Wrap(err, platformerr.CodeProviderUnavailable, "AppsInToss 로그인 요청을 만들지 못했어요")
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	res, err := c.client.Do(req)
	if err != nil {
		return platformerr.Wrap(err, platformerr.CodeProviderUnavailable, "AppsInToss 로그인 서버에 연결하지 못했어요")
	}
	defer func() { _ = res.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return platformerr.Wrap(err, platformerr.CodeProviderUnavailable, "AppsInToss 로그인 응답을 읽지 못했어요")
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		// 토스는 비즈니스 오류를 200 + resultType=FAIL로 주므로 여기 오는 것은
		// mTLS 신원이나 요청 형식 문제다. 어느 쪽인지 알 수 있게 상태와 오류 코드를 남긴다.
		var envelope struct {
			ResultType string    `json:"resultType"`
			Error      *aitError `json:"error"`
		}
		_ = json.Unmarshal(raw, &envelope)
		slog.ErrorContext(ctx, "AppsInToss 로그인 API 실패",
			"path", path,
			"status", res.StatusCode,
			"result_type", envelope.ResultType,
			"error_code", errorCodeOf(envelope.Error),
			"reason", reasonOf(envelope.Error),
		)
		if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
			return platformerr.New(platformerr.CodeAuthInvalid, "AppsInToss 로그인을 확인하지 못했어요")
		}
		return platformerr.New(platformerr.CodeProviderUnavailable, "AppsInToss 로그인 서버 응답이 올바르지 않아요")
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return platformerr.Wrap(err, platformerr.CodeProviderResponseInvalid, "AppsInToss 로그인 응답을 해석하지 못했어요")
	}
	return nil
}

// aitError는 토스 파트너 API의 오류 봉투다. 토큰이나 사용자 식별자를 담지 않는다.
type aitError struct {
	ErrorCode string `json:"errorCode"`
	Reason    string `json:"reason"`
}

func errorCodeOf(e *aitError) string {
	if e == nil {
		return ""
	}
	return e.ErrorCode
}

func reasonOf(e *aitError) string {
	if e == nil {
		return ""
	}
	return e.Reason
}

// logAITFailure는 HTTP 200으로 오는 비즈니스 실패의 근거를 남긴다.
//
// 이 값이 없으면 "인증서버에 등록된 미니앱이 아닙니다"(4050)와 만료된 인가코드가
// 서버 로그에서 똑같이 보인다. 인가코드와 access token은 남기지 않는다.
func logAITFailure(ctx context.Context, step, resultType string, e *aitError) {
	slog.ErrorContext(ctx, "AppsInToss 로그인 실패",
		"step", step,
		"result_type", resultType,
		"error_code", errorCodeOf(e),
		"reason", reasonOf(e),
	)
}
