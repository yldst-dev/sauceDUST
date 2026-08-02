package qdrant

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

// 담을 장수 계산은 "원본은 디스크, 줄인 것만 메모리"를 전제로 합니다.
// 그 값을 넣기 전에 만든 컬렉션은 그 전제 밖에 있으므로 옮겨야 합니다.
func TestExistingCollectionMovesToDisk(t *testing.T) {
	client, collection := liveClient(t, true)
	ctx := context.Background()

	// 예전 방식으로, on_disk 없이 직접 만듭니다.
	body := map[string]any{
		"vectors": map[string]any{"size": 768, "distance": "Cosine"},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	if err := rawCall(ctx, client, http.MethodPut, "/collections/"+collection, raw); err != nil {
		t.Fatalf("예전 방식 컬렉션 생성 실패: %v", err)
	}
	if onDisk(t, client, collection) {
		t.Fatal("준비가 잘못됐습니다. 만들자마자 on_disk입니다")
	}

	// EnsureCollection이 기존 컬렉션을 만나면 옮겨야 합니다.
	if err := client.EnsureCollection(ctx, liveModel(collection, 768)); err != nil {
		t.Fatalf("기존 컬렉션 확인 실패: %v", err)
	}
	if !onDisk(t, client, collection) {
		t.Error("기존 컬렉션이 on_disk로 옮겨지지 않았습니다")
	}

	// 두 번 불러도 탈이 없어야 합니다.
	if err := client.EnsureCollection(ctx, liveModel(collection, 768)); err != nil {
		t.Fatalf("두 번째 확인에서 실패했습니다: %v", err)
	}
}

// 새로 만드는 컬렉션은 처음부터 on_disk여야 합니다.
func TestNewCollectionIsOnDisk(t *testing.T) {
	client, collection := liveClient(t, true)

	if err := client.EnsureCollection(context.Background(), liveModel(collection, 768)); err != nil {
		t.Fatalf("컬렉션 생성 실패: %v", err)
	}
	if !onDisk(t, client, collection) {
		t.Error("새 컬렉션이 on_disk가 아닙니다")
	}
}

func onDisk(t *testing.T, client *Client, collection string) bool {
	t.Helper()
	var payload struct {
		Result struct {
			Config struct {
				Params struct {
					Vectors struct {
						OnDisk *bool `json:"on_disk"`
					} `json:"vectors"`
				} `json:"params"`
			} `json:"config"`
		} `json:"result"`
	}
	err := client.call(context.Background(), http.MethodGet, "/collections/"+collection, nil, &payload)
	if err != nil {
		t.Fatalf("컬렉션 조회 실패: %v", err)
	}
	v := payload.Result.Config.Params.Vectors.OnDisk
	return v != nil && *v
}

// rawCall은 어댑터를 거치지 않고 원하는 본문을 그대로 보냅니다.
// 예전 판이 만들던 모양을 재현하려면 필요합니다.
func rawCall(ctx context.Context, client *Client, method, path string, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, method, client.base+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("content-type", "application/json")
	if client.apiKey != "" {
		req.Header.Set("api-key", client.apiKey)
	}
	resp, err := client.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("응답이 %d입니다", resp.StatusCode)
	}
	return nil
}
