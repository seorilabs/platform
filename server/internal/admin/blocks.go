package admin

import (
	"context"
	"net/http"
	"regexp"

	"github.com/seorilabs/platform/server/internal/blocklist"
	"github.com/seorilabs/platform/server/internal/httpx"
	"github.com/seorilabs/platform/server/internal/platformerr"
)

// adminBlockUIDPattern은 앱 범위 사용자 식별자다.
//
// Firebase uid는 28자 영숫자이고 AIT userKey와 익명 식별자는 형식이 다르다.
// 여기서 좁게 잡으면 정작 차단해야 할 계정을 못 넣는다. 대신 Firestore
// 문서 ID로 쓸 수 없는 문자만 막는다.
var adminBlockUIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

// Blocks는 앱별 차단 계정 조작 포트다. blocklist.Service가 구현한다.
type Blocks interface {
	List(ctx context.Context, appID string) ([]blocklist.Entry, error)
	Block(ctx context.Context, appID, uid, reason, actor string) (blocklist.Entry, error)
	Unblock(ctx context.Context, appID, uid string) (bool, error)
}

type blocksAdminHandler struct {
	service Blocks
	apps    Apps
	auditor Auditor
}

// RegisterBlocks는 차단 관리 API를 연다.
//
// 차단은 registry/apps/*.json이 아니라 여기로만 들어간다. 저장소가
// public이고 사용자 식별자를 git에 남길 수 없다. ADR 0026 참고.
func RegisterBlocks(mux *http.ServeMux, auth *Authenticator, service Blocks, apps Apps, auditor Auditor) error {
	if mux == nil || auth == nil || service == nil || apps == nil {
		return platformerr.New(platformerr.CodeRuntimeConfigInvalid,
			"차단 관리 Admin API 설정이 올바르지 않아요")
	}
	h := &blocksAdminHandler{service: service, apps: apps, auditor: auditor}

	mux.Handle("GET /v1/admin/apps/{appId}/blocks",
		auth.Middleware(AccessRead, http.HandlerFunc(httpx.Wrap(h.list))))
	mux.Handle("POST /v1/admin/apps/{appId}/blocks",
		auth.Middleware(AccessWrite, http.HandlerFunc(httpx.Wrap(h.block))))
	mux.Handle("DELETE /v1/admin/apps/{appId}/blocks/{uid}",
		auth.Middleware(AccessWrite, http.HandlerFunc(httpx.Wrap(h.unblock))))
	return nil
}

// requireApp은 존재하지 않는 앱에 차단을 쌓지 못하게 막는다.
//
// 오타 하나로 만들어진 앱 컬렉션은 아무 요청도 읽지 않으므로 차단한 줄
// 알고 넘어가게 된다.
func (h *blocksAdminHandler) requireApp(r *http.Request) (string, error) {
	appID := r.PathValue("appId")
	if !adminAppIDPattern.MatchString(appID) {
		return "", platformerr.New(platformerr.CodeRequestInvalid, "앱 식별자가 올바르지 않아요")
	}
	if _, err := h.apps.Get(r.Context(), appID); err != nil {
		return "", err
	}
	return appID, nil
}

func (h *blocksAdminHandler) list(w http.ResponseWriter, r *http.Request) error {
	appID, err := h.requireApp(r)
	if err != nil {
		return err
	}
	entries, err := h.service.List(r.Context(), appID)
	if err != nil {
		return err
	}
	httpx.WriteOK(w, http.StatusOK, map[string]any{"appId": appID, "blocks": entries})
	return nil
}

type blockRequest struct {
	UID    string `json:"uid"`
	Reason string `json:"reason"`
}

func (h *blocksAdminHandler) block(w http.ResponseWriter, r *http.Request) error {
	appID, err := h.requireApp(r)
	if err != nil {
		return err
	}

	var req blockRequest
	if err := httpx.DecodeStrict(w, r, &req); err != nil {
		return err
	}
	if !adminBlockUIDPattern.MatchString(req.UID) {
		return platformerr.New(platformerr.CodeRequestInvalid, "차단할 계정 식별자가 올바르지 않아요")
	}

	login := actorLogin(ActorFrom(r.Context()))
	entry, err := h.service.Block(r.Context(), appID, req.UID, req.Reason, login)
	if err != nil {
		h.audit(r.Context(), appID, req.UID, string(platformerr.CodeOf(err)), login)
		return err
	}

	h.audit(r.Context(), appID, req.UID, "blocked", login)
	httpx.WriteOK(w, http.StatusOK, entry)
	return nil
}

func (h *blocksAdminHandler) unblock(w http.ResponseWriter, r *http.Request) error {
	appID, err := h.requireApp(r)
	if err != nil {
		return err
	}
	uid := r.PathValue("uid")
	if !adminBlockUIDPattern.MatchString(uid) {
		return platformerr.New(platformerr.CodeRequestInvalid, "차단 해제할 계정 식별자가 올바르지 않아요")
	}

	login := actorLogin(ActorFrom(r.Context()))
	removed, err := h.service.Unblock(r.Context(), appID, uid)
	if err != nil {
		h.audit(r.Context(), appID, uid, string(platformerr.CodeOf(err)), login)
		return err
	}

	// 차단돼 있지 않았어도 200이다. 운영자가 원한 최종 상태는 같고,
	// 404를 보면 다른 앱을 잘못 본 것으로 오해해 조작을 반복한다.
	outcome := "unblocked"
	if !removed {
		outcome = "not_blocked"
	}
	h.audit(r.Context(), appID, uid, outcome, login)
	httpx.WriteOK(w, http.StatusOK, map[string]any{"appId": appID, "uid": uid, "removed": removed})
	return nil
}

// audit은 차단 조작을 감사 원장에 남긴다.
//
// uid를 detail이 아니라 전용 필드로 남기지 않는 이유는 감사 원장의
// puid 자리가 플랫폼 사용자 ID 전용이기 때문이다. 차단은 앱 범위
// 식별자를 다룬다.
func (h *blocksAdminHandler) audit(ctx context.Context, appID, uid, outcome, actor string) {
	if h.auditor == nil {
		return
	}
	h.auditor.Record(ctx, "identity.block", appID, "", outcome,
		map[string]any{"uid": uid, "actor": actor})
}
