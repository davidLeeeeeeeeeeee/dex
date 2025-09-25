package sender

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
)

// 实现对单个节点通过 HTTP/3 发送 inv 消息
func doSendTx(t *SendTask, client *http.Client) error {
	url := fmt.Sprintf("https://%s/tx", t.Target)
	req, err := http.NewRequest("POST", url, bytes.NewReader(t.Message.([]byte)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-protobuf")

	// 使用传入的 client 而非全局单例
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respData, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("doSendTx: status %d, body %s", resp.StatusCode, string(respData))
	}

	return nil
}
