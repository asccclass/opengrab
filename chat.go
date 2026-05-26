package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ChatRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Stream      bool      `json:"stream"`
	Temperature float64   `json:"temperature"`
	MaxTokens   int       `json:"max_tokens"`
}

type ChatChoice struct {
	Message struct {
		Content string `json:"content"`
	} `json:"message"`
	Delta struct {
		Content          string `json:"content"`
		ReasoningContent string `json:"reasoning_content"`
	} `json:"delta"`
	FinishReason *string `json:"finish_reason"`
}

type ChatResponse struct {
	Choices []ChatChoice `json:"choices"`
}

func createChatCompletion(ctx context.Context, config AppConfig, messages []Message) (string, error) {
	token, err := getToken()
	if err != nil {
		return "", err
	}

	reqBody := ChatRequest{
		Model:       config.ModelName,
		Messages:    messages,
		Stream:      true,
		Temperature: 0.7,
		MaxTokens:   20480,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	apiReq, err := http.NewRequestWithContext(ctx, http.MethodPost, config.APIBaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	apiReq.Header.Set("Content-Type", "application/json")
	apiReq.Header.Set("Authorization", "Bearer "+token)

	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
		Timeout: 60 * time.Second,
	}

	resp, err := client.Do(apiReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("API returned %s: %s", resp.Status, strings.TrimSpace(string(respBody)))
	}

	if !strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		var chatResp ChatResponse
		if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
			return "", err
		}
		if len(chatResp.Choices) == 0 {
			return "", fmt.Errorf("API returned no choices")
		}
		return chatResp.Choices[0].Message.Content, nil
	}

	var reply strings.Builder
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}

		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" {
			continue
		}
		if data == "[DONE]" {
			break
		}

		var streamResp ChatResponse
		if err := json.Unmarshal([]byte(data), &streamResp); err != nil {
			continue
		}
		if len(streamResp.Choices) == 0 {
			continue
		}
		reply.WriteString(streamResp.Choices[0].Delta.Content)
	}
	if err := scanner.Err(); err != nil && err != io.EOF {
		return "", err
	}

	return reply.String(), nil
}
