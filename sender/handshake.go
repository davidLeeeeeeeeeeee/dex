package sender

import (
	"bytes"
	"crypto/ecdsa"
	"dex/db"
	"dex/utils"
	"fmt"
	"google.golang.org/protobuf/proto"
	"io"
	"net/http"
)

// Handshake2Step 两步获取随机challenge再完成握手
func Handshake2Step(serverAddr, clientID string, privKey *ecdsa.PrivateKey) error {
	// 1. 先拿 challenge
	challenge, err := GetChallenge(serverAddr, clientID)
	if err != nil {
		return fmt.Errorf("GetChallenge error: %v", err)
	}

	// 2. 拼message = clientID + challenge => 做ECDSA签名
	message := clientID + challenge
	signatureHex, err := SignMessage(privKey, message)
	if err != nil {
		return fmt.Errorf("SignMessage error: %v", err)
	}

	// 3. 准备proto
	pubPEM := utils.PublicKeyToPEM(&privKey.PublicKey)
	reqProto := &db.HandshakeRequest{
		ClientId:  clientID,
		PublicKey: pubPEM,
		Signature: signatureHex,
	}
	payloadBytes, err := proto.Marshal(reqProto)
	if err != nil {
		return err
	}

	// 4. 发 /handshake
	url := fmt.Sprintf("https://%s/handshake", serverAddr)
	client := CreateHttp3Client()
	req, err := http.NewRequest("POST", url, bytes.NewReader(payloadBytes))
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
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("handshake fail status=%d, body=%s", resp.StatusCode, string(bodyBytes))
	}

	// 5. 解析返回
	respBytes, _ := io.ReadAll(resp.Body)
	var respProto db.HandshakeResponse
	if err := proto.Unmarshal(respBytes, &respProto); err != nil {
		return fmt.Errorf("handshake response proto unmarshal error: %v", err)
	}

	if respProto.GetStatus() != "handshake_ok" {
		return fmt.Errorf("handshake not ok, status=%s", respProto.GetStatus())
	}
	return nil
}

func GetChallenge(serverAddr, clientID string) (string, error) {
	url := fmt.Sprintf("https://%s/handshake_challenge?client_id=%s", serverAddr, clientID)
	client := CreateHttp3Client()
	resp, err := client.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("GetChallenge status=%d, body=%s", resp.StatusCode, string(bodyBytes))
	}
	challengeBytes, _ := io.ReadAll(resp.Body)
	return string(challengeBytes), nil
}
