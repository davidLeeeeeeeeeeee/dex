package sender

import (
	"crypto/tls"
	"net/http"
	"sync"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
)

var (
	globalSessionCache = tls.NewLRUClientSessionCache(1280)
	globalHttp3Client  *http.Client
	once               sync.Once
)

func CreateHttp3Client() *http.Client {
	once.Do(func() {
		tlsCfg := &tls.Config{
			InsecureSkipVerify: true,
			MinVersion:         tls.VersionTLS13,
			MaxVersion:         tls.VersionTLS13,
			ClientSessionCache: globalSessionCache,
		}
		tr := &http3.Transport{
			TLSClientConfig: tlsCfg,
			// 这里是大写的 QUICConfig
			QUICConfig: &quic.Config{
				// 每 10 秒发一次心跳，防止 idle timeout
				KeepAlivePeriod: 10 * time.Second,
				// 可选：延长最大空闲超时
				MaxIdleTimeout: 5 * time.Minute,
			},
		}
		globalHttp3Client = &http.Client{
			Transport: tr,
		}
	})
	return globalHttp3Client
}

func LogErrorf(format string, v ...interface{}) error {
	// 这里可以自定义错误输出
	// 也可以直接使用 log.Printf
	return nil
}
