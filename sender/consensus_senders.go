package sender

import (
	"bytes"
	"dex/db"
	"fmt"
	"io"
	"net/http"

	"google.golang.org/protobuf/proto"
)

// ============= Message Types =============

// chitsMessage 用于发送Chits响应
type chitsMessage struct {
	requestData []byte
}

// blockMessage 用于发送完整区块
type blockMessage struct {
	requestData []byte
}

// heightQueryMessage 用于发送高度查询
type heightQueryMessage struct {
	requestData []byte
	onSuccess   func(*db.HeightResponse)
}

// syncRequestMessage 用于同步请求
type syncRequestMessage struct {
	requestData []byte
	fromHeight  uint64
	toHeight    uint64
	onSuccess   func([]*db.Block)
}

// ============= Send Functions =============

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

// doSendBlock 执行区块发送
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

// doSendHeightQuery 执行高度查询
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

// doSendSyncRequest 执行同步请求
func doSendSyncRequest(t *SendTask, client *http.Client) error {
	msg, ok := t.Message.(*syncRequestMessage)
	if !ok {
		return fmt.Errorf("doSendSyncRequest: message is not *syncRequestMessage, got %T", t.Message)
	}

	var blocks []*db.Block

	for height := msg.fromHeight; height <= msg.toHeight; height++ {
		req := &db.GetBlockRequest{Height: height}
		data, err := proto.Marshal(req)
		if err != nil {
			continue
		}

		url := fmt.Sprintf("https://%s/getblock", t.Target)
		httpReq, err := http.NewRequest("POST", url, bytes.NewReader(data))
		if err != nil {
			continue
		}
		httpReq.Header.Set("Content-Type", "application/x-protobuf")

		resp, err := client.Do(httpReq)
		if err != nil {
			continue
		}

		if resp.StatusCode == http.StatusOK {
			respBytes, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err == nil {
				var blockResp db.GetBlockResponse
				if err := proto.Unmarshal(respBytes, &blockResp); err == nil && blockResp.Block != nil {
					blocks = append(blocks, blockResp.Block)
				}
			}
		} else {
			resp.Body.Close()
		}
	}

	if msg.onSuccess != nil && len(blocks) > 0 {
		msg.onSuccess(blocks)
	}

	return nil
}
