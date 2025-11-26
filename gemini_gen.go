package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/google/generative-ai-go/genai"
	"google.golang.org/api/option"
)

// v30.1 Fix: 改名以避免與 main.go 衝突
type GeminiConfig struct {
	LLM struct {
		ApiKey string `json:"ApiKey"`
	} `json:"LLM"`
}

// v30.1 Fix: 改名以避免與 main.go 衝突
func loadGeminiConfig(path string) (*GeminiConfig, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("開啟設定檔失敗: %w", err)
	}
	defer file.Close()

	decoder := json.NewDecoder(file)
	config := &GeminiConfig{}
	err = decoder.Decode(config)
	if err != nil {
		return nil, fmt.Errorf("解析設定檔失敗: %w", err)
	}
	return config, nil
}

func main() {
	// 1. 讀取設定
	config, err := loadGeminiConfig("env.json")
	if err != nil {
		log.Fatalf("載入設定檔失敗: %v", err)
	}

	if config.LLM.ApiKey == "" {
		log.Fatal("錯誤: env.json 中未設定 LLM.ApiKey")
	}

	// 2. 初始化 Gemini 客戶端
	ctx := context.Background()
	client, err := genai.NewClient(ctx, option.WithAPIKey(config.LLM.ApiKey))
	if err != nil {
		log.Fatalf("初始化失敗: %v", err)
	}
	defer client.Close()

	// 3. 設定模型 (gemini-2.5-flash)
	model := client.GenerativeModel("gemini-2.5-flash")
	model.SetTemperature(0.7)
	model.ResponseMIMEType = "application/json"

	// 4. ★★★ 強制生成唯一 ID (由 Go 決定，不讓 AI 亂猜) ★★★
	now := time.Now()
	// 格式：S2_YYYYMMDD_HH_MM_SS (例如: S2_20251127_14_30_05)
	// 這樣保證每次按下去都是當下的最新時間
	fixedID := fmt.Sprintf("S2_%s_%s", now.Format("20060102"), now.Format("15_04_05"))

	sysPrompt := fmt.Sprintf(`
    【Role】
    You are a professional Sora2 Video Prompt Generator.
    Characters: Sir Whiskers (Cat Chef) & Sunny Bun (Rabbit Assistant).
    Style: Cheerful, Kind, Positive, Disney Pixar, 8k.
    Forbidden: Violence, Sadness, Darkness, Anger.

    【Task】
    1. Create ONE (1) new story based on "November 2025" trending topics.
    2. Use "Viral Logic" for titles and content.
    3. Output strictly in the specified Single JSON Object format.
    4. All content must be in ENGLISH.

    【Constraint: ID Assignment】
    You MUST use this EXACT unique_id for this task: "%s"
    Do NOT generate your own date or time. Use the provided ID.

    【Prompt Text Format (Strict Cinematic Timeline)】
    The 'prompt' field must be a single multi-line string using this exact structure:
    Line 1: @jeremy202.whiskbunbu
    Line 2: %s [Title]
    Line 3: [Overall Style Description]
    Line 4: With Camera Timeline + Music Cues
    Line 5: 🎬 English Version
    
    Scene 1 — [Scene Title]
    00:00–00:08 — [Camera Shot]
    [Action Description...]
    Music: [Music Description]
    [Character Dialogue if any]
    Camera: [Camera Movement]

    Scene 2 — [Scene Title]
    00:08–00:18 — [Camera Shot]
    [Action Description...]
    ...
    END — [Ending Description]

    【JSON Structure Example (Single Object Only)】
    {
      "prompt": "@jeremy202.whiskbunbu\n%s Title\nA Sora2 Cinematic Style...\nWith Camera Timeline + Music Cues\n🎬 English Version\n\nScene 1 — The Beginning\n00:00–00:08 — Wide Shot\n...",
      "metadata": {
        "unique_id": "%s",
        "file_name": "%s_FileName.mp4",
        "title": "Sora AI: Viral Title! 🚀",
        "description": "Viral description...",
        "tags": ["Sora", "SoraAI", "Viral", "Cute"],
        "category_id": "24",
        "privacy": "private"
      }
    }

    Please output ONLY the Single JSON Object. Do NOT output a List/Array.
    Generate now.
    `, fixedID, fixedID, fixedID, fixedID, fixedID)

	fmt.Println("正在請求 Gemini 生成故事 (使用強制 ID: " + fixedID + ")...")

	// 5. 發送請求
	resp, err := model.GenerateContent(
		ctx,
		genai.Text(sysPrompt),
	)
	if err != nil {
		log.Fatalf("生成失敗: %v", err)
	}

	// 6. 處理回傳結果並存檔
	if len(resp.Candidates) > 0 && len(resp.Candidates[0].Content.Parts) > 0 {
		var jsonOutput string
		for _, part := range resp.Candidates[0].Content.Parts {
			if txt, ok := part.(genai.Text); ok {
				jsonOutput += string(txt)
			}
		}

		jsonOutput = strings.TrimSpace(jsonOutput)
		jsonOutput = strings.ReplaceAll(jsonOutput, "```json", "")
		jsonOutput = strings.ReplaceAll(jsonOutput, "```", "")
		jsonOutput = strings.TrimSpace(jsonOutput)

		fileName := "story.json"
		err := os.WriteFile(fileName, []byte(jsonOutput), 0644)
		if err != nil {
			log.Fatalf("無法寫入檔案 %s: %v", fileName, err)
		}
		fmt.Printf("SUCCESS")

	} else {
		fmt.Println("沒有收到回應。")
		os.Exit(1)
	}
}
