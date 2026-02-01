package sender

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
)

type pullQueryMsg struct {
	requestData []byte
}

func doSendPullQuery(t *SendTask, client *http.Client) error {
	pm, ok := t.Message.(*pullQueryMsg)
	if !ok {
		return fmt.Errorf("doSendPullQuery expect *pullQueryMsg, got %T", t.Message)
	}
	url := fmt.Sprintf("https://%s/pullquery", t.Target)
	req, _ := http.NewRequest("POST", url, bytes.NewReader(pm.requestData))
	req.Header.Set("Content-Type", "application/x-protobuf")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("pullquery status=%d: %s", resp.StatusCode, string(b))
	}
	// 完全读取 response body，确保连接可复用
	_, _ = io.Copy(io.Discard, resp.Body)
	// 不需要读取/反序列化 chits，等待对端异步POST /chits 过来
	return nil
}
