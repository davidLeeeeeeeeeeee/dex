package smt

// getBitAtFromMSB gets the bit at an offset from the most significant bit
func getBitAtFromMSB(data []byte, position int) int {
	if int(data[position/8])&(1<<(8-1-uint(position)%8)) > 0 {
		return 1
	}
	return 0
}

// setBitAtFromMSB sets the bit at an offset from the most significant bit
func setBitAtFromMSB(data []byte, position int) {
	n := int(data[position/8])
	n |= 1 << (8 - 1 - uint(position)%8)
	data[position/8] = byte(n)
}

func countSetBits(data []byte) int {
	count := 0
	for i := 0; i < len(data)*8; i++ {
		if getBitAtFromMSB(data, i) == 1 {
			count++
		}
	}
	return count
}

func countCommonPrefix(data1 []byte, data2 []byte) int {
	count := 0
	for i := 0; i < len(data1)*8; i++ {
		if getBitAtFromMSB(data1, i) == getBitAtFromMSB(data2, i) {
			count++
		} else {
			break
		}
	}
	return count
}

func emptyBytes(length int) []byte {
	b := make([]byte, length)
	return b
}

func reverseByteSlices(slices [][]byte) [][]byte {
	for left, right := 0, len(slices)-1; left < right; left, right = left+1, right-1 {
		slices[left], slices[right] = slices[right], slices[left]
	}

	return slices
}

// ============================================
// 16 叉 JMT Nibble 操作函数
// ============================================

// getNibbleAt 获取指定位置的 Nibble (0-15)
// path: Key 的哈希值 (通常为 32 字节)
// position: Nibble 位置 (0 到 len(path)*2-1)
// 返回: 0-15 的值
func getNibbleAt(path []byte, position int) byte {
	byteIndex := position / 2
	if position%2 == 0 {
		return path[byteIndex] >> 4 // 高 4 位
	}
	return path[byteIndex] & 0x0F // 低 4 位
}

// countCommonNibblePrefix 计算两个路径的公共 Nibble 前缀长度
// 返回: 从头开始连续相同的 Nibble 数量
func countCommonNibblePrefix(path1, path2 []byte) int {
	minLen := len(path1)
	if len(path2) < minLen {
		minLen = len(path2)
	}
	maxNibbles := minLen * 2

	count := 0
	for i := 0; i < maxNibbles; i++ {
		if getNibbleAt(path1, i) == getNibbleAt(path2, i) {
			count++
		} else {
			break
		}
	}
	return count
}

// nibbleSlice 从路径中提取指定范围的 Nibbles
// start: 起始位置 (包含)
// end: 结束位置 (不包含)
// 返回: Nibble 数组
func nibbleSlice(path []byte, start, end int) []byte {
	if start >= end {
		return nil
	}
	result := make([]byte, end-start)
	for i := start; i < end; i++ {
		result[i-start] = getNibbleAt(path, i)
	}
	return result
}

// nibblesToBytes 将 Nibble 数组转换回字节数组
// 如果 nibbles 长度为奇数，最后一个字节的低 4 位为 0
func nibblesToBytes(nibbles []byte) []byte {
	byteLen := (len(nibbles) + 1) / 2
	result := make([]byte, byteLen)
	for i, n := range nibbles {
		if i%2 == 0 {
			result[i/2] = n << 4
		} else {
			result[i/2] |= n
		}
	}
	return result
}
