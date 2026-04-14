package llms

import (
	"errors"
	"os"
	"strings"

	"github.com/asccclass/opengrab/llms/ollama"
	"github.com/ollama/ollama/api"
)

// Provider 定義了所有 LLM 供應商必須實作的方法
type Provider interface {
	Chat(messages []ollama.Message) (ollama.Message, error)
	Name() string
}

// ChatStreamFunc 定義了通用的 LLM 聊天函式簽名
type ChatStreamFunc func(modelName string, messages []ollama.Message, tools []api.Tool, opts ollama.Options, callback func(string)) (ollama.Message, error)

// GetProvider 回傳指定名稱的 Provider 函式
// 目前支援: "ollama" (預設), "copilot" (GitHub Copilot)
func GetProviderFunc(providerName string) (ChatStreamFunc, error) {
	switch strings.ToLower(providerName) {
	case "ollama", "": // 預設為 Ollama
		return ollama.ChatStream, nil
	default:
		return nil, errors.New("unsupported provider: " + providerName)
	}
}

// GetDefaultChatStream 回傳根據 PCAI_PROVIDER 環境變數設定的 ChatStreamFunc
// 供背景服務(排程、歸納、Heartbeat)使用，不再寫死 Ollama
func GetDefaultChatStream() ChatStreamFunc {
	provider := os.Getenv("Default_Provider")
	fn, err := GetProviderFunc(provider)
	if err != nil {
		// Fallback: Ollama
		fn, _ = GetProviderFunc("ollama")
	}
	return fn
}
