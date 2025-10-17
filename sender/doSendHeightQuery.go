package sender

import (
	"bytes"
	"dex/db"
	"fmt"
	"io"
	"net/http"

	"google.golang.org/protobuf/proto"
)

// 执行高度查询
func doSendHeightQuery(t *SendTask, client *http.Client) error {
	msg, ok := t.Message.(*heightQueryMessage)
	if !ok {
		return fmt.Errorf("doSendHeightQuery: message is not *heightQueryMessage, got %T", t.Message)
	}

	url := fmt.Sprintf("https://%s/heightquery", t.Target)
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
		return fmt.Errorf("doSendHeightQuery: status=%d, body=%s", resp.StatusCode, string(respData))
	}

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	var heightResp db.HeightResponse
	if err := proto.Unmarshal(respBytes, &heightResp); err != nil {
		return fmt.Errorf("doSendHeightQuery: unmarshal HeightResponse fail: %v", err)
	}

	if msg.onSuccess != nil {
		msg.onSuccess(&heightResp)
	}

	return nil
}
