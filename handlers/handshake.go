// handlers/handshake.go

package handlers

import (
	"dex/db"
	"dex/logs"
	"dex/utils"
	"google.golang.org/protobuf/proto"
	"io"
	"net/http"
	"strings"
	"time"
)

func HandleHandshake(w http.ResponseWriter, r *http.Request) {
	// 1. 读取二进制请求体 => HandshakeRequest
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read handshake request body", http.StatusInternalServerError)
		return
	}
	var reqProto db.HandshakeRequest
	if err := proto.Unmarshal(body, &reqProto); err != nil {
		http.Error(w, "Invalid proto payload", http.StatusBadRequest)
		return
	}

	clientID := reqProto.GetClientId()
	signatureHex := reqProto.GetSignature()
	pubKeyPEM := reqProto.GetPublicKey()

	// 2. 在 ChallengeMap 里拿出对应的 challenge
	ChallengeMu.Lock()
	info, ok := ChallengeMap[clientID]
	if ok {
		// 检查是否过期
		if time.Since(info.CreatedTime) > challengeLifetime {
			// 已经过期
			delete(ChallengeMap, clientID)
			ok = false
		}
	}
	ChallengeMu.Unlock()

	if !ok {
		http.Error(w, "No valid challenge for this client_id (maybe expired or not requested)", http.StatusUnauthorized)
		return
	}

	// 3. 构造要验签的 message
	message := clientID + info.Challenge

	// 4. 校验签名
	pubKey, err := utils.DecodePublicKey(pubKeyPEM)
	if err != nil {
		http.Error(w, "Invalid public key", http.StatusBadRequest)
		return
	}
	if !utils.VerifySignature(pubKey, message, signatureHex) {
		logs.Debug("Handshake failed: invalid signature from clientID=%s\n", clientID)
		http.Error(w, "Invalid signature", http.StatusUnauthorized)
		return
	}

	// 5. 认证通过 => 写 ClientInfo
	clientIP := strings.Split(r.RemoteAddr, ":")[0]
	dbMgr, err := db.NewManager("")
	if err != nil {
		http.Error(w, "DB not available", http.StatusInternalServerError)
		return
	}

	infoDB := &db.ClientInfo{
		Ip:           clientIP,
		Authed:       true,
		PublicKeyPem: pubKeyPEM,
	}
	if err := db.SaveClientInfo(dbMgr, infoDB); err != nil {
		http.Error(w, "Failed to save client info", http.StatusInternalServerError)
		return
	}

	// 6. 用完就把 ChallengeMap 里的记录删掉
	ChallengeMu.Lock()
	delete(ChallengeMap, clientID)
	ChallengeMu.Unlock()

	// 返回
	resProto := &db.HandshakeResponse{
		Status: "handshake_ok",
	}
	resBytes, err := proto.Marshal(resProto)
	if err != nil {
		http.Error(w, "Failed to marshal handshake response", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.WriteHeader(http.StatusOK)
	w.Write(resBytes)
}
