package main

import (
  "bytes"
  "crypto/tls"
  "fmt"
  "io"
  "net/http"
  "strings"

  "dex/pb"
  "google.golang.org/protobuf/proto"
)

func main() {
  req := &pb.LogsRequest{MaxLines: 20000}
  body, _ := proto.Marshal(req)
  tr := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
  c := &http.Client{Transport: tr}
  resp, err := c.Post("https://127.0.0.1:6000/logs", "application/x-protobuf", bytes.NewReader(body))
  if err != nil { panic(err) }
  defer resp.Body.Close()
  b, _ := io.ReadAll(resp.Body)
  var out pb.LogsResponse
  if err := proto.Unmarshal(b, &out); err != nil { panic(err) }

  keys := []string{"[Coordinator]", "[Participant]", "[SigningService]", "ROAST", "session failed", "context deadline exceeded", "StartSession", "nonce", "share"}
  printed := 0
  for i := len(out.Logs)-1; i >= 0; i-- {
    l := out.Logs[i]
    if l == nil { continue }
    msg := l.Message
    hit := false
    for _, k := range keys {
      if strings.Contains(msg, k) { hit = true; break }
    }
    if hit {
      fmt.Printf("[%s] [%s] %s\n", l.Timestamp, l.Level, msg)
      printed++
      if printed >= 150 { break }
    }
  }
  fmt.Printf("printed=%d\n", printed)
}
