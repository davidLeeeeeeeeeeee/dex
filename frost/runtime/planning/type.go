// frost/runtime/planning/type.go
package planning

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"

	"dex/pb"
)

// Job is the deterministic signing task planned from withdraw queue state.
type Job struct {
	JobID        string
	Chain        string
	Asset        string
	VaultID      uint32
	KeyEpoch     uint64
	WithdrawIDs  []string
	TemplateHash []byte
	TemplateData []byte
	FirstSeq     uint64
	IsComposite  bool
	SubJobs      []*Job
	Logs         []*pb.FrostPlanningLog
}

// generateJobID = H(chain || asset || vault_id || first_seq || template_hash || key_epoch)
func generateJobID(chain, asset string, vaultID uint32, firstSeq uint64, templateHash []byte, keyEpoch uint64) string {
	data := chain + "|" + asset + "|" +
		strconv.FormatUint(uint64(vaultID), 10) + "|" +
		strconv.FormatUint(firstSeq, 10) + "|" +
		hex.EncodeToString(templateHash) + "|" +
		strconv.FormatUint(keyEpoch, 10)
	hash := sha256.Sum256([]byte(data))
	return hex.EncodeToString(hash[:])
}

func parseAmount(amount string) uint64 {
	n, _ := strconv.ParseUint(amount, 10, 64)
	return n
}
