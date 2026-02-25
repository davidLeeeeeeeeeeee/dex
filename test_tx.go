package main

import (
	"bytes"
	"crypto/sha256"
	"dex/pb"
	"dex/utils"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"google.golang.org/protobuf/proto"
)

func main() {
	// From miner_keys.txt index 0
	privKHex := "39db477fa2907bf9cb50dab5fd2d7b6206e49ecfea99e6eedd109e1d5be94015"
	address := "bc1q8ra2f338djg0pzgms35tpkdxjx9lr39mkyw84y"

	// Fetch nonce
	nonceReq := map[string]string{"address": address}
	nonceBytes, _ := json.Marshal(nonceReq)
	resp, err := http.Post("http://127.0.0.1:8080/api/wallet/nonce", "application/json", bytes.NewBuffer(nonceBytes))
	if err != nil {
		panic(err)
	}
	defer resp.Body.Close()
	var nonceResp map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&nonceResp)
	nonce := uint64(nonceResp["nonce"].(float64)) + 1
	fmt.Printf("Nonce: %d\n", nonce)

	privKey, err := utils.ParseSecp256k1PrivateKey(privKHex)
	if err != nil {
		panic(err)
	}

	pubKeyBytes := utils.SerializePublicKey(privKey.PublicKey)

	base := &pb.BaseMessage{
		FromAddress: address,
		Nonce:       nonce,
		Fee:         "0",
		PublicKeys: &pb.PublicKeys{
			Keys: map[int32][]byte{
				int32(pb.SignAlgo_SIGN_ALGO_ECDSA_P256): pubKeyBytes,
			},
		},
	}
	tx := &pb.Transaction{
		Base:         base,
		To:           "bc1qtestaddress",
		TokenAddress: "FB",
		Amount:       "1.5",
	}

	anyTxDraft := &pb.AnyTx{
		Content: &pb.AnyTx_Transaction{
			Transaction: tx,
		},
	}

	draftBytes, err := proto.Marshal(anyTxDraft)
	if err != nil {
		panic(err)
	}

	hash := sha256.Sum256(draftBytes)
	txId := hex.EncodeToString(hash[:])
	fmt.Printf("TxId: %s\n", txId)

	sig, err := utils.Sign(privKey, draftBytes)
	if err != nil {
		panic(err)
	}

	base.TxId = txId
	base.Signature = sig

	finalBytes, err := proto.Marshal(anyTxDraft)
	if err != nil {
		panic(err)
	}

	submitReq := map[string]string{
		"tx": base64.StdEncoding.EncodeToString(finalBytes),
	}
	submitBytes, _ := json.Marshal(submitReq)
	sResp, err := http.Post("http://127.0.0.1:8080/api/wallet/submittx", "application/json", bytes.NewBuffer(submitBytes))
	if err != nil {
		panic(err)
	}
	defer sResp.Body.Close()
	sb, _ := io.ReadAll(sResp.Body)
	fmt.Printf("Submit Response: %s\n", string(sb))

	// poll receipt
	for i := 0; i < 5; i++ {
		rcReq := map[string]string{"tx_id": txId}
		rcb, _ := json.Marshal(rcReq)
		rResp, err := http.Post("http://127.0.0.1:8080/api/wallet/receipt", "application/json", bytes.NewBuffer(rcb))
		if err != nil {
			panic(err)
		}
		defer rResp.Body.Close()
		rb, _ := io.ReadAll(rResp.Body)
		fmt.Printf("Receipt Response: %s\n", string(rb))
	}
}
