package workers

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	"dex/frost/chain"
	"dex/frost/chain/btc"
	"dex/frost/runtime/planning"
	"dex/keys"
	"dex/logs"
	"dex/pb"

	"google.golang.org/protobuf/proto"
)

type testStateReader struct {
	data map[string][]byte
}

func newTestStateReader() *testStateReader {
	return &testStateReader{data: make(map[string][]byte)}
}

func (r *testStateReader) Get(key string) ([]byte, bool, error) {
	v, ok := r.data[key]
	if !ok {
		return nil, false, nil
	}
	return v, true, nil
}

func (r *testStateReader) Scan(prefix string, fn func(k string, v []byte) bool) error {
	for k, v := range r.data {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			if !fn(k, v) {
				break
			}
		}
	}
	return nil
}

type testTxSubmitter struct {
	withdrawTx *pb.FrostWithdrawSignedTx
}

func (s *testTxSubmitter) Submit(tx any) (txID string, err error) {
	return "test", nil
}
func (s *testTxSubmitter) SubmitDkgCommitTx(ctx context.Context, tx *pb.FrostVaultDkgCommitTx) error {
	return nil
}
func (s *testTxSubmitter) SubmitDkgShareTx(ctx context.Context, tx *pb.FrostVaultDkgShareTx) error {
	return nil
}
func (s *testTxSubmitter) SubmitDkgValidationSignedTx(ctx context.Context, tx *pb.FrostVaultDkgValidationSignedTx) error {
	return nil
}
func (s *testTxSubmitter) SubmitWithdrawSignedTx(ctx context.Context, tx *pb.FrostWithdrawSignedTx) error {
	s.withdrawTx = tx
	return nil
}

type testSigningService struct {
	result *SignedPackage
	err    error
}

func (s *testSigningService) StartSigningSession(ctx context.Context, params *SigningSessionParams) (sessionID string, err error) {
	return "session-test", nil
}
func (s *testSigningService) GetSessionStatus(sessionID string) (*SessionStatus, error) {
	return &SessionStatus{SessionID: sessionID, State: "COMPLETE", Progress: 1}, nil
}
func (s *testSigningService) CancelSession(sessionID string) error { return nil }
func (s *testSigningService) WaitForCompletion(ctx context.Context, sessionID string, timeout time.Duration) (*SignedPackage, error) {
	return s.result, s.err
}

type testLogReporter struct{}

func (r *testLogReporter) ReportWithdrawPlanningLog(ctx context.Context, log *pb.FrostWithdrawPlanningLogTx) error {
	return nil
}

func testTaprootScript(seed byte) []byte {
	out := make([]byte, 34)
	out[0] = 0x51
	out[1] = 0x20
	for i := 2; i < len(out); i++ {
		out[i] = seed
	}
	return out
}

func buildTestBTCTemplate(t *testing.T, vaultID uint32, keyEpoch uint64) (*btc.BTCTemplate, []byte, []chain.UTXO) {
	t.Helper()

	inputs := []chain.UTXO{
		{
			TxID:   "1111111111111111111111111111111111111111111111111111111111111111",
			Vout:   0,
			Amount: 120,
		},
		{
			TxID:   "2222222222222222222222222222222222222222222222222222222222222222",
			Vout:   1,
			Amount: 80,
		},
	}

	params := chain.WithdrawTemplateParams{
		Chain:       "btc",
		Asset:       "BTC",
		VaultID:     vaultID,
		KeyEpoch:    keyEpoch,
		WithdrawIDs: []string{"wd_1"},
		Outputs: []chain.WithdrawOutput{
			{WithdrawID: "wd_1", To: "bc1qrecipient", Amount: 150},
		},
		Inputs: inputs,
		Fee:    50,
	}

	template, err := btc.NewBTCTemplate(params)
	if err != nil {
		t.Fatalf("build template failed: %v", err)
	}

	templateData, err := template.ToJSON()
	if err != nil {
		t.Fatalf("serialize template failed: %v", err)
	}
	return template, templateData, inputs
}

func mustStoreUtxo(t *testing.T, reader *testStateReader, vaultID uint32, txid string, vout uint32, amount string, spk []byte) {
	t.Helper()
	key := keys.KeyFrostBtcUtxo(vaultID, txid, vout)
	data, err := proto.Marshal(&pb.FrostUtxo{
		Txid:           txid,
		Vout:           vout,
		Amount:         amount,
		VaultId:        vaultID,
		FinalizeHeight: 1,
		ScriptPubkey:   spk,
	})
	if err != nil {
		t.Fatalf("marshal utxo failed: %v", err)
	}
	reader.data[key] = data
}

func TestWithdrawWorkerExtractMessagesBTC(t *testing.T) {
	reader := newTestStateReader()
	vaultID := uint32(7)
	keyEpoch := uint64(1)
	template, templateData, _ := buildTestBTCTemplate(t, vaultID, keyEpoch)

	spkByInput := make(map[string][]byte)
	for i, in := range template.Inputs {
		spk := testTaprootScript(byte(0x10 + i))
		mustStoreUtxo(t, reader, vaultID, in.TxID, in.Vout, fmt.Sprintf("%d", in.Amount), spk)
		spkByInput[fmt.Sprintf("%s:%d", in.TxID, in.Vout)] = spk
	}

	w := &WithdrawWorker{stateReader: reader}
	job := &planning.Job{
		JobID:        "job_extract_btc",
		Chain:        "btc",
		VaultID:      vaultID,
		KeyEpoch:     keyEpoch,
		TemplateData: templateData,
	}

	got, err := w.extractMessages(job)
	if err != nil {
		t.Fatalf("extractMessages failed: %v", err)
	}

	scriptPubkeys := make([][]byte, 0, len(template.Inputs))
	for _, in := range template.Inputs {
		scriptPubkeys = append(scriptPubkeys, spkByInput[fmt.Sprintf("%s:%d", in.TxID, in.Vout)])
	}
	want, err := template.ComputeTaprootSighash(scriptPubkeys, btc.SighashDefault)
	if err != nil {
		t.Fatalf("compute expected sighashes failed: %v", err)
	}

	if len(got) != len(want) {
		t.Fatalf("message count mismatch: got=%d want=%d", len(got), len(want))
	}
	for i := range want {
		if !bytes.Equal(got[i], want[i]) {
			t.Fatalf("message[%d] mismatch", i)
		}
	}
}

func TestWithdrawWorkerWaitAndSubmitBTCIncludesTemplateFields(t *testing.T) {
	reader := newTestStateReader()
	submitter := &testTxSubmitter{}
	vaultID := uint32(9)
	keyEpoch := uint64(2)
	template, templateData, _ := buildTestBTCTemplate(t, vaultID, keyEpoch)

	spkByInput := make(map[string][]byte)
	for i, in := range template.Inputs {
		spk := testTaprootScript(byte(0x30 + i))
		mustStoreUtxo(t, reader, vaultID, in.TxID, in.Vout, fmt.Sprintf("%d", in.Amount), spk)
		spkByInput[fmt.Sprintf("%s:%d", in.TxID, in.Vout)] = spk
	}

	sig1 := bytes.Repeat([]byte{0x11}, 64)
	sig2 := bytes.Repeat([]byte{0x22}, 64)
	signingSvc := &testSigningService{
		result: &SignedPackage{
			SessionID:  "sess-1",
			JobID:      "job_submit_btc",
			Signatures: [][]byte{sig1, sig2},
		},
	}

	w := &WithdrawWorker{
		stateReader:    reader,
		txSubmitter:    submitter,
		logReporter:    &testLogReporter{},
		signingService: signingSvc,
		localAddress:   "node-test",
		Logger:         logs.NewNodeLogger("worker-btc-test", 0),
	}

	job := &planning.Job{
		JobID:        "job_submit_btc",
		Chain:        "btc",
		Asset:        "BTC",
		VaultID:      vaultID,
		KeyEpoch:     keyEpoch,
		WithdrawIDs:  []string{"wd_1"},
		TemplateHash: template.TemplateHash(),
		TemplateData: templateData,
	}

	w.waitAndSubmit(context.Background(), job, "sess-1")

	tx := submitter.withdrawTx
	if tx == nil {
		t.Fatal("expected withdraw tx to be submitted")
	}
	if !bytes.Equal(tx.TemplateData, templateData) {
		t.Fatal("template_data mismatch")
	}
	if !bytes.Equal(tx.TemplateHash, job.TemplateHash) {
		t.Fatal("template_hash mismatch")
	}
	if len(tx.InputSigs) != 2 {
		t.Fatalf("input_sigs count mismatch: got=%d want=2", len(tx.InputSigs))
	}
	if !bytes.Equal(tx.InputSigs[0], sig1) || !bytes.Equal(tx.InputSigs[1], sig2) {
		t.Fatal("input_sigs content mismatch")
	}

	if len(tx.ScriptPubkeys) != len(template.Inputs) {
		t.Fatalf("script_pubkeys count mismatch: got=%d want=%d", len(tx.ScriptPubkeys), len(template.Inputs))
	}
	for i, in := range template.Inputs {
		wantSPK := spkByInput[fmt.Sprintf("%s:%d", in.TxID, in.Vout)]
		if !bytes.Equal(tx.ScriptPubkeys[i], wantSPK) {
			t.Fatalf("script_pubkeys[%d] mismatch", i)
		}
	}

	if len(tx.SignedPackageBytes) != 128 {
		t.Fatalf("signed package bytes length mismatch: got=%d want=128", len(tx.SignedPackageBytes))
	}
	if !bytes.Equal(tx.SignedPackageBytes[:64], sig1) || !bytes.Equal(tx.SignedPackageBytes[64:], sig2) {
		t.Fatal("signed package bytes content mismatch")
	}
}
