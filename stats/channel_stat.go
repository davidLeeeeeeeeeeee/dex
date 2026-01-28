package stats

// ChannelStat 单个 channel 的状态
type ChannelStat struct {
	Name   string  `json:"name"`   // channel 名称
	Module string  `json:"module"` // 所属模块
	Len    int     `json:"len"`    // 当前长度
	Cap    int     `json:"cap"`    // 容量
	Usage  float64 `json:"usage"`  // 使用率 (len/cap)
}

// NewChannelStat 创建并计算使用率
func NewChannelStat(name, module string, length, capacity int) ChannelStat {
	usage := 0.0
	if capacity > 0 {
		usage = float64(length) / float64(capacity)
	}
	return ChannelStat{
		Name:   name,
		Module: module,
		Len:    length,
		Cap:    capacity,
		Usage:  usage,
	}
}
