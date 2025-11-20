package types

// 区块定义
type Block struct {
	ID            string
	Height        uint64
	ParentID      string
	Data          string
	Proposer      string
	Window        int    // 时间窗口（替代原来的Round）
	VRFProof      []byte // VRF证明
	VRFOutput     []byte // VRF输出
	BLSPublicKey  []byte // 提案者的BLS公钥（用于VRF验证）
}
