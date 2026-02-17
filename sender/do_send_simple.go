package sender

import (
	"bytes"
	"dex/logs"
	"errors"
	"fmt"
	"io"
	"net/http"
)

func postProtobufNoReply(t *SendTask, client *http.Client, op string, path string, payload []byte) error {
	url := fmt.Sprintf("https://%s%s", t.Target, path)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
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
		return fmt.Errorf("%s: status=%d, body=%s", op, resp.StatusCode, string(respData))
	}

	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

// doSendTx 发送交易消息到 /tx。
func doSendTx(t *SendTask, client *http.Client) error {
	payload, ok := t.Message.([]byte)
	if !ok {
		return fmt.Errorf("doSendTx: message is not []byte, got %T", t.Message)
	}

	url := fmt.Sprintf("https://%s/tx", t.Target)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
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
		return &httpStatusError{
			op:         "doSendTx",
			statusCode: resp.StatusCode,
			body:       string(respData),
		}
	}

	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

// doSendBlock 发送区块消息到 /put。
func doSendBlock(t *SendTask, client *http.Client) error {
	msg, ok := t.Message.(*blockMessage)
	if !ok {
		return fmt.Errorf("doSendBlock: message is not *blockMessage, got %T", t.Message)
	}
	return postProtobufNoReply(t, client, "doSendBlock", "/put", msg.requestData)
}

// doSendChits 发送共识 Chits 到 /chits。
func doSendChits(t *SendTask, client *http.Client) error {
	msg, ok := t.Message.(*chitsMessage)
	if !ok {
		return fmt.Errorf("doSendChits: message is not *chitsMessage, got %T", t.Message)
	}
	return postProtobufNoReply(t, client, "doSendChits", "/chits", msg.requestData)
}

// doSendPushQuery 发送 PushQuery 到 /pushquery。
func doSendPushQuery(t *SendTask, client *http.Client) error {
	msg, ok := t.Message.(*pushQueryMsg)
	if !ok {
		return fmt.Errorf("doSendPushQuery: message is not *pushQueryMsg, got %T", t.Message)
	}
	// 不需要反序列化响应，等待对端异步 POST /chits。
	return postProtobufNoReply(t, client, "doSendPushQuery", "/pushquery", msg.requestData)
}

// doSendPullQuery 发送 PullQuery 到 /pullquery。
func doSendPullQuery(t *SendTask, client *http.Client) error {
	msg, ok := t.Message.(*pullQueryMsg)
	if !ok {
		return fmt.Errorf("doSendPullQuery: message is not *pullQueryMsg, got %T", t.Message)
	}
	// 不需要反序列化响应，等待对端异步 POST /chits。
	return postProtobufNoReply(t, client, "doSendPullQuery", "/pullquery", msg.requestData)
}

// doSendToOnePeer 发送 gossip payload 到 /gossipAnyMsg。
func doSendToOnePeer(t *SendTask, client *http.Client) error {
	msgBytes, ok := t.Message.([]byte)
	if !ok {
		return fmt.Errorf("doSendToOnePeer: message is not []byte, got %T", t.Message)
	}
	if t.Target == "" {
		return errors.New("doSendToOnePeer: empty target ip")
	}

	if err := postProtobufNoReply(t, client, "doSendToOnePeer", "/gossipAnyMsg", msgBytes); err != nil {
		return err
	}

	logs.Trace("[Gossip] success to %s, messageLen=%d", t.Target, len(msgBytes))
	return nil
}
