package main

import (
    "bytes"
    "crypto/tls"
    "fmt"
    "io"
    "net/http"

    "dex/pb"
    "google.golang.org/protobuf/proto"
)

func main() {
    req := &pb.LogsRequest{MaxLines: 20000}
    body, _ := proto.Marshal(req)

    tr := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
    c := &http.Client{Transport: tr}

    resp, err := c.Post("https://127.0.0.1:6000/logs", "application/x-protobuf", bytes.NewReader(body))
    if err != nil {
        panic(err)
    }
    defer resp.Body.Close()
    b, _ := io.ReadAll(resp.Body)

    var out pb.LogsResponse
    if err := proto.Unmarshal(b, &out); err != nil {
        panic(err)
    }

    fmt.Printf("logs=%d\n", len(out.Logs))
    keywords := []string{"WithdrawWorker", "FrostManager", "Scan error", "Found pending withdraw", "ProcessOnce error", "started signing session", "signing session", "Created job", "mac verification failed", "decrypt share", "frost_withdraw_signed"}

    for _, k := range keywords {
        fmt.Printf("==== %s ====\n", k)
        count := 0
        for i := len(out.Logs) - 1; i >= 0; i-- {
            l := out.Logs[i]
            if l == nil {
                continue
            }
            if bytes.Contains([]byte(l.Message), []byte(k)) {
                fmt.Printf("[%s] [%s] %s\n", l.Timestamp, l.Level, l.Message)
                count++
                if count >= 25 {
                    break
                }
            }
        }
        if count == 0 {
            fmt.Println("(none)")
        }
    }
}
