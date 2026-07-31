package apple

import (
	"context"
	"crypto/x509"
	"errors"
	"time"

	"github.com/richzw/appstore"

	"github.com/seorilabs/platform/server/internal/platformerr"
)

// requestTimeout은 App Store 호출 상한이다.
//
// Play의 8초보다 길다. Apple은 JWS 검증에 인증서 체인 확인이
// 붙고 OCSP 왕복까지 있어서 실측이 더 느리다.
const requestTimeout = 12 * time.Second

// Apple App Store Server API 에러 코드다.
// https://developer.apple.com/documentation/appstoreserverapi/error_codes
const (
	errInvalidTransactionID     = 4000006
	errTransactionIDNotFound    = 4040010
	errRateLimitExceeded        = 4290000
	errGeneralInternal          = 5000000
	errGeneralInternalRetryable = 5000001
)

// Client는 richzw/appstore를 transactionSource로 감싼 어댑터다.
//
// 라이브러리 타입이 이 파일 밖으로 새지 않게 한다.
// 도메인 판단은 apple.go의 순수 함수가 한다.
type Client struct {
	store    *appstore.StoreClient
	revoker  *revocationChecker
	timeout  time.Duration
	sandbox  bool
	bundleID string
}

// Config는 App Store 클라이언트 설정이다.
type Config struct {
	// KeyContent는 App Store Connect에서 받은 .p8 개인키다.
	KeyContent []byte
	KeyID      string
	Issuer     string
	BundleID   string
	Sandbox    bool

	// RequireOCSP는 인증서 폐기 확인을 강제한다.
	//
	// production은 반드시 true다. richzw/appstore는 체인 검증까지만
	// 하고 폐기 확인을 하지 않아서 우리가 더한다. ADR 0009다.
	RequireOCSP bool

	// TrustedRoots는 신뢰 루트다. nil이면 Apple Root CA G3만 쓴다.
	// 테스트에서만 다른 값을 넣는다.
	TrustedRoots *x509.CertPool
}

// NewClient는 App Store 클라이언트를 만든다.
func NewClient(cfg Config) (*Client, error) {
	switch {
	case len(cfg.KeyContent) == 0:
		return nil, platformerr.New(platformerr.CodeProviderConfigInvalid,
			"App Store 개인키가 필요해요")
	case cfg.KeyID == "":
		return nil, platformerr.New(platformerr.CodeProviderConfigInvalid,
			"App Store 키 ID가 필요해요")
	case cfg.Issuer == "":
		return nil, platformerr.New(platformerr.CodeProviderConfigInvalid,
			"App Store issuer ID가 필요해요")
	case cfg.BundleID == "":
		return nil, platformerr.New(platformerr.CodeProviderConfigInvalid,
			"App Store 번들 ID가 필요해요")
	}

	// production에서 폐기 확인을 끄면 탈취된 인증서로 만든 위조 JWS를
	// 그대로 신뢰하게 된다. 설정 실수를 부팅 때 잡는다.
	if !cfg.Sandbox && !cfg.RequireOCSP {
		return nil, platformerr.New(platformerr.CodeProviderConfigInvalid,
			"production에서는 인증서 폐기 확인을 끌 수 없어요")
	}

	store := appstore.NewStoreClient(&appstore.StoreConfig{
		KeyContent:      cfg.KeyContent,
		KeyID:           cfg.KeyID,
		Issuer:          cfg.Issuer,
		BundleID:        cfg.BundleID,
		Sandbox:         cfg.Sandbox,
		TrustedCertPool: cfg.TrustedRoots,
	})

	c := &Client{
		store:    store,
		timeout:  requestTimeout,
		sandbox:  cfg.Sandbox,
		bundleID: cfg.BundleID,
	}
	if cfg.RequireOCSP {
		c.revoker = newRevocationChecker(cfg.TrustedRoots)
	}
	return c, nil
}

// GetTransaction은 거래를 조회하고 JWS를 검증한다.
func (c *Client) GetTransaction(ctx context.Context, transactionID string) (*appstore.JWSTransaction, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	rsp, err := c.store.GetTransactionInfo(ctx, transactionID)
	if err != nil {
		return nil, mapAPIError(ctx, err)
	}
	if rsp == nil || rsp.SignedTransactionInfo == "" {
		return nil, platformerr.New(platformerr.CodeProviderResponseInvalid,
			"App Store 응답에 거래 정보가 없어요")
	}

	// 서명·체인 검증이 여기서 일어난다.
	// 실패하면 위조되었거나 Apple 인증서가 바뀐 것이다.
	tx, err := c.store.ParseNotificationV2TransactionInfo(rsp.SignedTransactionInfo)
	if err != nil {
		return nil, platformerr.Wrap(err, platformerr.CodeProviderResponseInvalid,
			"App Store 서명을 확인하지 못했어요")
	}

	// 라이브러리가 하지 않는 폐기 확인을 더한다.
	if c.revoker != nil {
		if err := c.revoker.check(ctx, rsp.SignedTransactionInfo); err != nil {
			return nil, err
		}
	}

	return tx, nil
}

// Finish는 finishTransaction을 호출한다.
func (c *Client) Finish(ctx context.Context, transactionID string) error {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	if _, err := c.store.FinishTransaction(ctx, transactionID); err != nil {
		return mapAPIError(ctx, err)
	}
	return nil
}

// mapAPIError는 App Store 에러를 플랫폼 에러로 바꾼다.
//
// 재시도 가능 여부가 여기서 갈린다.
func mapAPIError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}

	// 타임아웃이 먼저다. 라이브러리가 감싼 에러 안에 숨을 수 있다.
	if ctx.Err() != nil {
		return platformerr.Wrap(err, platformerr.CodeProviderTimeout,
			"App Store 응답이 늦어요")
	}

	var apiErr *appstore.Error
	if !errors.As(err, &apiErr) {
		return platformerr.Wrap(err, platformerr.CodeProviderUnavailable,
			"App Store에 연결하지 못했어요")
	}

	switch apiErr.ErrorCode() {
	case errTransactionIDNotFound:
		// 마켓 반영 지연일 수 있어 재시도 가능으로 둔다.
		return platformerr.Wrap(err, platformerr.CodePurchaseNotFound,
			"구매 내역을 찾을 수 없어요")
	case errInvalidTransactionID:
		return platformerr.Wrap(err, platformerr.CodePurchaseInvalid,
			"구매 정보가 올바르지 않아요")
	case errRateLimitExceeded, errGeneralInternal, errGeneralInternalRetryable:
		return platformerr.Wrap(err, platformerr.CodeProviderUnavailable,
			"App Store가 응답하지 않아요")
	default:
		// 401 계열은 라이브러리가 API 에러로 만들지 않고 status 에러로
		// 흘리므로 여기 오지 않는다. 남은 것은 미지 코드다.
		return platformerr.Wrap(err, platformerr.CodeProviderUnavailable,
			"App Store 응답을 처리하지 못했어요")
	}
}

// ParseNotification은 App Store 알림 JWS를 검증하고 해석한다.
//
// 이 검증이 알림 엔드포인트의 인증이다. 별도 토큰이 없다.
// Apple 인증서 체인으로 서명된 페이로드만 통과한다.
func (c *Client) ParseNotification(signedPayload string) (*appstore.NotificationPayload, error) {
	if signedPayload == "" {
		return nil, platformerr.New(platformerr.CodeProviderResponseInvalid,
			"App Store 알림이 비어 있어요")
	}

	payload, err := c.store.ParseNotificationV2Payload(signedPayload)
	if err != nil {
		return nil, platformerr.Wrap(err, platformerr.CodeProviderResponseInvalid,
			"App Store 알림 서명을 확인하지 못했어요")
	}
	return payload, nil
}

// ParseTransaction은 알림에 실린 거래 JWS를 검증하고 해석한다.
func (c *Client) ParseTransaction(signedTransactionInfo string) (*appstore.JWSTransaction, error) {
	if signedTransactionInfo == "" {
		return nil, platformerr.New(platformerr.CodeProviderResponseInvalid,
			"App Store 거래 정보가 비어 있어요")
	}

	tx, err := c.store.ParseNotificationV2TransactionInfo(signedTransactionInfo)
	if err != nil {
		return nil, platformerr.Wrap(err, platformerr.CodeProviderResponseInvalid,
			"App Store 거래 서명을 확인하지 못했어요")
	}
	return tx, nil
}
