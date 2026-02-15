package sender

import (
	"bytes"
	"dex/logs"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// doSendToOnePeer posts a binary gossip payload to peer /gossipAnyMsg.
func doSendToOnePeer(t *SendTask, client *http.Client) error {
	msgBytes, ok := t.Message.([]byte)
	if !ok {
		return fmt.Errorf("doSendToOnePeer: message is not []byte, got %T", t.Message)
	}
	if t.Target == "" {
		return errors.New("doSendToOnePeer: empty target ip")
	}

	url := fmt.Sprintf("https://%s/gossipAnyMsg", t.Target)
	req, err := http.NewRequest("POST", url, bytes.NewReader(msgBytes))
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
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("doSendToOnePeer: status=%d, target=%s, resp=%s",
			resp.StatusCode, t.Target, string(respBody))
	}

	logs.Trace("[Gossip] success to %s, messageLen=%d", t.Target, len(msgBytes))
	return nil
}
