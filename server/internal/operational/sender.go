package operational

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type Sender struct {
	url    string
	secret []byte
	client *http.Client
	now    func() time.Time
}

func NewSender(url string, secret []byte, client *http.Client) (*Sender, error) {
	if strings.TrimSpace(url) == "" || len(secret) < 32 {
		return nil, errors.New("operational: Backoffice URL과 32바이트 이상 서명키가 필요하다")
	}
	if client == nil {
		client = &http.Client{Timeout: 8 * time.Second}
	}
	return &Sender{url: url, secret: secret, client: client, now: time.Now}, nil
}

func (s *Sender) Send(ctx context.Context, event Event) error {
	if err := validateEvent(event); err != nil {
		return err
	}
	body, err := json.Marshal(struct {
		Version int `json:"version"`
		Event
	}{Version: 1, Event: event})
	if err != nil {
		return err
	}
	timestamp := strconv.FormatInt(s.now().UTC().Unix(), 10)
	mac := hmac.New(sha256.New, s.secret)
	_, _ = mac.Write([]byte(timestamp + "."))
	_, _ = mac.Write(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Seori-Timestamp", timestamp)
	req.Header.Set("X-Seori-Signature", "v1="+hex.EncodeToString(mac.Sum(nil)))
	res, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()
	// 짧은 JSON 응답을 비워 connection을 재사용한다. 본문에는 운영상 필요한
	// 정보가 없고 오류에도 상태 코드만 기록해 외부 payload가 로그로 새지 않게 한다.
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4<<10))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("operational: Backoffice HTTP %d", res.StatusCode)
	}
	return nil
}

type Dispatcher struct {
	repo   *Repository
	sender *Sender
}

func NewDispatcher(repo *Repository, sender *Sender) *Dispatcher {
	return &Dispatcher{repo: repo, sender: sender}
}

func (d *Dispatcher) Drain(ctx context.Context, limit int) (sent, failed int, err error) {
	if err := d.repo.RecoverExpired(ctx); err != nil {
		return 0, 0, err
	}
	for range limit {
		item, found, err := d.repo.ClaimNext(ctx)
		if err != nil {
			return sent, failed, err
		}
		if !found {
			break
		}
		if sendErr := d.sender.Send(ctx, item.Event); sendErr != nil {
			failed++
			if err := d.repo.Fail(ctx, item.EventID, item.LeaseID, sendErr.Error()); err != nil {
				return sent, failed, err
			}
			continue
		}
		if err := d.repo.Complete(ctx, item.EventID, item.LeaseID); err != nil {
			return sent, failed, err
		}
		sent++
	}
	return sent, failed, nil
}
