package identity

import (
	"crypto/subtle"
	"errors"
	"log/slog"
	"mime"
	"net/http"
	"strings"

	"github.com/seorilabs/platform/server/internal/platformerr"
)

const kakaoUnlinkMaxBodyBytes = 4 * 1024

var kakaoUnlinkReferrers = map[string]struct{}{
	"ACCOUNT_DELETE":        {},
	"FORCED_ACCOUNT_DELETE": {},
	"UNLINK_FROM_ADMIN":     {},
	"UNLINK_FROM_APPS":      {},
	"INCOMPLETE_SIGN_UP":    {},
}

// KakaoUnlinkWebhookConfig는 Kakao app과 Platform app의 서버 측 연결이다.
// AdminKey는 요청 인증에만 사용하고 로그나 응답에 포함하지 않는다.
type KakaoUnlinkWebhookConfig struct {
	PlatformAppID string
	KakaoAppID    string
	AdminKey      []byte
}

type kakaoUnlinkRequest struct {
	userID       string
	referrerType string
}

func (h *Handler) kakaoUnlinkWebhook(w http.ResponseWriter, r *http.Request) error {
	config := h.kakaoUnlink
	if config == nil {
		return platformerr.New(platformerr.CodePlatformUnavailable,
			"Kakao webhook이 준비되지 않았어요")
	}
	if !validKakaoAuthorization(r.Header.Get("Authorization"), config.AdminKey) {
		return platformerr.New(platformerr.CodeAuthInvalid, "Kakao webhook 인증이 올바르지 않아요")
	}
	req, err := decodeKakaoUnlinkRequest(w, r, config.KakaoAppID)
	if err != nil {
		return err
	}

	// Kakao는 유효한 callback에 대해 사용자 부재나 내부 처리 실패 여부와 관계없이
	// 3초 안에 빈 200을 요구한다. 재전송 폭주를 막고 오류는 비식별 코드만 남긴다.
	if err := h.svc.DisconnectExternalAccount(
		r.Context(), config.PlatformAppID, "kakao", req.userID,
	); err != nil {
		slog.ErrorContext(r.Context(), "Kakao unlink webhook 처리 실패",
			"app_id", config.PlatformAppID,
			"referrer_type", req.referrerType,
			"error_code", platformerr.CodeOf(err),
		)
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	return nil
}

func validKakaoAuthorization(value string, adminKey []byte) bool {
	if len(adminKey) == 0 {
		return false
	}
	want := append([]byte("KakaoAK "), adminKey...)
	return subtle.ConstantTimeCompare([]byte(value), want) == 1
}

func decodeKakaoUnlinkRequest(
	w http.ResponseWriter,
	r *http.Request,
	wantAppID string,
) (kakaoUnlinkRequest, error) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/x-www-form-urlencoded" {
		return kakaoUnlinkRequest{}, platformerr.New(platformerr.CodeContentTypeInvalid,
			"application/x-www-form-urlencoded이 필요해요")
	}
	r.Body = http.MaxBytesReader(w, r.Body, kakaoUnlinkMaxBodyBytes)
	if err := r.ParseForm(); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) || strings.Contains(err.Error(), "request body too large") {
			return kakaoUnlinkRequest{}, platformerr.New(platformerr.CodeRequestTooLarge,
				"Kakao webhook 요청이 너무 커요")
		}
		return kakaoUnlinkRequest{}, platformerr.New(platformerr.CodeRequestInvalid,
			"Kakao webhook 요청을 해석할 수 없어요")
	}
	allowed := map[string]struct{}{
		"app_id": {}, "user_id": {}, "referrer_type": {}, "group_user_token": {},
	}
	for key, values := range r.PostForm {
		if _, ok := allowed[key]; !ok || len(values) != 1 {
			return kakaoUnlinkRequest{}, platformerr.New(platformerr.CodeRequestInvalid,
				"Kakao webhook 요청 필드가 올바르지 않아요")
		}
	}
	appID, ok := oneFormValue(r.PostForm, "app_id")
	if !ok || appID != wantAppID {
		return kakaoUnlinkRequest{}, platformerr.New(platformerr.CodeRequestInvalid,
			"Kakao 앱 ID가 올바르지 않아요")
	}
	userID, ok := oneFormValue(r.PostForm, "user_id")
	if !ok || !isDecimalID(userID, 20) {
		return kakaoUnlinkRequest{}, platformerr.New(platformerr.CodeRequestInvalid,
			"Kakao 사용자 ID가 올바르지 않아요")
	}
	referrerType, ok := oneFormValue(r.PostForm, "referrer_type")
	if !ok {
		return kakaoUnlinkRequest{}, platformerr.New(platformerr.CodeRequestInvalid,
			"Kakao 연결 해제 사유가 필요해요")
	}
	if _, allowed := kakaoUnlinkReferrers[referrerType]; !allowed {
		return kakaoUnlinkRequest{}, platformerr.New(platformerr.CodeRequestInvalid,
			"Kakao 연결 해제 사유가 올바르지 않아요")
	}
	if values, exists := r.PostForm["group_user_token"]; exists &&
		(len(values) != 1 || len(values[0]) > 256) {
		return kakaoUnlinkRequest{}, platformerr.New(platformerr.CodeRequestInvalid,
			"Kakao group user token이 올바르지 않아요")
	}
	return kakaoUnlinkRequest{userID: userID, referrerType: referrerType}, nil
}

func oneFormValue(values map[string][]string, key string) (string, bool) {
	items, ok := values[key]
	if !ok || len(items) != 1 || items[0] == "" {
		return "", false
	}
	return items[0], true
}

func isDecimalID(value string, maxLen int) bool {
	if value == "" || len(value) > maxLen {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
