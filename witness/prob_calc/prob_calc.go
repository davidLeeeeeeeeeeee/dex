package main

import (
	"fmt"
	"math"
)

func main() {
	// 定义初始参数
	totalBalls := 100.0 // 总球数
	ballsA := 20.0      // A球数量
	drawCount := 10     // 抽取的数量

	// 初始化概率为 1
	probability := 1.0

	// 模拟逐步抽球过程（不放回）
	// i 代表已经抽出的球的数量
	for i := 0; i < drawCount; i++ {
		// 当前池子中剩余的 A 球数量 = ballsA - i
		// 当前池子中剩余的总球数量 = totalBalls - i

		currentA := ballsA - float64(i)
		currentTotal := totalBalls - float64(i)

		// 计算当前这一步抽中 A 的概率，并乘到总概率中
		probability *= (currentA / currentTotal)
	}

	// 格式化输出结果
	fmt.Println("--- 计算结果 ---")
	fmt.Printf("总球数: %.0f, A球数: %.0f, 抽取: %d\n", totalBalls, ballsA, drawCount)
	fmt.Printf("全是 A 的概率 (科学计数法): %e\n", probability)
	fmt.Printf("全是 A 的概率 (小数形式): %.10f\n", probability)

	// 为了对比，计算一下如果是有放回抽样 (0.2^10)
	roughly := math.Pow(0.2, 10)
	fmt.Printf("对比：有放回抽样概率: %e\n", roughly)
}
