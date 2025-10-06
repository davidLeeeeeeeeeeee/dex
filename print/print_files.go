package main

import (
	"dex/logs"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// 硬编码开关：是否包含 _test.go 文件。
// 修改为 true 则打印 _test.go 文件，修改为 false 则跳过 _test.go 文件。
var includeTestFiles = false

// 要排除的相对目录列表（相对于根目录）
// 例如: []string{"vendor", "node_modules", ".git", "build"}
var excludeDirs = []string{
	"vendor",
	"node_modules",
	".git",
	"build",
	"dist",
	"data",
	"utils",
	"print",
	// 在这里添加更多要排除的目录
}

func main() {
	// 创建(或覆盖)一个输出文件
	file, err := os.Create("output_dex.txt")
	if err != nil {
		logs.Error("无法创建输出文件: %v", err)
	}
	defer file.Close()

	// 如果希望日志也一起重定向到文件，可以取消下面的注释
	// log.SetOutput(file)

	dir := `./` // 指定要遍历的目录
	printGoFiles(dir, file)
}

func printGoFiles(dir string, outFile *os.File) {
	files, err := os.ReadDir(dir)
	if err != nil {
		// 将错误信息写入 outFile
		fmt.Fprintf(outFile, "无法读取目录 %s: %v\n", dir, err)
		return
	}

	for _, f := range files {
		path := filepath.Join(dir, f.Name())
		if f.IsDir() {
			// 检查是否应该排除该目录
			if shouldExcludeDir(path) {
				fmt.Fprintf(outFile, "跳过排除的目录: %s\n", path)
				continue
			}
			// 如果是目录且不在排除列表中，则递归调用
			printGoFiles(path, outFile)
		} else {
			// 处理 .proto 文件，直接打印
			if strings.HasSuffix(f.Name(), ".proto") {
				printFile(path, outFile)
				continue
			}

			// 处理 .go 文件（排除 .pb.go 文件）
			if strings.HasSuffix(f.Name(), ".go") && !strings.HasSuffix(f.Name(), ".pb.go") {
				// 根据开关决定是否跳过 _test.go 文件
				if !includeTestFiles && strings.HasSuffix(f.Name(), "_test.go") && !strings.HasSuffix(f.Name(), "main_test.go") {
					continue
				}
				printFile(path, outFile)
			}
		}
	}
}

// shouldExcludeDir 检查给定的路径是否应该被排除
func shouldExcludeDir(path string) bool {
	// 清理路径，移除开头的 ./ 或 .\
	cleanPath := filepath.Clean(path)
	cleanPath = strings.TrimPrefix(cleanPath, ".")
	cleanPath = strings.TrimPrefix(cleanPath, string(filepath.Separator))

	// 将路径分割成部分
	pathParts := strings.Split(cleanPath, string(filepath.Separator))

	// 检查路径的任何部分是否在排除列表中
	for _, part := range pathParts {
		for _, excludeDir := range excludeDirs {
			if part == excludeDir {
				return true
			}
		}
	}

	// 也可以使用完整路径匹配（如果需要更精确的控制）
	for _, excludeDir := range excludeDirs {
		// 完整路径匹配
		if cleanPath == excludeDir {
			return true
		}
		// 路径前缀匹配
		if strings.HasPrefix(cleanPath, excludeDir+string(filepath.Separator)) {
			return true
		}
	}

	return false
}

func printFile(path string, outFile *os.File) {
	// 将文件路径写入输出文件
	fmt.Fprintf(outFile, "\n文件路径: %s\n", path)

	// 读取并写入文件内容
	content, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(outFile, "无法读取文件 %s: %v\n", path, err)
		return
	}
	fmt.Fprintf(outFile, "文件内容:\n%s\n", string(content))
}
