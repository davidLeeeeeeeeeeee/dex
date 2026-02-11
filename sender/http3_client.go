package sender

import (
	"crypto/tls"
	"dex/config"
	"net/http"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
)

// 创建非单例的 HTTP/3 客户端
func createHttp3Client(cfg *config.Config) *http.Client {
	if cfg == nil {
		cfg = config.DefaultConfig()
	}

	tlsCfg := &tls.Config{
		InsecureSkipVerify: true,
		MinVersion:         tls.VersionTLS13,
		MaxVersion:         tls.VersionTLS13,
		ClientSessionCache: tls.NewLRUClientSessionCache(128),
		// 添加ALPN协议支持
		NextProtos: []string{"h3", "h3-29", "h3-28", "h3-27"},
	}

	tr := &http3.Transport{
		TLSClientConfig: tlsCfg,
		QUICConfig: &quic.Config{
			KeepAlivePeriod: 10 * time.Second,
			MaxIdleTimeout:  5 * time.Minute,
			// 可选：添加0-RTT支持
			Allow0RTT: true,
		},
	}

	return &http.Client{
		Transport: tr,
		Timeout:   cfg.Network.ConnectionTimeout,
	}
}
