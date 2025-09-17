package handlers

type RequestPayload struct {
	String1 string `json:"string1"`
	String2 string `json:"string2"`
}

// HandshakePayload 用于握手请求的 JSON 格式
type HandshakePayload struct {
	ClientID  string `json:"client_id"`
	PublicKey string `json:"public_key"` // 客户端发送的公钥 PEM
	Signature string `json:"signature"`  // 对 (client_id + server_challenge) 的签名
}

var ServerChallenge = "server_challenge_123456"
