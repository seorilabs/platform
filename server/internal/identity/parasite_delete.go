package identity

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// 도감의 불변 페이지는 하위 문서이므로 부모 삭제로 없어지지 않는다.
// 토큰 차단 표식을 먼저 남기고 첫 페이지를 반복 비운다. 실패 시 워커가
// 같은 작업을 재시도하며, 게임 데이터가 모두 지워지기 전에는 Auth를 지우지 않는다.
func deleteParasiteSave(ctx context.Context, client *http.Client, project, uid string) error {
	database := "projects/" + url.PathEscape(project) + "/databases/(default)"
	documents := database + "/documents/"
	base := "https://firestore.googleapis.com/v1/"
	owner := documents + "parasite_users/" + url.PathEscape(uid)
	if _, err := parasiteDeletionRequest(ctx, client, http.MethodPatch, base+documents+"deleted_accounts/"+url.PathEscape(uid), []byte(`{"fields":{}}`)); err != nil {
		return err
	}
	for {
		// Immutable pages contain only payload. Mask that content out: deletion needs names,
		// and reading 200 full pages could exceed the bounded response before cleanup.
		body, err := parasiteDeletionRequest(ctx, client, http.MethodGet, base+owner+"/blobs?pageSize=200&mask.fieldPaths=deletionMetadata", nil)
		if err != nil {
			return err
		}
		var page struct {
			Documents []struct {
				Name string `json:"name"`
			} `json:"documents"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return fmt.Errorf("identity: cloud deletion page invalid")
		}
		if len(page.Documents) == 0 {
			break
		}
		writes := make([]map[string]string, 0, len(page.Documents))
		for _, document := range page.Documents {
			prefix := owner + "/blobs/"
			if !strings.HasPrefix(document.Name, prefix) {
				return fmt.Errorf("identity: cloud deletion target outside account")
			}
			digest := strings.TrimPrefix(document.Name, prefix)
			if _, err := hex.DecodeString(digest); err != nil || len(digest) != 64 {
				return fmt.Errorf("identity: cloud deletion page identity invalid")
			}
			writes = append(writes, map[string]string{"delete": document.Name})
		}
		payload, err := json.Marshal(map[string]any{"writes": writes})
		if err != nil {
			return err
		}
		if _, err := parasiteDeletionRequest(ctx, client, http.MethodPost, base+database+"/documents:commit", payload); err != nil {
			return err
		}
	}
	_, err := parasiteDeletionRequest(ctx, client, http.MethodDelete, base+owner+"/state/head", nil)
	return err
}

func parasiteDeletionRequest(ctx context.Context, client *http.Client, method, target string, payload []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("identity: cloud deletion transport failed")
	}
	defer func() { _ = response.Body.Close() }()
	if method == http.MethodDelete && response.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("identity: cloud deletion HTTP %d", response.StatusCode)
	}
	// A bounded page cannot contain an unbounded response, even if the provider misbehaves.
	body, err := io.ReadAll(io.LimitReader(response.Body, 4*1024*1024+1))
	if err != nil || len(body) > 4*1024*1024 {
		return nil, fmt.Errorf("identity: cloud deletion response unavailable")
	}
	return body, nil
}
