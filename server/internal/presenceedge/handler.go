// Package presenceedge는 RPI에서 최근 활성 session을 MySQL projection으로
// 갱신한다. 장애 시 복구할 원장이 아니라 의도적으로 유실 가능한 관측 경로다.
package presenceedge

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"sync"
	"time"

	"github.com/seorilabs/platform/server/internal/httpx"
	"github.com/seorilabs/platform/server/internal/platformerr"
	"github.com/seorilabs/platform/server/internal/presence"
)

const (
	maxBodyBytes       = 1024
	minimumWritePeriod = 30 * time.Second
	limiterRetention   = 10 * time.Minute
	maxLimiterSessions = 100_000
)

type TokenVerifier interface {
	Verify(raw string) (presence.Token, error)
}

type Repository interface {
	Upsert(ctx context.Context, session Session) error
	Ping(ctx context.Context) error
}

type Session struct {
	AppID       string
	SessionHash string
	Platform    string
	AppVersion  string
	Sequence    int64
	LastSeenAt  time.Time
	ExpiresAt   time.Time
}

type Handler struct {
	verifier TokenVerifier
	repo     Repository
	now      func() time.Time
	limiter  *sessionLimiter
}

func NewHandler(verifier TokenVerifier, repo Repository) *Handler {
	return &Handler{
		verifier: verifier,
		repo:     repo,
		now:      time.Now,
		limiter:  newSessionLimiter(),
	}
}

func (h *Handler) WithClock(now func() time.Time) *Handler {
	h.now = now
	return h
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/presence/heartbeat", httpx.Wrap(h.heartbeat))
	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /health/ready", h.ready)
}

type heartbeatRequest struct {
	Version    int    `json:"version"`
	Sequence   int64  `json:"sequence"`
	Platform   string `json:"platform"`
	AppVersion string `json:"appVersion,omitempty"`
}

func (h *Handler) heartbeat(w http.ResponseWriter, r *http.Request) error {
	tokenString, err := httpx.BearerToken(r)
	if err != nil {
		return err
	}
	verified, err := h.verifier.Verify(tokenString)
	if err != nil {
		return platformerr.Wrap(err, platformerr.CodeAuthInvalid, "presence token을 확인할 수 없어요")
	}
	var req heartbeatRequest
	if err := decodeHeartbeat(w, r, &req); err != nil {
		return err
	}
	if req.Version != 1 || req.Sequence < 0 {
		return platformerr.New(platformerr.CodeRequestInvalid, "heartbeat version 또는 sequence가 올바르지 않아요")
	}
	if !validPlatform(req.Platform) {
		return platformerr.New(platformerr.CodePlatformInvalid, "platform이 올바르지 않아요")
	}
	if len(req.AppVersion) > 32 {
		return platformerr.New(platformerr.CodeRequestInvalid, "appVersion이 너무 길어요")
	}

	now := h.now().UTC()
	limiterKey := verified.AppID + ":" + verified.SessionHash
	allowed, saturated := h.limiter.Allow(limiterKey, now)
	if saturated {
		w.Header().Set("Retry-After", "300")
		return platformerr.New(platformerr.CodeRateLimited, "presence 수신이 혼잡해요")
	}
	if allowed {
		writeCtx, cancel := context.WithTimeout(r.Context(), 300*time.Millisecond)
		defer cancel()
		if err := h.repo.Upsert(writeCtx, Session{
			AppID:       verified.AppID,
			SessionHash: verified.SessionHash,
			Platform:    req.Platform,
			AppVersion:  req.AppVersion,
			Sequence:    req.Sequence,
			LastSeenAt:  now,
			ExpiresAt:   now.Add(presence.ActiveTTL),
		}); err != nil {
			// 실패한 쓰기가 rate limit 슬롯을 먹으면 안 된다. 기록을 남기면
			// 클라이언트가 곧바로 재시도해도 minimumWritePeriod 동안 조용히
			// 합쳐져 200으로 돌아가고, 그 구간의 presence가 통째로 사라진다.
			h.limiter.Forget(limiterKey)
			w.Header().Set("Retry-After", "300")
			return platformerr.Wrap(err, platformerr.CodePlatformUnavailable, "presence를 저장하지 못했어요")
		}
	}
	httpx.WriteOK(w, http.StatusOK, struct {
		AcceptedAt string `json:"acceptedAt"`
	}{AcceptedAt: now.Format(time.RFC3339Nano)})
	return nil
}

func (h *Handler) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 300*time.Millisecond)
	defer cancel()
	if err := h.repo.Ping(ctx); err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func decodeHeartbeat(w http.ResponseWriter, r *http.Request, dst any) error {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return platformerr.New(platformerr.CodeContentTypeInvalid, "Content-Type은 application/json이어야 해요")
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return platformerr.New(platformerr.CodeRequestTooLarge, "heartbeat 본문이 너무 커요")
		}
		return platformerr.New(platformerr.CodeRequestInvalid, "heartbeat 본문이 올바르지 않아요")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return platformerr.New(platformerr.CodeRequestInvalid, "heartbeat 본문이 하나가 아니에요")
	}
	return nil
}

func validPlatform(value string) bool {
	switch value {
	case "android", "ios", "web", "ait":
		return true
	default:
		return false
	}
}

type sessionLimiter struct {
	mu    sync.Mutex
	last  map[string]time.Time
	calls int
}

func newSessionLimiter() *sessionLimiter {
	return &sessionLimiter{last: make(map[string]time.Time)}
}

// Allow는 같은 token의 과도한 쓰기를 조용히 합친다. 정상 heartbeat보다 빠른
// 요청을 429로 돌리면 정상 클라이언트의 backoff까지 키우므로 성공으로 받되 DB를
// 갱신하지 않는다. 새 session이 상한을 넘길 때만 명시적으로 밀어낸다.
func (l *sessionLimiter) Allow(key string, now time.Time) (allowed, saturated bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls++
	if l.calls%1000 == 0 {
		for existing, seenAt := range l.last {
			if now.Sub(seenAt) > limiterRetention {
				delete(l.last, existing)
			}
		}
	}
	lastSeen, exists := l.last[key]
	if !exists && len(l.last) >= maxLimiterSessions {
		return false, true
	}
	if exists && now.Sub(lastSeen) < minimumWritePeriod {
		return false, false
	}
	l.last[key] = now
	return true, false
}

// Forget은 쓰기에 실패한 key의 기록을 지워 다음 요청이 다시 쓰기를 시도하게 한다.
func (l *sessionLimiter) Forget(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.last, key)
}
