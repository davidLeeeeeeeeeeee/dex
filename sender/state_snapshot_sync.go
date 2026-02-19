package sender

import (
	"bytes"
	"dex/types"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

func (sm *SenderManager) FetchStateSnapshotShards(targetAddress string, targetHeight uint64) (*types.StateSnapshotShardsResponse, error) {
	ip, err := sm.AddressToIP(targetAddress)
	if err != nil {
		return nil, err
	}

	req := types.StateSnapshotShardsRequest{TargetHeight: targetHeight}
	var resp types.StateSnapshotShardsResponse
	if err := sm.postJSON(ip, "/statedb/snapshot/shards", req, &resp); err != nil {
		return nil, err
	}
	if resp.Error != "" {
		return nil, fmt.Errorf("remote error: %s", resp.Error)
	}
	return &resp, nil
}

func (sm *SenderManager) FetchStateSnapshotPage(targetAddress string, snapshotHeight uint64, shard string, pageSize int, pageToken string) (*types.StateSnapshotPageResponse, error) {
	ip, err := sm.AddressToIP(targetAddress)
	if err != nil {
		return nil, err
	}

	req := types.StateSnapshotPageRequest{
		SnapshotHeight: snapshotHeight,
		Shard:          shard,
		PageSize:       pageSize,
		PageToken:      pageToken,
	}
	var resp types.StateSnapshotPageResponse
	if err := sm.postJSON(ip, "/statedb/snapshot/page", req, &resp); err != nil {
		return nil, err
	}
	if resp.Error != "" {
		return nil, fmt.Errorf("remote error: %s", resp.Error)
	}
	return &resp, nil
}

func (sm *SenderManager) postJSON(targetIP, path string, reqObj interface{}, respObj interface{}) error {
	payload, err := json.Marshal(reqObj)
	if err != nil {
		return err
	}

	url := fmt.Sprintf("https://%s%s", targetIP, path)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := sm.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("status=%d body=%s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if len(body) == 0 {
		return fmt.Errorf("empty response body")
	}

	if err := json.Unmarshal(body, respObj); err != nil {
		return fmt.Errorf("decode json failed: %w", err)
	}
	return nil
}
