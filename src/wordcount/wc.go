package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func PrintLineCount(patterns []string) {
	var totalLineCount, totalWordCount, totalCharCount int
	fileCount := 0

	for _, pattern := range patterns {
		files, err := filepath.Glob(pattern)
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
		for _, filename := range files {
			fileCount++
			lineCount, wordCount, charCount := CountLineWordChars(filename)
			PrintInfo(lineCount, wordCount, charCount, "["+filename+"]")

			totalLineCount += lineCount
			totalWordCount += wordCount
			totalCharCount += charCount
		}
	}

	if fileCount > 1 {
		PrintInfo(totalLineCount, totalWordCount, totalCharCount, "Total")
	}
}

func PrintWithComma(n int) string {
	s := fmt.Sprintf("%d", n)

	if len(s) <= 3 {
		return s
	}

	start := len(s) % 3
	if start == 0 {
		start = 3
	}

	var result strings.Builder
	result.WriteString(s[:start])
	for i := start; i < len(s); i += 3 {
		result.WriteString(",")
		result.WriteString(s[i : i+3])
	}
	return result.String()
}

func PrintInfo(lineCount int, wordCount int, charCount int, tail string) {
	fmt.Printf("%12s %12s %15s %s\n",
		PrintWithComma(lineCount), PrintWithComma(wordCount), PrintWithComma(charCount), tail)
}

func CountLineWordChars(filename string) (int, int, int) {
	file, err := os.Open(filename)
	if err != nil {
		fmt.Printf("Error opening file %s: %v\n", filename, err)
		os.Exit(1)
	}

	var lineCount, wordCount, charCount int

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		lineCount++
		charCount += len(line)
		wordCount += len(strings.Fields(line))
	}

	if err := scanner.Err(); err != nil {
		fmt.Printf("Error reading file %s: %v\n", filename, err)
		os.Exit(1)
	}

	file.Close()
	return lineCount, wordCount, charCount
}

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: wc <file>")
		os.Exit(0)
	}

	if os.Args[1] == "ver" || os.Args[1] == "version" {
		fmt.Println("WC version 1.0 written by chobocho")
		os.Exit(0)
	}

	PrintLineCount(os.Args[1:])
}
