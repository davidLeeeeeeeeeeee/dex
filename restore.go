package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

func restoreComments(filePath string) {
	fmt.Printf("--- Processing %s ---\n", filePath)

	// Get HEAD content
	headCmd := exec.Command("git", "show", "HEAD:"+filePath)
	var headOut bytes.Buffer
	headCmd.Stdout = &headOut
	if err := headCmd.Run(); err != nil {
		fmt.Printf("Error running git show: %v\n", err)
		return
	}

	headLines := strings.Split(headOut.String(), "\n")

	// Read current content
	currData, err := os.ReadFile(filePath)
	if err != nil {
		fmt.Printf("Error reading file: %v\n", err)
		return
	}
	currLines := strings.Split(string(currData), "\n")

	fmt.Printf("Lines in HEAD: %d, Lines in current: %d\n", len(headLines), len(currLines))

	// Regex for comments
	commentRegex := regexp.MustCompile(`^(.*?)(\s*//.*)$`)

	newLines := make([]string, 0, len(currLines))
	limit := len(headLines)
	if len(currLines) < limit {
		limit = len(currLines)
	}

	for i := 0; i < limit; i++ {
		headLine := headLines[i]
		currLine := currLines[i]

		headMatch := commentRegex.FindStringSubmatch(headLine)
		currMatch := commentRegex.FindStringSubmatch(currLine)

		if len(headMatch) > 0 && len(currMatch) > 0 {
			headCode := headMatch[1]
			headComm := headMatch[2]
			currCode := currMatch[1]

			if strings.TrimSpace(headCode) == strings.TrimSpace(currCode) {
				newLines = append(newLines, headLine)
			} else {
				codePart := strings.TrimRight(currCode, " \t")
				newLines = append(newLines, codePart+headComm)
			}
		} else if len(headMatch) > 0 && len(currMatch) == 0 {
			// Possibly garbled comment
			if hasGarbage(currLine) || strings.Contains(currLine, "//") {
				if strings.Contains(currLine, "//") {
					parts := strings.SplitN(currLine, "//", 2)
					newLines = append(newLines, parts[0]+headMatch[2])
				} else {
					if strings.HasPrefix(strings.TrimSpace(headLine), "//") {
						newLines = append(newLines, headLine)
					} else {
						newLines = append(newLines, currLine)
					}
				}
			} else {
				newLines = append(newLines, currLine)
			}
		} else {
			newLines = append(newLines, currLine)
		}
	}

	if len(currLines) > len(headLines) {
		newLines = append(newLines, currLines[len(headLines):]...)
	}

	err = os.WriteFile(filePath, []byte(strings.Join(newLines, "\n")), 0644)
	if err != nil {
		fmt.Printf("Error writing file: %v\n", err)
	} else {
		fmt.Printf("Successfully restored comments in %s\n", filePath)
	}
}

func hasGarbage(s string) bool {
	for _, r := range s {
		if r > 127 {
			return true
		}
	}
	return false
}

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: go run restore.go <files...>")
		return
	}

	for _, arg := range os.Args[1:] {
		restoreComments(arg)
	}
}
