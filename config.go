package main

import (
	"fmt"
	"os"
	"strings"
)

type AppConfig struct {
	APIBaseURL string
	ModelName  string
}

func getConfig() (AppConfig, error) {
	config := AppConfig{
		APIBaseURL: strings.TrimRight(strings.TrimSpace(os.Getenv("ASCS_API_BASE_URL")), "/"),
		ModelName:  strings.TrimSpace(os.Getenv("ASCS_MODEL_NAME")),
	}
	if config.APIBaseURL == "" {
		return AppConfig{}, fmt.Errorf("ASCS_API_BASE_URL is not set; set it in your shell, .env, or envfile")
	}
	if config.ModelName == "" {
		return AppConfig{}, fmt.Errorf("ASCS_MODEL_NAME is not set; set it in your shell, .env, or envfile")
	}
	return config, nil
}

func getToken() (string, error) {
	token := strings.TrimSpace(os.Getenv("ASCS_API_TOKEN"))
	if token == "" {
		return "", fmt.Errorf("ASCS_API_TOKEN is not set; set it in your shell, .env, or envfile as ASCS_API_TOKEN=your_token")
	}
	return token, nil
}
