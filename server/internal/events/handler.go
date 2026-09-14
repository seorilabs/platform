package events

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/seorilabs/platform/server/internal/httpx"
	"github.com/seorilabs/platform/server/internal/identity"
	"github.com/seorilabs/platform/server/internal/platformerr"
	"github.com/seorilabs/platform/server/internal/registry"
)

// SessionResolver는 요청에서 세션을 꺼낸다.
//
// 인터페이스를 여기에 두는 이유는 Handler가 소비자이기 때문이다.
// identity.Handler가 구현하고, 이벤트 수집은 세션이 없어도 동작한다.
type SessionResolver interface {
	Authenticate(r *http.Request) (identity.Session, error)
}

// GA4Sender는 Handler가 허용·정규화한 이벤트를 외부 GA4 sink로 전달한다.
// 인터페이스는 소비자인 Handler 쪽에 둔다.
type GA4Sender interface {
	Send(ctx context.Context, app registry.App, rows []*Row) error
}

// Handler는 이벤트 수집 HTTP 핸들러다.
type Handler struct {
	collector *Collector
	registry  *registry.Registry
	sessions  SessionResolver
	ga4       GA4Sender
}

func NewHandler(c *Collector, reg *registry.Registry, sessions SessionResolver) *Handler {
	return &Handler{collector: c, registry: reg, sessions: sessions}
}

func (h *Handler) WithGA4(sender GA4Sender) *Handler {
	h.ga4 = sender
	return h
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/events", httpx.Wrap(h.ingest))
}

type clientEvent struct {
	EventID   string         `json:"eventId"`
	Name      string         `json:"name"`
	TSUnixMS  int64          `json:"tsUnixMs"`
	SessionID string         `json:"sessionId"`
	Params    map[string]any `json:"params"`
}

type ingestRequest struct {
	Events []clientEvent `json:"events"`
	// Context는 배치 전체에 공통인 정보다.
	// 이벤트마다 반복하면 본문이 커진다.
	Context struct {
		Platform    string `json:"platform"`
		AppVersion  string `json:"appVersion"`
		Locale      string `json:"locale"`
		GA4ClientID string `json:"ga4ClientId"`
		SDKVersion  string `json:"sdkVersion"`
	} `json:"context"`
}

type ingestResponse struct {
	Accepted int `json:"accepted"`
	Dropped  int `json:"dropped"`
}

// ingest는 이벤트 배치를 받는다.
//
// 인증은 선택이다. 세션이 있으면 platform_user_id를 붙이고 없으면 익명이다.
// RemoteConfig 조회처럼 익명으로도 허용해야 하는 경로가 있기 때문이다.
//
// 부분 수락이다. 일부 이벤트가 거부돼도 200을 돌려준다.
// 클라이언트가 배치를 통째로 재전송하면 중복만 늘어난다.
func (h *Handler) ingest(w http.ResponseWriter, r *http.Request) error {
	var req ingestRequest
	if err := httpx.DecodeStrict(w, r, &req); err != nil {
		return err
	}

	if len(req.Events) == 0 {
		return platformerr.New(platformerr.CodeRequestInvalid, "이벤트가 비어 있어요")
	}
	if len(req.Events) > MaxBatchSize {
		return platformerr.Newf(platformerr.CodeEventBatchTooLarge,
			"한 번에 %d건까지 보낼 수 있어요", MaxBatchSize)
	}

	appID, err := httpx.Header(r, identity.AppHeader, platformerr.CodeRequestInvalid)
	if err != nil {
		return err
	}

	app, err := h.registry.GetUsable(r.Context(), appID)
	if err != nil {
		return err
	}
	if !app.FeatureEnabled("events") {
		// 기능이 꺼진 앱은 조용히 받아들이고 버린다.
		// 에러를 주면 클라이언트가 재시도해 무의미한 트래픽이 생긴다.
		httpx.WriteOK(w, http.StatusOK, ingestResponse{Accepted: 0, Dropped: len(req.Events)})
		return nil
	}

	// 삭제를 지원하는 앱은 모든 이벤트를 본인 신원에 연결한다. 차단된
	// 토큰을 익명으로 낮추면 삭제 대상에서 빠진 식별 데이터가 생긴다.
	var puid string
	if h.sessions != nil {
		sess, authErr := h.sessions.Authenticate(r)
		if authErr == nil {
			if sess.AppID != app.AppID {
				return platformerr.New(platformerr.CodeAuthForbidden, "앱과 세션이 일치하지 않아요")
			}
			puid = sess.PlatformUserID
		} else if app.FeatureEnabled("account_deletion") || platformerr.CodeOf(authErr) == platformerr.CodeAuthForbidden || platformerr.CodeOf(authErr) == platformerr.CodeUserBlocked {
			return authErr
		}
	}
	if app.FeatureEnabled("account_deletion") && puid == "" {
		return platformerr.New(platformerr.CodeAuthRequired, "인증된 계정이 필요해요")
	}

	rows, dropped := h.buildRows(r.Context(), app, req, puid)

	if len(rows) > 0 {
		if err := h.collector.Insert(r.Context(), rows); err != nil {
			return err
		}
		h.forwardGA4(r.Context(), app, rows)
	}

	httpx.WriteOK(w, http.StatusOK, ingestResponse{Accepted: len(rows), Dropped: dropped})
	return nil
}

// forwardGA4는 BigQuery 원장 적재와 앱 응답을 GA4 가용성에서 분리한다. 여기서 실패를
// 앱 재시도로 돌리면 이미 적재된 행이 중복되므로 운영 경고만 남기고 수락을 유지한다.
func (h *Handler) forwardGA4(ctx context.Context, app registry.App, rows []*Row) {
	if h.ga4 == nil {
		return
	}
	if err := h.ga4.Send(ctx, app, rows); err != nil {
		slog.WarnContext(ctx, "GA4 이벤트 중계 실패",
			"app_id", app.AppID,
			"event_count", len(rows),
			"code", platformerr.CodeOf(err),
		)
	}
}

func (h *Handler) buildRows(
	ctx context.Context,
	app registry.App,
	req ingestRequest,
	puid string,
) ([]*Row, int) {
	now := h.collector.Now()
	rows := make([]*Row, 0, len(req.Events))
	dropped := 0

	for _, e := range req.Events {
		name, ok := NormalizeEventName(e.Name)
		if !ok {
			dropped++
			continue
		}

		// 레지스트리 접두사를 벗긴다.
		// 플랫폼 테이블은 app_id 컬럼이 있어 접두사가 불필요하고,
		// 벗겨야 앱을 가로지르는 쿼리가 가능해진다.
		stripped := app.StripEventPrefix(name)

		// allowlist 밖은 조용히 버린다. 서버 GA4 중계를 쓰는 앱도 같은
		// allowlist를 따르므로 비용과 QPS가 규모와 무관한 상수로 묶인다.
		if !app.EventAllowed(stripped) && !app.EventAllowed(name) {
			dropped++
			continue
		}

		if e.EventID == "" {
			dropped++
			continue
		}

		eventTS := now
		if e.TSUnixMS > 0 {
			eventTS = h.collector.ClampEventTime(time.UnixMilli(e.TSUnixMS))
		}

		params := NormalizeParams(e.Params)
		sessionID := truncateRunes(e.SessionID, 64)
		if sessionID == "" {
			switch value := params["session_id"].(type) {
			case string:
				sessionID = truncateRunes(value, 64)
			case int64:
				sessionID = strconv.FormatInt(value, 10)
			}
		}

		rows = append(rows, &Row{
			EventID:        truncateRunes(e.EventID, 64),
			ReceivedAt:     now,
			EventTS:        eventTS,
			AppID:          app.AppID,
			PlatformUserID: puid,
			GA4ClientID:    truncateRunes(req.Context.GA4ClientID, 64),
			SessionID:      sessionID,
			EventName:      stripped,
			Platform:       truncateRunes(req.Context.Platform, 16),
			AppVersion:     truncateRunes(req.Context.AppVersion, 32),
			Locale:         truncateRunes(req.Context.Locale, 16),
			Params:         params,
			SDKVersion:     truncateRunes(req.Context.SDKVersion, 32),
		})
	}

	if dropped > 0 {
		slog.DebugContext(ctx, "이벤트 일부 폐기",
			"app_id", app.AppID, "dropped", dropped, "accepted", len(rows))
	}
	return rows, dropped
}
