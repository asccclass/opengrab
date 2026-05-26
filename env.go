package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"
)

func loadEnvFiles(paths ...string) error {
	var loadErrs []string

	for _, path := range paths {
		file, err := os.Open(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			loadErrs = append(loadErrs, fmt.Sprintf("%s: %v", path, err))
			continue
		}

		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}

			key, value, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}

			key = strings.TrimSpace(key)
			value = strings.TrimSpace(value)
			value = strings.Trim(value, `"'`)
			if key == "" || os.Getenv(key) != "" {
				continue
			}

			if err := os.Setenv(key, value); err != nil {
				loadErrs = append(loadErrs, fmt.Sprintf("%s: %v", key, err))
			}
		}
		if err := scanner.Err(); err != nil {
			loadErrs = append(loadErrs, fmt.Sprintf("%s: %v", path, err))
		}
		if err := file.Close(); err != nil {
			loadErrs = append(loadErrs, fmt.Sprintf("%s: %v", path, err))
		}
	}

	if len(loadErrs) > 0 {
		return errors.New(strings.Join(loadErrs, "; "))
	}
	return nil
}
