package sender

import (
	"bytes"
	"compress/gzip"
	"dex/pb"
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

	var blocks []*pb.Block
	var lastErr error

	for height := msg.fromHeight; height <= msg.toHeight; height++ {
		req := &pb.GetBlockRequest{Height: height}
		data, err := proto.Marshal(req)
		if err != nil {
			lastErr = err
			continue
		}

		url := fmt.Sprintf("https://%s/getblock", t.Target)
		httpReq, err := http.NewRequest("POST", url, bytes.NewReader(data))
		if err != nil {
			lastErr = err
			continue
		}
		httpReq.Header.Set("Content-Type", "application/x-protobuf")

		resp, err := client.Do(httpReq)
		if err != nil {
			lastErr = err
			continue
		}

		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("status=%d", resp.StatusCode)
			resp.Body.Close()
			continue
		}

		var reader io.Reader = resp.Body
		if resp.Header.Get("Content-Encoding") == "gzip" {
			gzReader, err := gzip.NewReader(resp.Body)
			if err != nil {
				resp.Body.Close()
				lastErr = fmt.Errorf("cannot create gzip reader: %w", err)
				continue
			}
			reader = gzReader
			respBytes, err := io.ReadAll(reader)
			gzReader.Close()
			resp.Body.Close()
			if err != nil {
				lastErr = err
				continue
			}
			var blockResp pb.GetBlockResponse
			if err := proto.Unmarshal(respBytes, &blockResp); err != nil {
				lastErr = err
				continue
			}
			if blockResp.Block != nil {
				blocks = append(blocks, blockResp.Block)
			}
			continue
		}

		respBytes, err := io.ReadAll(reader)
		resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}
		var blockResp pb.GetBlockResponse
		if err := proto.Unmarshal(respBytes, &blockResp); err != nil {
			lastErr = err
			continue
		}
		if blockResp.Block != nil {
			blocks = append(blocks, blockResp.Block)
		}
	}

	if msg.onSuccess != nil && len(blocks) > 0 {
		msg.onSuccess(blocks)
		return nil
	}

	if len(blocks) == 0 && lastErr != nil {
		return fmt.Errorf("no blocks fetched for heights %d-%d: %v", msg.fromHeight, msg.toHeight, lastErr)
	}
	if len(blocks) == 0 {
		return fmt.Errorf("no blocks fetched for heights %d-%d", msg.fromHeight, msg.toHeight)
	}
	return nil
}
