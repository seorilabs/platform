package events

import (
	"context"
	"log/slog"
	"net/http"
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

// Handler는 이벤트 수집 HTTP 핸들러다.
type Handler struct {
	collector *Collector
	registry  *registry.Registry
	sessions  SessionResolver
}

func NewHandler(c *Collector, reg *registry.Registry, sessions SessionResolver) *Handler {
	return &Handler{collector: c, registry: reg, sessions: sessions}
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

	// 세션은 선택이다. 없거나 만료됐어도 익명으로 수집한다.
	var puid string
	if h.sessions != nil {
		if sess, err := h.sessions.Authenticate(r); err == nil {
			puid = sess.PlatformUserID
		}
	}

	rows, dropped := h.buildRows(r.Context(), app, req, puid)

	if len(rows) > 0 {
		if err := h.collector.Insert(r.Context(), rows); err != nil {
			return err
		}
	}

	httpx.WriteOK(w, http.StatusOK, ingestResponse{Accepted: len(rows), Dropped: dropped})
	return nil
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

		// allowlist 밖은 조용히 버린다. GA4로는 여전히 간다.
		// 비용과 QPS를 규모와 무관한 상수로 묶는 장치다.
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

		rows = append(rows, &Row{
			EventID:        truncateRunes(e.EventID, 64),
			ReceivedAt:     now,
			EventTS:        eventTS,
			AppID:          app.AppID,
			PlatformUserID: puid,
			GA4ClientID:    truncateRunes(req.Context.GA4ClientID, 64),
			SessionID:      truncateRunes(e.SessionID, 64),
			EventName:      stripped,
			Platform:       truncateRunes(req.Context.Platform, 16),
			AppVersion:     truncateRunes(req.Context.AppVersion, 32),
			Locale:         truncateRunes(req.Context.Locale, 16),
			Params:         NormalizeParams(e.Params),
			SDKVersion:     truncateRunes(req.Context.SDKVersion, 32),
		})
	}

	if dropped > 0 {
		slog.DebugContext(ctx, "이벤트 일부 폐기",
			"app_id", app.AppID, "dropped", dropped, "accepted", len(rows))
	}
	return rows, dropped
}
