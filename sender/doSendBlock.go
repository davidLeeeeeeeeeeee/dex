package sender

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
)

// 执行区块发送
func doSendBlock(t *SendTask, client *http.Client) error {
	msg, ok := t.Message.(*blockMessage)
	if !ok {
		return fmt.Errorf("doSendBlock: message is not *blockMessage, got %T", t.Message)
	}

	url := fmt.Sprintf("https://%s/put", t.Target)
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
		return fmt.Errorf("doSendBlock: status=%d, body=%s", resp.StatusCode, string(respData))
	}

	return nil
}
