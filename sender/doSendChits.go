package sender

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
)

// doSendChits 执行Chits发送
func doSendChits(t *SendTask, client *http.Client) error {
	msg, ok := t.Message.(*chitsMessage)
	if !ok {
		return fmt.Errorf("doSendChits: message is not *chitsMessage, got %T", t.Message)
	}

	url := fmt.Sprintf("https://%s/chits", t.Target)
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
		return fmt.Errorf("doSendChits: status=%d, body=%s", resp.StatusCode, string(respData))
	}

	return nil
}
