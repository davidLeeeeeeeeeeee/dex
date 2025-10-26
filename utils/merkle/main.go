// file: poh_chain_demo.go
package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"runtime"
	"sync"
	"time"
)

func main() {
	var (
		n         = flag.Int("n", 100000, "随机生成的 hash 字符串数量")
		rounds    = flag.Int("rounds", 1, "第一阶段：每个字符串独立重复哈希的轮数（>=1）")
		printK    = flag.Int("print", 3, "最后打印前K条随机字符串用于校验")
		showFinal = flag.Bool("show-final", true, "是否打印串行PoH链的最终根（十六进制）")
	)
	flag.Parse()

	if *n <= 0 || *rounds <= 0 {
		fmt.Println("参数非法：n 和 rounds 必须 > 0")
		return
	}

	fmt.Printf("=== 随机 + 并行独立哈希 + 串行PoH链 Demo ===\n")
	fmt.Printf("数量: %d, 第一阶段轮数: %d, CPU 核心: %d\n\n", *n, *rounds, runtime.NumCPU())

	// ---------------------------
	// 生成 N 条“hash字符串”（64位hex）
	// ---------------------------
	startGen := time.Now()
	raw := make([]byte, (*n)*32) // N * 32字节随机源
	if _, err := rand.Read(raw); err != nil {
		panic(err)
	}
	hashStrs := make([]string, *n) // 64位hex字符串
	for i := 0; i < *n; i++ {
		b := raw[i*32 : (i+1)*32]
		dst := make([]byte, hex.EncodedLen(len(b)))
		hex.Encode(dst, b)
		hashStrs[i] = string(dst)
	}
	elapsedGen := time.Since(startGen)
	fmt.Printf("[阶段0] 生成随机hash字符串完成，用时: %v\n", elapsedGen)

	// ---------------------------
	// 第一阶段：并行独立哈希
	//   digest[i] = SHA256(hashStrs[i]) 重复 rounds 次（后续轮对摘要再哈希）
	//   结果保存在 digests 中（每条32字节）
	// ---------------------------
	type Arr32 = [32]byte
	digests := make([]Arr32, *n)

	fmt.Println("[阶段1] 并行独立哈希开始...")
	start1 := time.Now()

	workers := runtime.GOMAXPROCS(0)
	var wg sync.WaitGroup
	ch := make(chan int, 1024)

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range ch {
				// 第一次：对 hex 字符串本身做 SHA256
				sum := sha256.Sum256([]byte(hashStrs[i]))
				// 后续 rounds-1 轮：对前一次摘要继续做 SHA256
				for r := 1; r < *rounds; r++ {
					sum = sha256.Sum256(sum[:])
				}
				digests[i] = sum
			}
		}()
	}
	for i := 0; i < *n; i++ {
		ch <- i
	}
	close(ch)
	wg.Wait()

	elapsed1 := time.Since(start1)
	totalHashes1 := int64(*n) * int64(*rounds)
	fmt.Printf("[阶段1] 完成。用时: %v，总哈希次数: %d，平均每次: ~%v\n",
		elapsed1, totalHashes1, time.Duration(int64(elapsed1)/totalHashes1))

	// ---------------------------
	// 第二阶段：串行 PoH 链
	//   chain0 = digests[0]
	//   对 i=1..N-1：chain = SHA256(chain || digests[i])
	//   严格单线程，统计耗时
	// ---------------------------
	if *n == 1 {
		fmt.Println("[阶段2] 只有1条数据，PoH链无需推进。")
	} else {
		fmt.Println("[阶段2] 串行PoH链开始（单线程）...")
		start2 := time.Now()

		var chain Arr32 = digests[0]
		var buf [64]byte // 拼接 chain(32) + digest(32)
		for i := 1; i < *n; i++ {
			copy(buf[0:32], chain[:])
			copy(buf[32:64], digests[i][:])
			chain = sha256.Sum256(buf[:])
		}

		elapsed2 := time.Since(start2)
		ops := float64(*n-1) / elapsed2.Seconds()
		fmt.Printf("[阶段2] 串行PoH链完成。用时: %v，步数: %d，吞吐: %.2f ops/s\n",
			elapsed2, *n-1, ops)

		if *showFinal {
			finalHex := hex.EncodeToString(chain[:])
			fmt.Printf("[阶段2] 最终PoH根: %s\n", finalHex)
		}
	}

	// 展示前K条原始“hash字符串”，便于校验
	if *printK > 0 {
		k := *printK
		if k > *n {
			k = *n
		}
		fmt.Printf("\n前 %d 条随机hash字符串样例：\n", k)
		for i := 0; i < k; i++ {
			fmt.Println(hashStrs[i])
		}
	}
}
