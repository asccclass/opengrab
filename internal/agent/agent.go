package agent

import (
	"fmt"
	"os"
	"strings"

	"github.com/asccclass/opengrab/llms"
	"github.com/asccclass/opengrab/llms/ollama"
)

// Agent 封裝了對話邏輯、工具呼叫與 Session 管理
type Agent struct {
	//Session      *history.Session
	ModelName    string
	SystemPrompt string
	//Registry     *core.Registry
	Options  ollama.Options
	Provider llms.ChatStreamFunc
	// Logger       *SystemLogger // [NEW] 系統日誌
	//ActiveBuffer *history.ActiveBuffer
	//DailyLogger  *history.DailyLogger

	// Callbacks for UI interaction
	OnGenerateStart        func()
	OnModelMessageComplete func(content string)
	OnToolCall             func(name, args string)
	OnToolResult           func(result string)
	OnShortTermMemory      func(source, content string) // 短期記憶自動存入回調
	OnMemorySearch         func(query string) string    // 記憶預搜尋回調
	OnCheckPendingPlan     func() string                // 未完成任務檢查回調
	OnAcquireTaskLock      func() bool                  // 獲取任務鎖
	OnReleaseTaskLock      func()                       // 釋放任務鎖
	OnIsTaskLocked         func() bool                  // 檢查任務鎖
}

func NewAgent(providerName, modelName, systemPrompt string) *Agent {
	if providerName == "" {
		providerName = os.Getenv("Default_Provider")
	}
	defaultProvider, err := llms.GetProviderFunc(providerName)
	if err != nil {
		fmt.Println("Error getting provider: ", err)
		return nil
	}
	return &Agent{
		ModelName:    modelName,
		SystemPrompt: systemPrompt,
		Options: ollama.Options{
			Temperature: 0.7,
			TopP:        0.9,
		},
		Provider: defaultProvider,
	}
}

// SetModelConfig update the model and provider dynamically
func (a *Agent) SetModelConfig(modelName string, provider llms.ChatStreamFunc) {
	if modelName != "" {
		a.ModelName = modelName
	}
	if provider != nil {
		a.Provider = provider
	}
}

// toolNameToMemorySource 將工具名稱對應到短期記憶的來源分類
// 返回空字串表示不需要儲存
func toolNameToMemorySource(toolName string) string {
	sourceMap := map[string]string{
		"get_taiwan_weather": "weather",
		"manage_calendar":    "calendar",
		"manage_email":       "email",
		"web_search":         "search",
		"knowledge_search":   "knowledge_query",
	}
	if source, ok := sourceMap[toolName]; ok {
		return source
	}
	return ""
}

// extractJSONBlocks 透過計算大括號的數量，精確提取巢狀的 JSON 區塊
func extractJSONBlocks(text string) []string {
	var blocks []string
	startIdx := -1
	braceCount := 0

	for i, r := range text {
		switch r {
		case '{':
			if braceCount == 0 {
				startIdx = i
			}
			braceCount++
		case '}':
			braceCount--
			if braceCount == 0 && startIdx != -1 {
				blocks = append(blocks, text[startIdx:i+1])
				startIdx = -1
			} else if braceCount < 0 {
				braceCount = 0 // Ignore unmatched closing braces
			}
		}
	}
	return blocks
}

// Chat 處理使用者輸入，執行思考與工具呼叫迴圈
// onStream 是即時輸出 AI 回應的回調函式
func (a *Agent) Chat(input string, onStream func(string)) (string, error) {
	var currentResponse strings.Builder
	aiMsg, err := a.Provider(
		a.ModelName,
		[]ollama.Message{
			{
				Role:    "user",
				Content: input,
			},
		},
		nil,
		a.Options,
		func(content string) {
			currentResponse.WriteString(content)
			if onStream != nil {
				onStream(content)
			}
		},
	)

	if err != nil {
		/*
			// [LOG] 記錄錯誤
			if a.Logger != nil {
				a.Logger.LogError("AI 思考錯誤", err)
			}
		*/
		fmt.Println("AI 思考錯誤: ", err)
		return "", fmt.Errorf("AI 思考錯誤: %v", err)
	}

	// 累積最終回應 (移動到這裡，確保 fallback 處理完後再決定是否觸發回調)
	if aiMsg.Content != "" {
		content := strings.TrimSpace(aiMsg.Content)
		// 觸發訊息完成回調 (供 UI 渲染 Markdown)
		if a.OnModelMessageComplete != nil {
			a.OnModelMessageComplete(content)
		}
		return content, nil
	}
	return "", nil
}
