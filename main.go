package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
)

func main() {
	if err := loadEnvFiles(".env", "envfile"); err != nil {
		fmt.Printf("load env file warning: %v\n", err)
	}

	config, err := getConfig()
	if err != nil {
		fmt.Printf("config error: %v\n", err)
		return
	}

	agentContent, err := os.ReadFile("Agent.md")
	if err != nil {
		fmt.Printf("read Agent.md failed: %v\n", err)
		return
	}

	memory := newMemoryStore()
	if err := memory.Init(); err != nil {
		fmt.Printf("memory init warning: %v\n", err)
	}

	messages := []Message{
		{
			Role:    "system",
			Content: string(agentContent),
		},
	}
	if memoryContext := memory.Context(); memoryContext != "" {
		messages = append(messages, Message{
			Role:    "system",
			Content: memoryContext,
		})
	}

	scanner := bufio.NewScanner(os.Stdin)

	for {
		fmt.Print("\n > ")
		if !scanner.Scan() {
			break
		}
		userInput := strings.TrimSpace(scanner.Text())
		if userInput == "" {
			continue
		}
		if strings.EqualFold(userInput, "exit") {
			fmt.Println("bye")
			break
		}
		if strings.EqualFold(userInput, "/memory") {
			fmt.Printf("memory schema: %s\nmemory index: %s\nmemory log: %s\n", memory.SchemaPath, memory.IndexPath, memory.LogPath)
			continue
		}

		messages = append(messages, Message{
			Role:    "user",
			Content: userInput,
		})

		var lastReply string
		for {
			reply, err := createChatCompletion(context.Background(), config, messages)
			if err != nil {
				fmt.Printf("API error: %v\n", err)
				break
			}

			messages = append(messages, Message{
				Role:    "assistant",
				Content: reply,
			})

			replyTrimmed := strings.TrimSpace(reply)
			fmt.Println(replyTrimmed)
			lastReply = replyTrimmed

			if !strings.HasPrefix(replyTrimmed, "命令") {
				break
			}

			parts := strings.SplitN(replyTrimmed, ":", 2)
			if len(parts) < 2 {
				break
			}
			cmdStr := strings.TrimSpace(parts[1])

			fmt.Printf(">> [command]: %s\n", cmdStr)

			messages = append(messages, Message{
				Role:    "user",
				Content: runCommand(cmdStr),
			})
		}
		if lastReply != "" {
			if err := memory.Remember(userInput, lastReply); err != nil {
				fmt.Printf("memory warning: %v\n", err)
			}
		}
	}
}
