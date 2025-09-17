package sender

import (
	"bytes"
	"io"
	"net/http"
)

// Transporter 是一个简单接口，抽象了 “发请求” 这件事。
// Send() 返回值是 (statusCode, responseBody, error)
type Transporter interface {
	Send(url string, data []byte, contentType string) (int, []byte, error)
}

// Http3Transport 是在生产环境中用的真实实现。
type Http3Transport struct {
	// 你可以放一些配置，比如TLS等
}

func NewHttp3Transport() *Http3Transport {
	return &Http3Transport{}
}

// Send 实际执行 HTTP/3 POST
func (t *Http3Transport) Send(url string, data []byte, contentType string) (int, []byte, error) {
	client := CreateHttp3Client()
	req, err := http.NewRequest("POST", url, nil)
	if err != nil {
		return 0, nil, err
	}

	// 放 data 到 body
	req.Body = io.NopCloser(bytes.NewReader(data))
	req.Header.Set("Content-Type", contentType)

	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()

	respData, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, respData, nil
}
