package main

import (
	"fmt"
	"os"

	"github.com/asccclass/opengrab/llms/ollama"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
)

var (
	cfg          Config
	GlobalName   string // GlobalName 儲存 LLM 開機時自定義的名字
	modelName    string
	systemPrompt string

	aiStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Bold(true)
	// toolStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("13")).Italic(true)
	notifyStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Bold(true) // 亮黃色
	promptStr   = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Render(">>> ")
	currentOpts = ollama.Options{Temperature: 0.7, TopP: 0.9}
)

var chatCmd = &cobra.Command{
	Use:   "chat",
	Short: "開啟具備 AI Agent 能力的對話",
	Run:   runChat,
}

func init() {
	cfg = *LoadConfig()
	chatCmd.Flags().StringVarP(&modelName, "model", "m", cfg.Model, "指定使用的模型")
	chatCmd.Flags().StringVarP(&systemPrompt, "system", "s", cfg.SystemPrompt, "設定 System Prompt")
	rootCmd.AddCommand(chatCmd)
}

// rootCmd 代表基礎指令，當不帶任何子指令執行時觸發
var rootCmd = &cobra.Command{
	Use:   "opengrab-PCAI",
	Short: "Personalized Contextual AI - 你的個人 AI 助手",
	Long:  `一個支援多輪對話、工具呼叫、RAG 長期記憶的強大 CLI 工具。`,
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}
