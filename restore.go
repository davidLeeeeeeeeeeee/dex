package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

func restoreFromLastCommit(filePath string) {
	fmt.Printf("--- Processing %s ---\n", filePath)

	// 获取 HEAD~1 (上一个提交) 的内容 - 这是正确的注释
	prevData, err := getGitFileContent("HEAD~1", filePath)
	if err != nil {
		fmt.Printf("Error getting prev content for %s: %v\n", filePath, err)
		return
	}
	prevLines := strings.Split(prevData, "\n")

	// 获取 HEAD (当前提交) 的内容 - 这是包含乱码和正确逻辑的内容
	currData, err := getGitFileContent("HEAD", filePath)
	if err != nil {
		fmt.Printf("Error getting current content for %s: %v\n", filePath, err)
		return
	}
	currLines := strings.Split(currData, "\n")

	// 注释匹配正则
	// 匹配 // 及其前面的所有内容
	commentRegex := regexp.MustCompile(`^(.*?)(\s*//.*)$`)

	newLines := make([]string, 0, len(currLines))
	limit := len(prevLines)
	if len(currLines) < limit {
		limit = len(currLines)
	}

	for i := 0; i < limit; i++ {
		prevLine := prevLines[i]
		currLine := currLines[i]

		prevMatch := commentRegex.FindStringSubmatch(prevLine)
		currMatch := commentRegex.FindStringSubmatch(currLine)

		// 判定是否存在乱码：通常乱码会导致非ASCII字符增多
		hasGarbage := false
		for _, r := range currLine {
			if r > 127 {
				hasGarbage = true
				break
			}
		}

		// 逻辑：
		// 1. 如果当前行有乱码
		// 2. 且 HEAD~1 这一行有注释
		if hasGarbage && len(prevMatch) > 0 {
			prevCode := prevMatch[1]
			prevComm := prevMatch[2]

			// 如果当前行也有注释（即使是乱码注释）
			if len(currMatch) > 0 {
				currCode := currMatch[1]
				// 检查代码部分是否一致。如果一致，直接用旧的整行。
				if strings.TrimSpace(currCode) == strings.TrimSpace(prevCode) {
					newLines = append(newLines, prevLine)
				} else {
					// 代码变了，保留当前代码部分，恢复旧注释
					newLines = append(newLines, strings.TrimRight(currCode, " \t")+prevComm)
				}
			} else {
				// 当前行因为乱码可能没匹配上正则，但如果它包含 //
				if strings.Contains(currLine, "//") {
					parts := strings.SplitN(currLine, "//", 2)
					newLines = append(newLines, strings.TrimRight(parts[0], " \t")+prevComm)
				} else {
					// 没有 // 但有乱码，且旧代码是纯注释行
					if strings.HasPrefix(strings.TrimSpace(prevLine), "//") {
						newLines = append(newLines, prevLine)
					} else {
						newLines = append(newLines, currLine)
					}
				}
			}
		} else {
			// 没有乱码或旧版本没注释，保持原样
			newLines = append(newLines, currLine)
		}
	}

	// 处理剩余行
	if len(currLines) > len(prevLines) {
		newLines = append(newLines, currLines[len(prevLines):]...)
	}

	// 写回磁盘
	err = os.WriteFile(filePath, []byte(strings.Join(newLines, "\n")), 0644)
	if err != nil {
		fmt.Printf("Error writing file %s: %v\n", filePath, err)
	} else {
		fmt.Printf("Successfully repaired %s\n", filePath)
	}
}

func getGitFileContent(revision, filePath string) (string, error) {
	cmd := exec.Command("git", "show", revision+":"+filePath)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return out.String(), nil
}

func main() {
	files := []string{
		"vm/balance_fix_test.go",
		"vm/convert_to_matching_order_test.go",
		"vm/executor.go",
		"vm/freeze_handler.go",
		"vm/frost_dkg_handlers.go",
		"vm/frost_vault_dkg_validation_signed.go",
		"vm/frost_vault_init.go",
		"vm/frost_withdraw_request.go",
		"vm/frost_withdraw_signed.go",
		"vm/issue_token_handler.go",
		"vm/miner_handler.go",
		"vm/order_handler.go",
		"vm/transfer_handler.go",
		"vm/witness_events.go",
		"vm/witness_handler.go",
	}

	for _, f := range files {
		restoreFromLastCommit(f)
	}
}
