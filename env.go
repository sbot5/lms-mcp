package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

func readEnvFile(path string) (map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read env file: %w", err)
	}
	defer file.Close()

	m := make(map[string]string)
	sc := bufio.NewScanner(file)

	lineNum := 0
	for sc.Scan() {
		lineNum++
		line := strings.TrimSpace(strings.TrimPrefix(sc.Text(), "\ufeff"))
		line = strings.TrimPrefix(line, "export ")

		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		k, v, ok := strings.Cut(line, "=")
		if !ok {
			// Never echo the line itself: it may hold the token.
			return nil, fmt.Errorf("read env file: line %d: missing '='", lineNum)
		}

		key := strings.TrimSpace(k)
		val := strings.TrimSpace(v)
		if strings.HasPrefix(val, "\"") || strings.HasPrefix(val, "'") {
			if len(val) < 2 || val[len(val)-1] != val[0] {
				return nil, fmt.Errorf("read env file: line %d: unterminated quote", lineNum)
			}
			val = val[1 : len(val)-1]
		}

		m[key] = val
	}

	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read env file: %w", err)
	}

	return m, nil
}
