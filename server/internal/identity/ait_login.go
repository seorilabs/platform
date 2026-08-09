package identity

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"io"
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
	}
	if err := c.call(ctx, http.MethodPost, "/api-partner/v1/apps-in-toss/user/oauth2/generate-token", body, "", &token); err != nil {
		return "", err
	}
	if token.ResultType != "SUCCESS" || token.Success == nil || token.Success.AccessToken == "" {
		return "", platformerr.New(platformerr.CodeAuthInvalid, "AppsInToss 로그인을 확인하지 못했어요")
	}
	accessToken := token.Success.AccessToken
	var me struct {
		ResultType string `json:"resultType"`
		Success    *struct {
			UserKey json.RawMessage `json:"userKey"`
		} `json:"success"`
	}
	if err := c.call(ctx, http.MethodGet, "/api-partner/v1/apps-in-toss/user/oauth2/login-me", nil, accessToken, &me); err != nil {
		return "", err
	}
	if me.ResultType != "SUCCESS" || me.Success == nil {
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
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return platformerr.Wrap(err, platformerr.CodeProviderUnavailable, "AppsInToss 로그인 응답을 읽지 못했어요")
	}
	if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
		return platformerr.New(platformerr.CodeAuthInvalid, "AppsInToss 로그인을 확인하지 못했어요")
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return platformerr.New(platformerr.CodeProviderUnavailable, "AppsInToss 로그인 서버 응답이 올바르지 않아요")
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return platformerr.Wrap(err, platformerr.CodeProviderResponseInvalid, "AppsInToss 로그인 응답을 해석하지 못했어요")
	}
	return nil
}
