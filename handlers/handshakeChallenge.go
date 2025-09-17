// handlers/handshakeChallenge.go

package handlers

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"sync"
	"time"
	// 省略其它import
)

var (
	ChallengeMap = make(map[string]*ChallengeInfo)
	ChallengeMu  sync.Mutex
	// 可以自定义一个过期时间，比如 30秒或1分钟
	challengeLifetime = 30 * time.Second
)

type ChallengeInfo struct {
	Challenge   string
	CreatedTime time.Time
}

// 生成指定长度的随机hex字符串
func generateRandomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// handlers/handshakeChallenge.go

func HandleGetChallenge(w http.ResponseWriter, r *http.Request) {
	clientID := r.URL.Query().Get("client_id")
	if clientID == "" {
		http.Error(w, "missing client_id", http.StatusBadRequest)
		return
	}

	// 生成随机挑战
	challenge := generateRandomHex(8) // 8字节转16进制 => 长度16的字符串
	info := &ChallengeInfo{
		Challenge:   challenge,
		CreatedTime: time.Now(),
	}

	// 存入内存
	ChallengeMu.Lock()
	ChallengeMap[clientID] = info
	ChallengeMu.Unlock()

	// 返回给客户端
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(challenge))
}
