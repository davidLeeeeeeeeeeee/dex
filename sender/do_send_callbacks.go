package sender

import (
	"bytes"
	"compress/gzip"
	"dex/pb"
	"dex/types"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"google.golang.org/protobuf/proto"
)

func readProtobufResponseBody(resp *http.Response, op string) ([]byte, error) {
	var reader io.Reader = resp.Body
	if resp.Header.Get("Content-Encoding") == "gzip" {
		gzReader, err := gzip.NewReader(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("%s: cannot create gzip reader: %w", op, err)
		}
		defer gzReader.Close()
		reader = gzReader
	}

	respBytes, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("%s: read body error: %w", op, err)
	}
	return respBytes, nil
}

func postProtobufAndRead(t *SendTask, client *http.Client, op string, path string, payload []byte) ([]byte, error) {
	url := fmt.Sprintf("https://%s%s", t.Target, path)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-protobuf")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respData, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("%s: status=%d, body=%s", op, resp.StatusCode, string(respData))
	}

	return readProtobufResponseBody(resp, op)
}

// doSendHeightQuery 发送高度查询并回调响应。
func doSendHeightQuery(t *SendTask, client *http.Client) error {
	msg, ok := t.Message.(*heightQueryMessage)
	if !ok {
		return fmt.Errorf("doSendHeightQuery: message is not *heightQueryMessage, got %T", t.Message)
	}

	respBytes, err := postProtobufAndRead(t, client, "doSendHeightQuery", "/heightquery", msg.requestData)
	if err != nil {
		return err
	}

	var heightResp pb.HeightResponse
	if err := proto.Unmarshal(respBytes, &heightResp); err != nil {
		return fmt.Errorf("doSendHeightQuery: unmarshal HeightResponse fail: %v", err)
	}

	if msg.onSuccess != nil {
		msg.onSuccess(&heightResp)
	}
	return nil
}

// doSendSyncRequest 发送高度区间同步请求。
func doSendSyncRequest(t *SendTask, client *http.Client) error {
	msg, ok := t.Message.(*syncRequestMessage)
	if !ok {
		return fmt.Errorf("doSendSyncRequest: message is not *syncRequestMessage, got %T", t.Message)
	}

	reqPayload, err := json.Marshal(types.SyncBlocksRequest{
		FromHeight:    msg.fromHeight,
		ToHeight:      msg.toHeight,
		SyncShortMode: msg.syncShortMode,
	})
	if err != nil {
		return fmt.Errorf("doSendSyncRequest: marshal request failed: %w", err)
	}

	url := fmt.Sprintf("https://%s/getsyncblocks", t.Target)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(reqPayload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("doSendSyncRequest: read body failed: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("doSendSyncRequest: status=%d body=%s", resp.StatusCode, string(respBytes))
	}

	var syncResp types.SyncBlocksResponse
	if err := json.Unmarshal(respBytes, &syncResp); err != nil {
		return fmt.Errorf("doSendSyncRequest: decode json failed: %w", err)
	}
	if syncResp.Error != "" {
		return fmt.Errorf("doSendSyncRequest: remote error: %s", syncResp.Error)
	}
	if len(syncResp.Blocks) == 0 {
		return fmt.Errorf("doSendSyncRequest: no blocks fetched for heights %d-%d", msg.fromHeight, msg.toHeight)
	}

	if msg.onSuccessWithProofs != nil {
		msg.onSuccessWithProofs(&syncResp)
		return nil
	}

	if msg.onSuccess != nil {
		blocks := make([]*pb.Block, 0, len(syncResp.Blocks))
		for _, bundle := range syncResp.Blocks {
			if len(bundle.BlockBytes) == 0 {
				continue
			}
			var b pb.Block
			if err := proto.Unmarshal(bundle.BlockBytes, &b); err != nil {
				continue
			}
			blocks = append(blocks, &b)
		}
		if len(blocks) == 0 {
			return fmt.Errorf("doSendSyncRequest: decoded 0 blocks for heights %d-%d", msg.fromHeight, msg.toHeight)
		}
		msg.onSuccess(blocks)
	}
	return nil
}

// doSendGetBlock 按高度拉取区块并回调。
func doSendGetBlock(t *SendTask, client *http.Client) error {
	msg, ok := t.Message.(*pullBlockMessage)
	if !ok {
		return fmt.Errorf("doSendGetBlock: message is not *pullBlockMessage, got %T", t.Message)
	}

	respBytes, err := postProtobufAndRead(t, client, "doSendGetBlock", "/getblock", msg.requestData)
	if err != nil {
		return err
	}

	var blockResp pb.GetBlockResponse
	if err := proto.Unmarshal(respBytes, &blockResp); err != nil {
		return fmt.Errorf("doSendGetBlock: unmarshal GetBlockResponse err=%v", err)
	}
	if blockResp.Error != "" {
		return fmt.Errorf("doSendGetBlock: remote error: %s", blockResp.Error)
	}

	if msg.onSuccess != nil && blockResp.Block != nil {
		msg.onSuccess(blockResp.Block)
	}
	return nil
}

// doSendGetBlockByID 按区块 ID 拉取区块并校验返回 ID。
func doSendGetBlockByID(t *SendTask, client *http.Client) error {
	msg, ok := t.Message.(*pullBlockByIDMessage)
	if !ok {
		return fmt.Errorf("doSendGetBlockByID: message is not *pullBlockByIDMessage, got %T", t.Message)
	}

	respBytes, err := postProtobufAndRead(t, client, "doSendGetBlockByID", "/getblockbyid", msg.requestData)
	if err != nil {
		return err
	}

	var gbr pb.GetBlockResponse
	if err := proto.Unmarshal(respBytes, &gbr); err != nil {
		return fmt.Errorf("doSendGetBlockByID: bad GetBlockResponse: %w", err)
	}
	if gbr.Block == nil {
		return fmt.Errorf("doSendGetBlockByID: empty block in response")
	}
	if gbr.Block.BlockHash != msg.blockID {
		return fmt.Errorf("doSendGetBlockByID: returned block ID mismatch, want %s, got %s", msg.blockID, gbr.Block.BlockHash)
	}

	if msg.onSuccess != nil {
		msg.onSuccess(gbr.Block)
	}
	return nil
}

// doSendBatchGetTxs 批量获取交易并回调。
func doSendBatchGetTxs(t *SendTask, client *http.Client) error {
	msg, ok := t.Message.(*pullBatchTxMessage)
	if !ok {
		return fmt.Errorf("doSendBatchGetTxs: message is not *pullBatchTxMessage, got %T", t.Message)
	}

	respBytes, err := postProtobufAndRead(t, client, "doSendBatchGetTxs", "/batchgetdata", msg.requestData)
	if err != nil {
		return err
	}

	var respMsg pb.BatchGetShortTxResponse
	if err := proto.Unmarshal(respBytes, &respMsg); err != nil {
		return fmt.Errorf("doSendBatchGetTxs: unmarshal BatchGetDataResponse failed: %v", err)
	}

	if msg.onSuccess != nil {
		msg.onSuccess(respMsg.Transactions)
	}
	return nil
}
