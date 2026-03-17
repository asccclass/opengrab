package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/sashabaranov/go-openai"
)

func main() {
	// ctx := context.Background()
	// 設定 OpenAI Client 並將 BaseURL 指向本地端的 Ollama
	// Ollama 不需要真實的 API Key，但 go-openai 套件要求字串不能為空，因此隨便填入 "ollama"
	config := openai.DefaultConfig("ollama")
	config.BaseURL = "http://172.18.124.210:11434/v1"
	client := openai.NewClientWithConfig(config)

	// 讀取 Agent.md 作為 System Prompt
	agentContent, err := os.ReadFile("Agent.md")
	if err != nil {
		fmt.Printf("讀取 Agent.md 失敗: %v\n", err)
		return
	}

	// 初始化對話紀錄
	messages := []openai.ChatCompletionMessage{
		{
			Role:    openai.ChatMessageRoleSystem,
			Content: string(agentContent),
		},
	}

	scanner := bufio.NewScanner(os.Stdin)

	// 外層迴圈：等待使用者輸入
	for {
		fmt.Print("\n 【】 ")
		if !scanner.Scan() {
			break
		}
		userInput := strings.TrimSpace(scanner.Text())
		if userInput == "" {
			continue
		}

		messages = append(messages, openai.ChatCompletionMessage{
			Role:    openai.ChatMessageRoleUser,
			Content: userInput,
		})

		// 內層迴圈：Agent 思考與執行指令
		for {
			req := openai.ChatCompletionRequest{
				Model:    "llama4:latest",
				Messages: messages,
			}

			resp, err := client.CreateChatCompletion(context.Background(), req)
			if err != nil {
				fmt.Printf("API 請求錯誤: %v\n", err)
				fmt.Println("請確認 Ollama 服務是否正在執行 (http://localhost:11434)")
				break
			}

			reply := resp.Choices[0].Message.Content
			messages = append(messages, openai.ChatCompletionMessage{
				Role:    openai.ChatMessageRoleAssistant,
				Content: reply,
			})

			replyTrimmed := strings.TrimSpace(reply)
			fmt.Println(replyTrimmed)

			// 如果回覆不是以 ":" 開頭，代表是一般對話，印出結果並跳出內層迴圈等待使用者下一次輸入
			if !strings.HasPrefix(replyTrimmed, "命令") {
				fmt.Println(replyTrimmed)
				break
			}

			// 如果回覆以 ":" 開頭，解析並執行終端機指令
			parts := strings.SplitN(replyTrimmed, ":", 2)
			if len(parts) < 2 {
				break
			}
			cmdStr := strings.TrimSpace(parts[1])

			fmt.Printf(">> [系統執行指令]: %s\n", cmdStr)

			// 在 Unix/Linux/macOS 環境下使用 sh -c 執行指令 (Windows 可視需求改為 cmd /c)
			cmd := exec.Command("sh", "-c", cmdStr)
			output, err := cmd.CombinedOutput()

			var commandResult string
			if err != nil {
				commandResult = fmt.Sprintf("錯誤: %v\n輸出: %s", err, string(output))
			} else {
				commandResult = string(output)
			}

			// 將執行結果加回對話中，讓 Agent 讀取並繼續處理
			messages = append(messages, openai.ChatCompletionMessage{
				Role:    openai.ChatMessageRoleUser,
				Content: commandResult,
			})
		}
	}
}
