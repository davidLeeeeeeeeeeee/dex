package main

import (
    "fmt"
    "log"
    "os"
    "sort"
    "strings"

    "github.com/cockroachdb/pebble"
)

func main() {
    path := os.Getenv("SCAN_PATH")
    if path == "" {
        path = "data/data_node_0/state/checkpoints/h_00000000000000000600"
    }
    db, err := pebble.Open(path, &pebble.Options{ReadOnly: true})
    if err != nil {
        log.Fatalf("open %s: %v", path, err)
    }
    defer db.Close()

    type hit struct {
        key string
        val []byte
    }

    prefixes := []string{
        "v1_frost_btc_utxo_seq_",
        "v1_frost_btc_utxo_head_",
        "v1_frost_vault_available_btc_BTC_",
    }

    counts := map[string]int{}
    for v := 0; v <= 4; v++ {
        counts[fmt.Sprintf("v1_frost_btc_utxo_%d_", v)] = 0
        counts[fmt.Sprintf("v1_frost_btc_utxo_q_%d_", v)] = 0
    }

    hits := make([]hit, 0)

    iter, err := db.NewIter(&pebble.IterOptions{LowerBound: []byte("v1_sdb_state_"), UpperBound: []byte("v1_sdb_state_~")})
    if err != nil {
        log.Fatalf("iter: %v", err)
    }
    defer iter.Close()

    for iter.First(); iter.Valid(); iter.Next() {
        k := string(iter.Key())
        rest := strings.TrimPrefix(k, "v1_sdb_state_")
        i := strings.Index(rest, "_")
        if i < 0 || i+1 >= len(rest) {
            continue
        }
        biz := rest[i+1:]

        for _, p := range prefixes {
            if strings.HasPrefix(biz, p) {
                v := append([]byte(nil), iter.Value()...)
                hits = append(hits, hit{key: biz, val: v})
                break
            }
        }
        for p := range counts {
            if strings.HasPrefix(biz, p) {
                counts[p]++
            }
        }
    }
    if err := iter.Error(); err != nil {
        log.Fatalf("iter err: %v", err)
    }

    sort.Slice(hits, func(i, j int) bool { return hits[i].key < hits[j].key })

    fmt.Printf("scan path: %s\n", path)
    fmt.Println("=== key values ===")
    for _, h := range hits {
        fmt.Printf("%s = %q\n", h.key, string(h.val))
    }

    fmt.Println("=== utxo counts by vault ===")
    keys := make([]string, 0, len(counts))
    for k := range counts {
        keys = append(keys, k)
    }
    sort.Strings(keys)
    for _, k := range keys {
        fmt.Printf("%s -> %d\n", k, counts[k])
    }
}
