package sender

import (
	"bytes"
	"dex/pb"
	"fmt"
	"io"
	"net/http"

	"google.golang.org/protobuf/proto"
)

// 执行通过ID获取区块的请求
func doSendGetBlockByID(t *SendTask, client *http.Client) error {
	msg, ok := t.Message.(*pullBlockByIDMessage)
	if !ok {
		return fmt.Errorf("doSendGetBlockByID: message is not *pullBlockByIDMessage, got %T", t.Message)
	}

	url := fmt.Sprintf("https://%s/getblockbyid", t.Target)
	req, err := http.NewRequest("POST", url, bytes.NewReader(msg.requestData))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-protobuf")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respData, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("doSendGetBlockByID: status=%d, body=%s", resp.StatusCode, string(respData))
	}

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	// 解析 GetBlockResponse
	var gbr pb.GetBlockResponse
	if err := proto.Unmarshal(respBytes, &gbr); err != nil {
		return fmt.Errorf("doSendGetBlockByID: bad GetBlockResponse: %w", err)
	}
	if gbr.Block == nil {
		return fmt.Errorf("doSendGetBlockByID: empty block in response")
	}
	if gbr.Block.BlockHash != msg.blockID {
		return fmt.Errorf("doSendGetBlockByID: returned block ID mismatch, want %s, got %s",
			msg.blockID, gbr.Block.BlockHash)
	}

	if msg.onSuccess != nil {
		msg.onSuccess(gbr.Block)
	}
	return nil
}
