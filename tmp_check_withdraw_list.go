package main

import (
    "bytes"
    "crypto/tls"
    "fmt"
    "io"
    "net/http"
    "sort"

    "dex/pb"
    "google.golang.org/protobuf/proto"
)

func main() {
    req := &pb.FrostWithdrawListRequest{Chain: "", Asset: ""}
    body, _ := proto.Marshal(req)

    tr := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
    c := &http.Client{Transport: tr}

    resp, err := c.Post("https://127.0.0.1:6000/frost/withdraw/list", "application/x-protobuf", bytes.NewReader(body))
    if err != nil {
        panic(err)
    }
    defer resp.Body.Close()
    b, _ := io.ReadAll(resp.Body)

    var out pb.FrostWithdrawStateList
    if err := proto.Unmarshal(b, &out); err != nil {
        panic(err)
    }

    fmt.Printf("states=%d\n", len(out.States))
    if len(out.States) == 0 {
        return
    }

    seqs := make([]uint64, 0, len(out.States))
    byStatus := map[string]int{}
    for _, s := range out.States {
        if s == nil { continue }
        seqs = append(seqs, s.Seq)
        byStatus[s.Status]++
    }
    sort.Slice(seqs, func(i,j int) bool { return seqs[i] < seqs[j] })
    fmt.Printf("seq_min=%d seq_max=%d\n", seqs[0], seqs[len(seqs)-1])
    fmt.Printf("status=%v\n", byStatus)

    // print first 10 sorted by seq then txid
    sort.Slice(out.States, func(i,j int) bool {
        if out.States[i].Seq == out.States[j].Seq {
            return out.States[i].TxId < out.States[j].TxId
        }
        return out.States[i].Seq < out.States[j].Seq
    })

    n := 10
    if len(out.States) < n { n = len(out.States) }
    for i:=0; i<n; i++ {
        s := out.States[i]
        fmt.Printf("#%02d seq=%d status=%s tx=%s wid=%s job=%s\n", i+1, s.Seq, s.Status, s.TxId, s.WithdrawId, s.JobId)
    }
}
