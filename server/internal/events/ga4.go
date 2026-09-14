package events

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/seorilabs/platform/server/internal/platformerr"
	"github.com/seorilabs/platform/server/internal/registry"
)

const ga4MeasurementProtocolEndpoint = "https://www.google-analytics.com/mp/collect"

// MeasurementProtocol은 이벤트 수집기가 허용한 행을 GA4 Web stream으로 전달한다.
// api_secret은 이 구현 안에서도 로그나 오류 문자열에 넣지 않는다.
type MeasurementProtocol struct {
	secrets  map[string]string
	client   *http.Client
	endpoint string
}

func NewMeasurementProtocol(secrets map[string]string, client *http.Client) *MeasurementProtocol {
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	return &MeasurementProtocol{
		secrets:  secrets,
		client:   client,
		endpoint: ga4MeasurementProtocolEndpoint,
	}
}

type ga4Request struct {
	ClientID        string     `json:"client_id"`
	TimestampMicros int64      `json:"timestamp_micros,omitempty"`
	Consent         ga4Consent `json:"consent"`
	Events          []ga4Event `json:"events"`
}

type ga4Consent struct {
	AdUserData        string `json:"ad_user_data"`
	AdPersonalization string `json:"ad_personalization"`
}

type ga4Event struct {
	Name   string         `json:"name"`
	Params map[string]any `json:"params"`
}

func (m *MeasurementProtocol) Send(ctx context.Context, app registry.App, rows []*Row) error {
	if app.GA4.MeasurementID == "" || len(rows) == 0 {
		return nil
	}
	secret := strings.TrimSpace(m.secrets[app.AppID])
	if secret == "" {
		return platformerr.New(platformerr.CodeConfigUnavailable, "GA4 전송 설정을 확인하지 못했어요")
	}

	clientID := strings.TrimSpace(rows[0].GA4ClientID)
	if clientID == "" {
		// 구버전 클라이언트는 ga4ClientId가 없다. Platform 원장 적재는 유지하고
		// 새 버전이 보급될 때까지 GA4 전송만 건너뛴다.
		return nil
	}

	events := make([]ga4Event, 0, len(rows))
	var latest time.Time
	for _, row := range rows {
		if row == nil || row.GA4ClientID != clientID {
			continue
		}
		name := app.GA4.EventPrefix + row.EventName
		if normalized, ok := NormalizeEventName(name); ok {
			name = normalized
		} else {
			continue
		}
		params := make(map[string]any, len(row.Params)+6)
		for key, value := range row.Params {
			params[key] = value
		}
		params["platform"] = row.Platform
		params["app_version"] = row.AppVersion
		params["locale"] = row.Locale
		params["seori_event_id"] = row.EventID
		if _, exists := params["engagement_time_msec"]; !exists {
			params["engagement_time_msec"] = int64(1)
		}
		if sessionID, err := strconv.ParseInt(row.SessionID, 10, 64); err == nil && sessionID > 0 {
			params["session_id"] = sessionID
		}
		events = append(events, ga4Event{Name: name, Params: params})
		if row.EventTS.After(latest) {
			latest = row.EventTS
		}
	}
	if len(events) == 0 {
		return nil
	}

	payload := ga4Request{
		ClientID:        clientID,
		TimestampMicros: latest.UnixMicro(),
		Consent: ga4Consent{
			AdUserData:        "DENIED",
			AdPersonalization: "DENIED",
		},
		Events: events,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return platformerr.Wrap(err, platformerr.CodeInternal, "GA4 이벤트를 만들지 못했어요")
	}

	endpoint, err := url.Parse(m.endpoint)
	if err != nil {
		return platformerr.Wrap(err, platformerr.CodeInternal, "GA4 전송 경로를 만들지 못했어요")
	}
	query := endpoint.Query()
	query.Set("measurement_id", app.GA4.MeasurementID)
	query.Set("api_secret", secret)
	endpoint.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return platformerr.Wrap(err, platformerr.CodeInternal, "GA4 요청을 만들지 못했어요")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := m.client.Do(req)
	if err != nil {
		// *url.Error는 요청 URL 전체를 문자열에 넣는다. query에는 api_secret이 있으므로
		// 원본 오류를 감싸거나 로그에 넘기지 않는다.
		return platformerr.New(platformerr.CodeConfigUnavailable, "GA4에 이벤트를 보내지 못했어요")
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return platformerr.Wrap(
			fmt.Errorf("GA4 HTTP status %d", resp.StatusCode),
			platformerr.CodeConfigUnavailable,
			"GA4가 이벤트를 받지 못했어요",
		)
	}
	return nil
}
