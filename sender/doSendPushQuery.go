package sender

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
)

type pushQueryMsg struct {
	requestData []byte
}

func doSendPushQuery(t *SendTask, client *http.Client) error {
	pm, ok := t.Message.(*pushQueryMsg)
	if !ok {
		return fmt.Errorf("doSendPushQuery expect *pushQueryMsg, got %T", t.Message)
	}
	url := fmt.Sprintf("https://%s/pushquery", t.Target)
	req, _ := http.NewRequest("POST", url, bytes.NewReader(pm.requestData))
	req.Header.Set("Content-Type", "application/x-protobuf")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("pushquery status=%d: %s", resp.StatusCode, string(b))
	}
	// 不需要读取/反序列化 chits，等待对端异步POST /chits 过来，因为是异步的
	return nil
}
