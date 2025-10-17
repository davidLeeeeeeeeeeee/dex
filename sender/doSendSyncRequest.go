package sender

import (
	"bytes"
	"dex/db"
	"fmt"
	"io"
	"net/http"

	"google.golang.org/protobuf/proto"
)

// 执行同步请求
func doSendSyncRequest(t *SendTask, client *http.Client) error {
	msg, ok := t.Message.(*syncRequestMessage)
	if !ok {
		return fmt.Errorf("doSendSyncRequest: message is not *syncRequestMessage, got %T", t.Message)
	}

	var blocks []*db.Block

	for height := msg.fromHeight; height <= msg.toHeight; height++ {
		req := &db.GetBlockRequest{Height: height}
		data, err := proto.Marshal(req)
		if err != nil {
			continue
		}

		url := fmt.Sprintf("https://%s/getblock", t.Target)
		httpReq, err := http.NewRequest("POST", url, bytes.NewReader(data))
		if err != nil {
			continue
		}
		httpReq.Header.Set("Content-Type", "application/x-protobuf")

		resp, err := client.Do(httpReq)
		if err != nil {
			continue
		}

		if resp.StatusCode == http.StatusOK {
			respBytes, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err == nil {
				var blockResp db.GetBlockResponse
				if err := proto.Unmarshal(respBytes, &blockResp); err == nil && blockResp.Block != nil {
					blocks = append(blocks, blockResp.Block)
				}
			}
		} else {
			resp.Body.Close()
		}
	}

	if msg.onSuccess != nil && len(blocks) > 0 {
		msg.onSuccess(blocks)
	}

	return nil
}
