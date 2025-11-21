package main

import (
	"awesomeProject1/logs"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"go.dedis.ch/kyber/v3"
	"go.dedis.ch/kyber/v3/pairing/bn256"
	"go.dedis.ch/kyber/v3/sign/bls"
	"go.dedis.ch/kyber/v3/util/random"
)

// DataSet 表示每一组的最终数据：只存储“聚合后”的公钥、签名、消息
type DataSet struct {
	Message      []byte `json:"message"`
	AggPublicKey []byte `json:"agg_public_key"`
	AggSignature []byte `json:"agg_signature"`
}

// AllData 用于打包所有组写入 JSON
type AllData struct {
	Sets []DataSet `json:"sets"`
}

func main() {
	// 1. 使用 bn256 曲线的 BLS suite
	suite := bn256.NewSuite()

	// 2. 配置要生成多少组数据、每组多少参与方
	numSets := 1000
	numParticipants := 10
	allData := AllData{Sets: make([]DataSet, numSets)}

	// 用于统计“聚合公钥 + 聚合签名”这部分的总耗时
	var totalAggTime time.Duration

	for i := 0; i < numSets; i++ {
		// 2.1 生成一条随机消息
		msg := make([]byte, 32)
		if _, err := rand.Read(msg); err != nil {
			logs.Error("failed to generate random message: %v", err)
		}

		// 2.2 收集 10 个 (公钥, 签名)
		pubKeys := make([]kyber.Point, numParticipants)
		sigs := make([][]byte, numParticipants)

		for j := 0; j < numParticipants; j++ {
			// 把 io.Reader 包装成 cipher.Stream
			rng := random.New(rand.Reader)

			// 生成私钥、(公钥) 对
			privKey, pubKey := bls.NewKeyPair(suite, rng)

			// 对 msg 做签名
			signature, err := bls.Sign(suite, privKey, msg)
			if err != nil {
				logs.Error("failed to sign: %v", err)
			}

			pubKeys[j] = pubKey
			sigs[j] = signature
		}

		// 2.3 聚合公钥 + 聚合签名，并统计耗时
		startAgg := time.Now()

		aggPubKey := bls.AggregatePublicKeys(suite, pubKeys...)

		aggSig, err := bls.AggregateSignatures(suite, sigs...)
		if err != nil {
			logs.Error("failed to aggregate signatures: %v", err)
		}

		aggTime := time.Since(startAgg)
		totalAggTime += aggTime

		// 2.4 序列化聚合公钥
		aggPubKeyBytes, err := aggPubKey.MarshalBinary()
		if err != nil {
			logs.Error("failed to marshal agg public key: %v", err)
		}

		// 将“消息、聚合公钥、聚合签名”保存
		allData.Sets[i] = DataSet{
			Message:      msg,
			AggPublicKey: aggPubKeyBytes,
			AggSignature: aggSig,
		}
	}

	// 3. 写入 JSON 文件
	file, err := os.Create("bls_data.json")
	if err != nil {
		logs.Error("failed to create file: %v", err)
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(allData); err != nil {
		logs.Error("failed to encode JSON: %v", err)
	}

	fmt.Printf("Successfully generated %d groups of aggregated data.\n", numSets)
	fmt.Printf("Total aggregator time for %d groups: %v\n", numSets, totalAggTime)
	fmt.Printf("Average aggregator time per group: %v\n", totalAggTime/time.Duration(numSets))
	fmt.Println("Data stored in bls_data.json")
}
