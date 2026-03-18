package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/asccclass/opengrab/tools/cmdrunner"
	"github.com/joho/godotenv"
	"github.com/sashabaranov/go-openai"
)

func main() {
	if err := godotenv.Load("envfile"); err != nil {
		fmt.Println(err.Error())
		return
	}
	// 設定 OpenAI Client 並將 BaseURL 指向本地端的 Ollama
	// Ollama 不需要真實的 API Key，但 go-openai 套件要求字串不能為空，因此隨便填入 "ollama"
	config := openai.DefaultConfig("ollama")
	config.BaseURL = os.Getenv("ollamaUrl") + "/v1"
	client := openai.NewClientWithConfig(config)

	// 讀取 Agent.md 作為 System Prompt
	agentContent, err := os.ReadFile("Agent.md")
	if err != nil {
		fmt.Printf("讀取 Agent.md 失敗: %v\n", err)
		return
	}
	skillsContent, err := os.ReadFile("skills.md")
	if err != nil {
		fmt.Printf("讀取 skills.md 失敗: %v\n", err)
		return
	}

	// 註冊要監聽的訊號：SIGINT (Ctrl+C) 與 SIGTERM (如 Docker stop)
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	// 初始化對話紀錄
	messages := []openai.ChatCompletionMessage{
		{
			Role:    openai.ChatMessageRoleSystem,
			Content: string(agentContent) + "\n" + string(skillsContent),
		},
	}

	scanner := bufio.NewScanner(os.Stdin)
	ctx := context.Background()

	// 外層迴圈：等待使用者輸入
	for {
		fmt.Print("\n 【你】 ")
		if !scanner.Scan() {
			break
		}
		userInput := strings.TrimSpace(scanner.Text())
		if userInput == "" {
			continue
		}
		if strings.ToLower(userInput) == "exit" {
			close(quit)
			break
		}

		messages = append(messages, openai.ChatCompletionMessage{
			Role:    openai.ChatMessageRoleUser,
			Content: userInput,
		})

		// 內層迴圈：Agent 思考與執行指令
		for {
			req := openai.ChatCompletionRequest{
				Model:    "gemma3:12b",
				Messages: messages,
			}

			resp, err := client.CreateChatCompletion(context.Background(), req)
			if err != nil {
				fmt.Printf("API 請求錯誤: %v，請確認 Ollama 服務是否正在執行 (%s)\n", err, config.BaseURL)
				break
			}

			reply := resp.Choices[0].Message.Content
			messages = append(messages, openai.ChatCompletionMessage{
				Role:    openai.ChatMessageRoleAssistant,
				Content: reply,
			})

			replyTrimmed := strings.TrimSpace(reply)

			// 如果回覆不是以 ":" 開頭，代表是一般對話，印出結果並跳出內層迴圈等待使用者下一次輸入
			if !strings.HasPrefix(replyTrimmed, "命令") {
				fmt.Println("[AI] ", replyTrimmed)
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
			streamResult, err := cmdrunner.Run(ctx, cmdrunner.Options{
				Command: cmdStr,
				Timeout: 10 * time.Second,
				StdoutHandler: func(b []byte) {
					fmt.Print("[stdout] ", string(b))
				},
				StderrHandler: func(b []byte) {
					fmt.Print("[stderr] ", string(b))
				},
			})
			if err != nil {
				fmt.Println("stream error:", err)
			}

			var commandResult string
			if err != nil {
				commandResult = fmt.Sprintf("錯誤: %v\n輸出: %s", err, string(streamResult.Stderr))
			} else {
				commandResult = string(streamResult.Stdout)
				if commandResult == "" {
					commandResult = fmt.Sprintf("%s執行成功", cmdStr)
				} else {
					commandResult = fmt.Sprintf("%s執行成功\n輸出:\n%s", cmdStr, string(streamResult.Stdout))
				}
			}

			// 將執行結果加回對話中，讓 Agent 讀取並繼續處理
			messages = append(messages, openai.ChatCompletionMessage{
				Role:    openai.ChatMessageRoleUser,
				Content: commandResult,
			})
		}
	}

	// 3. 阻塞主執行緒，直到收到訊號
	<-quit
	fmt.Println("收到關閉訊號，準備關閉程式...")

	// 4. 設定寬限期 (例如 5 秒內必須關閉完畢)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 這裡可以加入其他清理工作，例如：
	// db.Close()
	// cache.Close()

	fmt.Println("程式已成功安全退出。")
}
