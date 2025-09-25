// 文件: sender/pull_block.go
package sender

import (
	"bytes"
	"compress/gzip"
	"dex/db"
	"fmt"
	"google.golang.org/protobuf/proto"
	"io"
	"net/http"
)

// pullBlockMessage 用来包装：要请求的 height + 回调函数
type pullBlockMessage struct {
	requestData []byte
	onSuccess   func(*db.Block)
}

// doSendGetBlock 真正执行 HTTP/3 POST /getblock 并解析返回
func doSendGetBlock(t *SendTask, client *http.Client) error {
	pm, ok := t.Message.(*pullBlockMessage)
	if !ok {
		return fmt.Errorf("doSendGetBlock: t.Message is not *pullBlockMessage")
	}

	url := fmt.Sprintf("https://%s/getblock", t.Target)

	req, err := http.NewRequest("POST", url, bytes.NewReader(pm.requestData))
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
		return fmt.Errorf("doSendGetBlock: status=%d, body=%s",
			resp.StatusCode, string(respData))
	}

	// 如果服务端使用 gzip 压缩，则需要解压
	var reader io.Reader = resp.Body
	if resp.Header.Get("Content-Encoding") == "gzip" {
		gzReader, err := gzip.NewReader(resp.Body)
		if err != nil {
			return fmt.Errorf("doSendGetBlock: cannot create gzip reader: %v", err)
		}
		defer gzReader.Close()
		reader = gzReader
	}

	// 读取响应体并反序列化
	respBytes, err := io.ReadAll(reader)
	if err != nil {
		return fmt.Errorf("doSendGetBlock: read body error: %v", err)
	}

	var blockResp db.GetBlockResponse
	if err := proto.Unmarshal(respBytes, &blockResp); err != nil {
		return fmt.Errorf("doSendGetBlock: unmarshal GetBlockResponse err=%v", err)
	}
	if blockResp.Error != "" {
		return fmt.Errorf("doSendGetBlock: remote error: %s", blockResp.Error)
	}

	// 调用 onSuccess 回调
	if pm.onSuccess != nil && blockResp.Block != nil {
		pm.onSuccess(blockResp.Block)
	}

	return nil
}
