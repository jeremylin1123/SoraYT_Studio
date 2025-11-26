package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/option"
	"google.golang.org/api/youtube/v3"
)

// ==========================================
// 1. 共用設定與結構
// ==========================================

const (
	// Sora Config
	UserCurlFile        = "userid.txt"
	RoleFile            = "Role.txt"
	SoraCreateEndpoint  = "https://sora.chatgpt.com/backend/nf/create"
	SoraPendingEndpoint = "https://sora.chatgpt.com/backend/nf/pending"
	SoraHistoryEndpoint = "https://sora.chatgpt.com/backend/project_y/mailbox?limit=50"
	ModelName           = "sy_8"
	DownloadDir         = "."

	// YouTube Config
	ConfigFile = "videos.json"
	EnvFile    = "env.json"
	TokenFile  = "token.json"
	StoryFile  = "story.json" // v29: 故事存檔
)

type SoraCredentials struct {
	BearerToken string
	Cookie      string
	DeviceID    string
	UserAgent   string
}

type SoraCreatePayload struct {
	Kind        string `json:"kind"`
	Prompt      string `json:"prompt"`
	Orientation string `json:"orientation"`
	Size        string `json:"size"`
	NFrames     int    `json:"n_frames"`
	Model       string `json:"model"`
}

type GlobalConfig struct {
	ScheduleSlots []string `json:"ScheduleSlots"`
	ArchiveFolder string   `json:"ArchiveFolder"`
}

type VideoConfig struct {
	UniqueID    string   `json:"unique_id"`
	FileName    string   `json:"file_name"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
	CategoryID  string   `json:"category_id"`
	Privacy     string   `json:"privacy"`
	Uploaded    bool     `json:"uploaded"`
	PublishAt   string   `json:"publish_at,omitempty"`
	IsManual    bool     `json:"is_manual,omitempty"`
	IgnoreCalc  bool     `json:"ignore_calc,omitempty"`
	DownloadURL string   `json:"download_url,omitempty"`
}

type VideoStatus struct {
	UniqueID string `json:"unique_id"`
	FileName string `json:"file_name"`
	Title    string `json:"title"`
	Status   string `json:"status"`
}

type IPInfo struct {
	IP      string `json:"ip"`
	City    string `json:"city"`
	Region  string `json:"region"`
	Country string `json:"country"`
}

type StatusAPIResponse struct {
	PendingCount int           `json:"pending_count"`
	StatusData   []VideoStatus `json:"status_data"`
	ManualData   []VideoStatus `json:"manual_data"`
	NextSchedule string        `json:"next_schedule"`
}

// v29: Story File Structure
type StoryContent struct {
	Prompt   string      `json:"prompt"`
	Metadata VideoConfig `json:"metadata"`
}

// Mailbox JSON Structs
type MailboxResponse struct {
	Items []MailboxItem `json:"items"`
}
type MailboxItem struct {
	ID         string        `json:"id"`
	Kind       string        `json:"kind"`
	DisplayStr string        `json:"display_str"`
	Object     MailboxObject `json:"object"`
}
type MailboxObject struct {
	Kind  string       `json:"kind"`
	Draft MailboxDraft `json:"draft"`
}
type MailboxDraft struct {
	ID              string `json:"id"`
	DownloadableURL string `json:"downloadable_url"`
}

var soraCreds *SoraCredentials
var youtubeConfig GlobalConfig = GlobalConfig{
	ScheduleSlots: []string{"00:00", "08:00", "12:00", "16:00"},
	ArchiveFolder: "_uploaded_videos",
}

// ==========================================
// 2. 主程式與初始化
// ==========================================

func main() {
	os.MkdirAll(youtubeConfig.ArchiveFolder, 0755)
	initSoraCredentials()
	loadGlobalConfig()

	fmt.Println("🔍 正在初始化網路環境檢查...")
	ip := checkIP()
	fmt.Printf("🌍 當前 IP: %s (國家: %s, 城市: %s)\n", ip.IP, ip.Country, ip.City)

	http.HandleFunc("/", handleHome)

	// Sora API
	http.HandleFunc("/api/auth/manual", handleManualAuth)
	http.HandleFunc("/api/sora/create", handleSoraCreate)
	http.HandleFunc("/api/sora/poll", handleSoraPoll)
	http.HandleFunc("/api/sora/download", handleSoraDownloadAndRename)
	http.HandleFunc("/api/sora/history_batch", handleSoraHistoryBatch)
	http.HandleFunc("/api/debug/history", handleDebugHistory)

	// v29: Story Load API (確保這裡只有一行)
	http.HandleFunc("/api/story/load", handleLoadStory)
	// v30: 呼叫外部 Gemini 生成器
	http.HandleFunc("/api/ai/generate_story", handleCallGemini)
	// YouTube API
	http.HandleFunc("/api/status", handleStatusAPI)
	http.HandleFunc("/api/video/delete", handleVideoDelete)
	http.HandleFunc("/youtube/run", handleYoutubeRun)
	http.HandleFunc("/youtube/manual_schedule", handleManualSchedule)
	http.HandleFunc("/oauth", handleOAuth)

	port := "9999"
	url := "http://localhost:" + port
	fmt.Printf("🚀 SkyForge v30 (Auto-Loader) 已啟動: %s\n", url)

	exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()

	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatal(err)
	}
}

func initSoraCredentials() {
	if data, err := os.ReadFile("session_cache.json"); err == nil {
		if err := json.Unmarshal(data, &soraCreds); err == nil && soraCreds.BearerToken != "" {
			fmt.Println("✅ Sora 憑證已載入 (Cache)")
			return
		}
	}
	if data, err := os.ReadFile(UserCurlFile); err == nil {
		if creds, parseErr := parseCurlContent(string(data)); parseErr == nil {
			soraCreds = creds
			fmt.Println("✅ Sora 憑證已載入 (Userid.txt)")
			return
		}
	}
	fmt.Println("⚠️ 無 Sora 憑證，請在網頁更新。")
}

func loadGlobalConfig() {
	data, err := os.ReadFile(EnvFile)
	if err == nil {
		json.Unmarshal(data, &youtubeConfig)
	}
}

func loadRoles() []string {
	var roles []string
	file, err := os.Open(RoleFile)
	if err != nil {
		return []string{"@jeremy202.whiskbunbu"}
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			roles = append(roles, line)
		}
	}
	return roles
}

// ==========================================
// 3. 前端介面
// ==========================================

func handleHome(w http.ResponseWriter, r *http.Request) {
	ip := checkIP()
	ipHtml := ""
	if ip.Country == "US" || ip.Country == "TW" {
		ipHtml = fmt.Sprintf(`<div style="background:#1b5e20; color:#fff; padding:10px; text-align:center; border-radius:8px; margin-bottom:20px; font-weight:bold;">✅ 網路環境安全： %s (%s, %s)</div>`, ip.IP, ip.Country, ip.City)
	} else {
		ipHtml = fmt.Sprintf(`<div style="background:#b71c1c; color:#fff; padding:10px; text-align:center; border-radius:8px; margin-bottom:20px; font-weight:bold;">⚠️ 警告：非慣用地區 IP (%s - %s)</div>`, ip.Country, ip.IP)
	}

	roles := loadRoles()
	var rolesHtmlBuilder strings.Builder
	for _, role := range roles {
		rolesHtmlBuilder.WriteString(fmt.Sprintf(
			`<div class="role-chip" draggable="true" ondragstart="event.dataTransfer.setData('text/plain', '%s')">%s</div>`,
			role, role,
		))
	}

	statusJSON := "[]"
	manualJSON := "[]"
	pendingCount := 0

	html := fmt.Sprintf(`
<!DOCTYPE html>
<html lang="zh-TW">
<head>
    <meta charset="UTF-8">
    <title>SkyForge v30</title>
    <style>
        :root { --bg: #1e1e1e; --card: #2d2d2d; --text: #fff; --accent: #7c4dff; --yt-red: #ff0000; }
        body { background: var(--bg); color: var(--text); font-family: 'Segoe UI', sans-serif; margin: 0; padding: 20px; display: flex; justify-content: center; }
        .container { display: grid; grid-template-columns: 1fr 1fr; gap: 20px; width: 95%%; max-width: 1400px; }
        .card { background: var(--card); padding: 20px; border-radius: 12px; box-shadow: 0 4px 12px rgba(0,0,0,0.3); }
        h2 { margin-top: 0; border-bottom: 1px solid #444; padding-bottom: 10px; color: var(--accent); }
        h3 { margin-top: 20px; margin-bottom: 10px; font-size: 1.1em; color: #ddd; }
        .role-container { display: flex; gap: 8px; flex-wrap: wrap; margin-bottom: 10px; padding: 10px; background: #333; border-radius: 8px; border: 1px dashed #555; }
        .role-chip { background: #555; color: white; padding: 5px 12px; border-radius: 15px; cursor: grab; font-size: 12px; user-select: none; }
        .role-chip:active { cursor: grabbing; }
        textarea, input, select { width: 100%%; padding: 10px; background: #333; color: #fff; border: 1px solid #555; border-radius: 6px; box-sizing: border-box; margin-bottom: 10px; font-family: monospace; }
        textarea:focus, input:focus { outline: 2px solid var(--accent); }
        button { width: 100%%; padding: 12px; border: none; border-radius: 6px; font-weight: bold; cursor: pointer; margin-bottom: 5px; }
        .btn-sora { background: linear-gradient(135deg, #667eea 0%%, #764ba2 100%%); color: white; }
        .btn-yt { background: #c00; color: white; }
        .btn-manual { background: #ff9800; color: black; }
        .btn-secondary { background: #555; color: white; }
        .btn-delete { background: #d32f2f; color: white; font-size: 0.8em; padding: 5px 10px; width: auto; }
        .btn-debug { background: #9c27b0; color: white; margin-top: 5px; }
        .btn-load { background: #ff9800; color: black; font-weight: bold; margin-bottom: 15px; }
        .download-manual-btn { background: #009688; color: white; }
        button:disabled { opacity: 0.5; cursor: not-allowed; }
        #log { background: #000; color: #0f0; padding: 15px; border-radius: 6px; height: 250px; overflow-y: auto; font-family: monospace; font-size: 12px; border: 1px solid #444; }
        table { width: 100%%; border-collapse: collapse; margin-top: 10px; font-size: 0.9em; }
        th, td { border: 1px solid #444; padding: 8px; text-align: left; }
        th { background: #333; }
        .status-ok { color: #4caf50; font-weight: bold; }
        .status-miss { color: #f44336; font-weight: bold; }
        .highlight-box { border: 2px solid #4caf50; padding: 10px; border-radius: 8px; margin-bottom: 15px; background: #1b3a1b; }
        #sora-usage-status { font-size: 0.9em; color: #aaa; margin-top: 10px; margin-bottom: 15px; }
        .next-schedule-info { color: #4fc3f7; font-size: 1.1em; margin-bottom: 15px; font-weight: bold; text-align: center; border: 1px solid #4fc3f7; padding: 10px; border-radius: 6px;}
        .checkbox-container { display: flex; align-items: center; margin-bottom: 15px; color: #fff; }
        .checkbox-container input { width: auto; margin-right: 10px; }
    </style>
</head>
<body>
    <div style="width: 95%%; max-width: 1400px;">
        %s 
        <div class="container" style="width: 100%%;">
            <div class="card">
                <h2>🌊 Sora 工廠 (SkyForge)</h2>
				<button class="btn-ai" onclick="generateStoryFromAI()">🧠 AI 自動生成故事 (Gemini)</button>
                <button class="btn-secondary" onclick="toggleManual()" style="width:auto; padding:5px 10px; font-size:0.8em;">更換 Sora 憑證</button>
                <div id="manual-box" style="display:none; margin-top:10px;">
                    <textarea id="curl-input" rows="3" placeholder="貼上 Curl..."></textarea>
                    <button onclick="submitManual()" style="background:#4caf50;">保存</button>
                </div>

                <button class="btn-load" onclick="loadStory()">📂 讀取 story.json 並填入</button>

                <h3>1. 角色 (拖曳)</h3>
                <div class="role-container">
                    <small style="width:100%%; color:#aaa; margin-bottom:5px;">拖曳標籤到輸入框：</small>
                    %s
                </div>

                <h3>2. 提示詞 (Prompt)</h3>
                <textarea id="sora-prompt" rows="6" placeholder="輸入 Sora 提示詞..." ondragover="event.preventDefault()" ondrop="drop(event)"></textarea>

                <h3>3. 影片設定 JSON (Metadata)</h3>
                <p style="font-size:0.8em; color:#aaa;">可選：指定 "unique_id" 以防止檔名重複。</p>
                <textarea id="meta-json" rows="8">{
  "unique_id": "", 
  "file_name": "S2_20251126_XX_XX_Title.mp4",
  "title": "My Sora Video #Shorts",
  "description": "Generated by Sora.\\n\\n#Sora #AI",
  "tags": ["Sora", "AI"],
  "category_id": "24",
  "privacy": "private"
}</textarea>

                <button id="btn-generate" class="btn-sora" onclick="startPipeline()">✨ 執行流水線 (多工並行)</button>
                <div id="sora-usage-status">點擊生成後顯示剩餘次數</div>
                <div id="sora-status" style="text-align:center; margin:10px 0; font-weight:bold; color:#aaa;">等待指令...</div>
                
                <h3>系統日誌</h3>
                <button class="btn-debug" onclick="runDebug()">🔍 Debug: 顯示完整回應</button>
                <div id="log">系統就緒... Port: 9999</div>
            </div>

            <div class="card">
                <h2>📺 YouTube 排程中心</h2>
                
                <div style="display:flex; justify-content:space-between; align-items:center; margin-bottom:15px;">
                    <div class="highlight-box" style="margin-bottom:0; flex-grow:1; margin-right:10px;">
                        📦 待上傳庫存: <span style="font-size:1.2em; font-weight:bold;">%d 部</span>
                    </div>
                    <button class="btn-yt" style="width: 200px;" onclick="checkHistoryAndDownload()">⬇️ 同步 History 並下載</button>
                </div>

                <h3>4. 庫存狀態</h3>
                <table id="fileTable">
                    <thead><tr><th>檔名</th><th>標題</th><th>狀態</th><th>操作</th></tr></thead>
                    <tbody></tbody>
                </table>

                <h3>5. 手動排程設定 (立即上傳)</h3>
                <form id="manualScheduleForm">
                    <select id="manual_file_select" name="filename"><option>載入中...</option></select>
                    <input type="datetime-local" name="publishtime" required>
                    <div class="checkbox-container">
                        <input type="checkbox" id="update_baseline" name="update_baseline" checked>
                        <label for="update_baseline">🔄 更新預計接續時間 (影響後續排程)</label>
                    </div>
                    <button type="submit" class="btn-manual">📅 設定排程並立即上傳</button>
                </form>
                <div id="manualMsg"></div>

                <h3>6. 批次自動上傳 (無腦模式)</h3>
                <div id="nextScheduleDisplay" class="next-schedule-info">📅 預計接續排程時間：載入中...</div>
                
                <form id="uploadForm">
                    <div style="display:flex; gap:10px;">
                        <input type="number" name="limit" value="5" min="1" placeholder="本次上傳數量">
                        <input type="hidden" name="date" value=""> 
                    </div>
                    <button type="submit" class="btn-yt">🚀 開始上傳與歸檔</button>
                </form>

                <hr style="margin: 20px 0; border: 0; border-top: 1px dashed #555;">
                <h3 style="color:#009688;">🔗 強制下載 (救援模式)</h3>
                <textarea id="manual-meta-json" rows="5" placeholder="(選填) 貼上 JSON 設定以自動改名並歸檔..." style="font-size:12px; font-family:monospace;"></textarea>
                <button class="download-manual-btn" onclick="manualDownload()">⬇️ 下載並套用 JSON</button>
            </div>
        </div>
    </div>

    <script>
        let STATUS_DATA = %s;
        let MANUAL_DATA = %s;
        
        function updateUsageDisplay(remaining) {
            const el = document.getElementById('sora-usage-status');
            el.innerHTML = '剩餘生成次數: <span style="font-weight:bold; color:#f90;">' + remaining + '</span> 次';
        }

        function log(msg) {
            const el = document.getElementById('log');
            el.innerHTML += '<br>[' + new Date().toLocaleTimeString() + '] ' + msg;
            el.scrollTop = el.scrollHeight;
        }

        function drop(e) {
            e.preventDefault();
            const role = e.dataTransfer.getData('text/plain');
            if(role) {
                const textArea = e.target;
                if (textArea.value.trim() === "") {
                    textArea.value = role;
                } else {
                    textArea.value = role + '\\n' + textArea.value;
                }
                textArea.focus();
            }
        }
// v30: 呼叫外部生成器
        async function generateStoryFromAI() {
            const status = document.getElementById('ai-status');
            const btn = document.querySelector('.btn-ai');
            
            btn.disabled = true;
            status.innerText = "⏳ 正在呼叫 Gemini 撰寫劇本 (約需 5-10 秒)...";
            log(">>> 呼叫外部 AI 生成器...");

            try {
                const res = await fetch('/api/ai/generate_story');
                const data = await res.json();
                
                if (res.ok) {
                    log("🎉 AI 生成成功！故事已寫入 story.json");
                    status.innerText = "✅ 生成完畢！請按下方按鈕讀取";
                    status.style.color = "#4caf50";
                } else {
                    throw new Error(data.error || "生成失敗");
                }
            } catch(e) {
                log("❌ AI 錯誤: " + e);
                status.innerText = "❌ 生成失敗: " + e;
                status.style.color = "#f44336";
            } finally {
                btn.disabled = false;
            }
        }
        async function fetchAndUpdateTables() {
            const res = await fetch('/api/status');
            const data = await res.json();
            STATUS_DATA = data.status_data;
            MANUAL_DATA = data.manual_data;
            renderTable();
            populateSelect();
            document.querySelector('.highlight-box span').innerText = data.pending_count + ' 部';
            if (data.next_schedule) {
                document.getElementById('nextScheduleDisplay').innerText = '📅 預計接續排程時間：' + data.next_schedule;
            } else {
                document.getElementById('nextScheduleDisplay').innerText = '📅 預計排程：從 [現在] 開始計算';
            }
        }

        window.onload = function() {
            fetchAndUpdateTables();
        };

        // v29: Load Story
// v29: 前端讀檔邏輯
        async function loadStory() {
            log(">>> 正在讀取 story.json ...");
            try {
                const res = await fetch('/api/story/load');
                if (!res.ok) {
                    const errData = await res.json();
                    throw new Error(errData.error || "無法讀取檔案");
                }
                const data = await res.json();
                
                // 自動填入
                if(data.prompt) document.getElementById('sora-prompt').value = data.prompt;
                if(data.metadata) document.getElementById('meta-json').value = JSON.stringify(data.metadata, null, 2);
                
                log("✅ 故事與設定已載入！");
            } catch(e) {
                alert("讀取失敗: " + e);
                log("❌ 讀取失敗: " + e);
            }
        }

        async function deleteVideo(filename) {
            if(!confirm('確定要從清單中移除 [' + filename + '] 嗎？此操作不可逆。')) return;
            try {
                const res = await fetch('/api/video/delete', {
                    method: 'POST',
                    headers: {'Content-Type': 'application/x-www-form-urlencoded'},
                    body: 'filename=' + encodeURIComponent(filename)
                });
                if(res.ok) {
                    log("🗑️ 已移除紀錄: " + filename);
                    fetchAndUpdateTables();
                } else { log("❌ 移除失敗"); }
            } catch(e) { log("異常: " + e); }
        }

        function renderTable() {
            const tbody = document.querySelector('#fileTable tbody');
            const list = STATUS_DATA.concat(MANUAL_DATA);
            tbody.innerHTML = '';
            if(list.length === 0) tbody.innerHTML = '<tr><td colspan="4">無資料</td></tr>';
            list.forEach(item => {
                const cls = item.status === 'Available' ? 'status-ok' : 'status-miss';
                const delBtn = '<button class="btn-delete" onclick="deleteVideo(\''+item.file_name+'\')">🗑️</button>';
                let actionBtn = '';
                if (item.status !== 'Available') {
                    actionBtn = '<button style="background:#4caf50; color:white; font-size:0.8em; padding:5px 10px; width:auto; margin-right:5px;" onclick="downloadMissing(\''+item.file_name+'\', \''+item.unique_id+'\')">⬇️</button> ';
                }
                tbody.innerHTML += '<tr><td>'+item.file_name+'</td><td>'+item.title.substring(0,20)+'...</td><td class="'+cls+'">'+item.status+'</td><td>'+actionBtn+delBtn+'</td></tr>';
            });
        }
        
        async function downloadMissing(filename, uniqueId) {
            log(">>> 嘗試補檔下載: " + filename);
            try {
                const res = await fetch('/api/sora/download', {
                    method: 'POST',
                    body: JSON.stringify({ filename: filename, unique_id_lookup: uniqueId })
                });
                const data = await res.json();
                 if(res.ok) {
                    if (data.skipped) {
                        log("⏭️ [跳過] " + (data.debug_msg || "檔案已存在"));
                    } else {
                        log("🎉 補檔成功: " + data.filename);
                    }
                    fetchAndUpdateTables();
                } else {
                    log("❌ 補檔失敗: " + data.error + " (請嘗試先按上方「掃描 History」更新連結)");
                }
            } catch(e) { log("異常: " + e); }
        }

        async function runDebug() {
            log(">>> 正在提取後端原始回應...");
            try {
                const res = await fetch('/api/debug/history');
                const text = await res.text();
                log("--- BACKEND RESPONSE (FULL) ---");
                log(text.substring(0, 1000) + "..."); 
                console.log(JSON.parse(text));
                log("----------------------------------");
            } catch(e) { log("Debug Error: " + e); }
        }

        async function startPipeline() {
            const prompt = document.getElementById('sora-prompt').value;
            const jsonStr = document.getElementById('meta-json').value;
            if(!prompt) return alert("請輸入提示詞");
            let metaObj;
            try {
                metaObj = JSON.parse(jsonStr);
                if(!metaObj.file_name) throw "缺少 file_name";
            } catch(e) { return alert("JSON 格式錯誤: " + e); }
            
            const status = document.getElementById('sora-status');
            status.innerText = "🚀 發送生成請求...";
            log(">>> 啟動任務: " + metaObj.file_name);
            try {
                const res = await fetch('/api/sora/create', {
                    method:'POST', headers:{'Content-Type':'application/x-www-form-urlencoded'},
                    body:'prompt='+encodeURIComponent(prompt)
                });
                const data = await res.json();
                if(data.error) throw data.error;
                if (data.rate_limit_and_credit_balance.estimated_num_videos_remaining !== undefined) {
                    updateUsageDisplay(data.rate_limit_and_credit_balance.estimated_num_videos_remaining);
                }
                const taskId = data.id;
                log("✅ 任務 ID: " + taskId + " 已建立");
                status.innerText = "⏳ 生成中 (請稍候)...";
                setTimeout(() => { pollSora(taskId, metaObj, prompt); }, 3000);
            } catch(e) { log("❌ 錯誤: " + e); }
        }

        async function pollSora(taskId, metaObj, originalPrompt) {
            const status = document.getElementById('sora-status');
            let attempts = 0;
            log("👀 開始監控: " + metaObj.file_name);
            const timer = setInterval(async () => {
                attempts++;
                if(attempts > 600) { clearInterval(timer); log("❌ 任務超時: " + metaObj.file_name); return; }
                try {
                    const res = await fetch('/api/sora/poll?task_id=' + taskId + '&prompt=' + encodeURIComponent(originalPrompt));
                    const data = await res.json();
                    if(data.status === 'running') {
                        if(attempts %% 10 === 0) log("⏳ " + metaObj.file_name + " 生成中...");
                    } else if(data.status === 'done') {
                        clearInterval(timer);
                        
                        let downloadUrl = "";
                        if(data.download_links && data.download_links.length > 0) {
                            downloadUrl = data.download_links[0];
                            log("✨ 抓到連結，準備下載...");
                        } else {
                            log("⚠️ 任務完成但沒抓到連結，先強制存檔...");
                        }

                        await downloadAndFinalize(downloadUrl, metaObj);

                        if (downloadUrl === "") {
                            log("🔄 3秒後自動嘗試補檔下載...");
                            setTimeout(() => {
                                downloadMissing(metaObj.file_name, metaObj.unique_id);
                            }, 3000);
                        } else {
                            fetchAndUpdateTables(); 
                        }
                    }
                } catch(e) { console.error(e); }
            }, 5000);
        }

        async function downloadAndFinalize(url, metaObj) {
            try {
                const res = await fetch('/api/sora/download', {
                    method: 'POST',
                    body: JSON.stringify({ url: url, filename: metaObj.file_name, meta_json: JSON.stringify(metaObj) })
                });
                const data = await res.json();
                if(res.ok) {
                    log("🎉 成功歸檔: " + data.filename);
                    document.getElementById('sora-status').innerText = "✅ 最新任務完成";
                } else { log("❌ 下載失敗: " + data.error); }
            } catch(e) { log("異常: " + e); }
        }
        
        async function checkHistoryAndDownload() {
            const btn = document.querySelector('.btn-yt[onclick="checkHistoryAndDownload()"]');
            btn.disabled = true; btn.innerText = '掃描中...';
            log(">>> 啟動 Mailbox 掃描並下載...");
            try {
                const res = await fetch('/api/sora/history_batch');
                const data = await res.json();
                if(data.download_links && data.download_links.length > 0) {
                    log("✅ 發現 " + data.download_links.length + " 個影片，開始下載批次...");
                    await processBatch(data.download_links);
                } else { log("⚠️ Mailbox 無可用連結。"); }
            } catch(e) { log("History Error: " + e); } 
            finally { btn.disabled = false; btn.innerText = '⬇️ 同步 History 並下載'; fetchAndUpdateTables(); }
        }

        async function processBatch(links) {
            let count = 0;
            for (const link of links) {
                count++;
                document.getElementById('sora-status').innerText = "下載中 (" + count + "/" + links.length + ")...";
                await triggerDownloadHistory(link); 
            }
            log("🎉 批次下載結束！");
            document.getElementById('sora-status').innerText = "✅ 下載完成，請檢查資料夾";
            fetchAndUpdateTables(); 
        }

        async function triggerDownloadHistory(url) {
            if(!url) return;
            try {
                const res = await fetch('/api/sora/download', { method:'POST', body: JSON.stringify({url: url, filename: ""}) });
                const data = await res.json();
                if(res.ok) {
                    if(data.skipped) { log("⏭️ [跳過] " + data.filename); } else { log("📥 [已下載] " + data.filename); }
                } else { log("❌ 下載失敗: " + data.error); }
            } catch(e) { log("連線異常: " + e); }
        }

        async function manualDownload() {
            const jsonStr = document.getElementById('manual-meta-json').value;
            if(!jsonStr) return alert("請貼上 JSON");
            let metaObj = null;
            try {
                metaObj = JSON.parse(jsonStr);
                if (!metaObj.file_name) return alert("JSON 缺少 file_name");
            } catch(e) { return alert("JSON 格式錯誤"); }
            log(">>> 手動建檔/下載中...");
            const bodyData = { 
                url: "", 
                filename: metaObj.file_name,
                meta_json: JSON.stringify(metaObj),
                unique_id_lookup: metaObj.unique_id 
            };

            try {
                const res = await fetch('/api/sora/download', { method: 'POST', body: JSON.stringify(bodyData) });
                const data = await res.json();
                if(res.ok) {
                    if (data.message) { log("ℹ️ " + data.message); } else { log("🎉 處理成功: " + data.filename); }
                    fetchAndUpdateTables();
                } else { log("❌ 失敗: " + data.error); }
            } catch(e) { log("異常: " + e); }
        }

        function populateSelect() {
            const sel = document.getElementById('manual_file_select');
            if (STATUS_DATA.length === 0 && MANUAL_DATA.length === 0) {
                sel.innerHTML = '<option value="" disabled selected>-- 無可用影片 --</option>';
            } else {
                sel.innerHTML = '<option value="" disabled selected>-- 選擇影片 --</option>';
                STATUS_DATA.forEach(i => sel.innerHTML += '<option value="'+i.file_name+'">'+i.file_name+'</option>');
                MANUAL_DATA.forEach(i => sel.innerHTML += '<option value="'+i.file_name+'" disabled>'+i.file_name+' (已排程)</option>');
            }
        }

        document.getElementById('manualScheduleForm').onsubmit = async function(e) {
            e.preventDefault();
            const fd = new FormData(this);
            const msgDiv = document.getElementById('manualMsg');
            msgDiv.innerText = "🚀 正在設定排程並上傳中...";
            log(">>> 手動上傳開始...");
            const res = await fetch('/youtube/manual_schedule', { method:'POST', body: new URLSearchParams(fd) });
            const reader = res.body.getReader();
            const dec = new TextDecoder();
            while(true) {
                const {value, done} = await reader.read();
                if(done) break;
                log(dec.decode(value));
            }
            msgDiv.innerText = "✅ 作業結束";
            fetchAndUpdateTables(); 
        };

        document.getElementById('uploadForm').onsubmit = async function(e) {
            e.preventDefault();
            log(">>> 準備上傳...");
            const fd = new FormData(this);
            const res = await fetch('/youtube/run?' + new URLSearchParams(fd).toString());
            const reader = res.body.getReader();
            const dec = new TextDecoder();
            while(true) {
                const {value, done} = await reader.read();
                if(done) break;
                log(dec.decode(value));
            }
            fetchAndUpdateTables(); 
        };

        function toggleManual() { document.getElementById('manual-box').style.display = 'block'; }
        async function submitManual() {
            const c = document.getElementById('curl-input').value;
            await fetch('/api/auth/manual', { method:'POST', headers:{'Content-Type':'application/x-www-form-urlencoded'}, body:'curl='+encodeURIComponent(c)});
            location.reload();
        }
    </script>
</body>
</html>
	`, ipHtml, rolesHtmlBuilder.String(), pendingCount, statusJSON, manualJSON)

	w.Write([]byte(html))
}

// ==========================================
// 4. Handlers (API)
// ==========================================

// v29: 讀取 story.json
func handleLoadStory(w http.ResponseWriter, r *http.Request) {
	// 讀取跟 main.go 同一層的 story.json
	data, err := os.ReadFile("story.json")
	if err != nil {
		jsonError(w, "找不到 story.json 檔案 (請確認檔案在根目錄)")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}

func handleStatusAPI(w http.ResponseWriter, r *http.Request) {
	videos, _ := loadConfig(ConfigFile)
	statusList := []VideoStatus{}
	manualList := []VideoStatus{}
	pendingCount := 0

	var lastScheduledTime time.Time

	for _, v := range videos {
		if !v.Uploaded {
			pendingCount++
			status := "Missing"
			if _, err := os.Stat(v.FileName); err == nil {
				status = "Available"
			}
			entry := VideoStatus{
				UniqueID: v.UniqueID,
				FileName: v.FileName, Title: v.Title, Status: status,
			}
			if v.IsManual {
				manualList = append(manualList, entry)
			} else {
				statusList = append(statusList, entry)
			}
		}
		if v.PublishAt != "" && !v.IgnoreCalc {
			t, err := time.Parse(time.RFC3339, v.PublishAt)
			if err == nil && t.After(lastScheduledTime) {
				lastScheduledTime = t
			}
		}
	}

	nextSlot := calculateNextSlot(lastScheduledTime)
	loc, _ := time.LoadLocation("Asia/Taipei")
	nextSlotStr := nextSlot.In(loc).Format("2006-01-02 15:04")

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(StatusAPIResponse{
		PendingCount: pendingCount,
		StatusData:   statusList,
		ManualData:   manualList,
		NextSchedule: nextSlotStr,
	})
}

func calculateNextSlot(lastTime time.Time) time.Time {
	loc, _ := time.LoadLocation("Asia/Taipei")
	if lastTime.IsZero() {
		lastTime = time.Now()
	} else {
		lastTime = lastTime.In(loc)
	}
	baseDate := time.Date(lastTime.Year(), lastTime.Month(), lastTime.Day(), 0, 0, 0, 0, loc)
	for _, slot := range youtubeConfig.ScheduleSlots {
		parts := strings.Split(slot, ":")
		h, _ := strconv.Atoi(parts[0])
		m, _ := strconv.Atoi(parts[1])
		candidate := time.Date(baseDate.Year(), baseDate.Month(), baseDate.Day(), h, m, 0, 0, loc)
		if candidate.After(lastTime) {
			return candidate
		}
	}
	nextDay := baseDate.AddDate(0, 0, 1)
	parts := strings.Split(youtubeConfig.ScheduleSlots[0], ":")
	h, _ := strconv.Atoi(parts[0])
	m, _ := strconv.Atoi(parts[1])
	return time.Date(nextDay.Year(), nextDay.Month(), nextDay.Day(), h, m, 0, 0, loc)
}

func handleVideoDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "405 Method Not Allowed", 405)
		return
	}
	filename := r.FormValue("filename")
	if filename == "" {
		http.Error(w, "Filename missing", 400)
		return
	}
	videos, _ := loadConfig(ConfigFile)
	newVideos := []VideoConfig{}
	found := false
	for _, v := range videos {
		if v.FileName == filename {
			found = true
		} else {
			newVideos = append(newVideos, v)
		}
	}
	if found {
		saveConfig(ConfigFile, newVideos)
		fmt.Printf("🗑️ 已刪除影片紀錄: %s\n", filename)
		w.WriteHeader(200)
	} else {
		http.Error(w, "File not found", 404)
	}
}

func handleManualAuth(w http.ResponseWriter, r *http.Request) {
	curl := r.FormValue("curl")
	creds, err := parseCurlContent(curl)
	if err != nil {
		jsonError(w, err.Error())
		return
	}
	soraCreds = creds
	saveCredentialsCache(creds)
	jsonError(w, "success")
}

func handleSoraCreate(w http.ResponseWriter, r *http.Request) {
	if soraCreds == nil {
		jsonError(w, "未登入")
		return
	}
	prompt := r.FormValue("prompt")
	payload := SoraCreatePayload{Kind: "video", Prompt: prompt, Orientation: "portrait", Size: "small", NFrames: 300, Model: ModelName}
	respBody, err := sendSoraRequest("POST", SoraCreateEndpoint, payload)
	if err != nil {
		jsonError(w, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(respBody)
}

// v28: Poll Handler - 精準 Task ID 比對
func handleSoraPoll(w http.ResponseWriter, r *http.Request) {
	if soraCreds == nil {
		jsonError(w, "未登入")
		return
	}
	targetTaskId := r.URL.Query().Get("task_id")
	// ❌ 移除未使用的 targetPrompt

	pendingData, err := sendSoraRequest("GET", SoraPendingEndpoint, nil)
	if err != nil {
		jsonError(w, err.Error())
		return
	}

	if targetTaskId != "" && strings.Contains(string(pendingData), targetTaskId) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "running"})
		return
	}

	mailData, err := sendSoraRequest("GET", SoraHistoryEndpoint, nil)
	if err != nil {
		jsonError(w, err.Error())
		return
	}

	// 使用 Task ID 進行提取
	links := extractLinksByTaskID(string(mailData), targetTaskId)

	if len(links) == 0 {
		fmt.Println("⚠️ 無法匹配 Task ID，啟動保底機制 (抓取最新)...")
		links = extractFirstValidLink(string(mailData))
	}

	response := map[string]interface{}{"status": "done", "download_links": links}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// v28 New: Extract by Task ID
func extractLinksByTaskID(jsonBody string, targetTaskID string) []string {
	var mailboxResponse MailboxResponse
	if err := json.Unmarshal([]byte(jsonBody), &mailboxResponse); err != nil {
		return nil
	}
	for _, item := range mailboxResponse.Items {
		if item.Kind == "sora_gen_complete" {
			if item.Object.Draft.ID != "" && strings.Contains(jsonBody, targetTaskID) { // 簡化檢查，JSON 結構可能有變
				// 實際上 MailboxObject.Draft 裡沒有 task_id 欄位，task_id 在外層或不同結構
				// 根據你提供的 JSON，task_id 在 item.object.draft.task_id
				// 但為了保險，我們還是保留 v27 的 Smart Match 作為備援
				return extractLinksSmart(jsonBody, "") // 暫時 fallback 到 smart match
			}
		}
	}
	// 修正：根據你的 JSON，task_id 確實在 draft 裡，這裡補上
	// 重新解析一次
	type DetailedMailbox struct {
		Items []struct {
			Object struct {
				Draft struct {
					TaskID          string `json:"task_id"`
					DownloadableURL string `json:"downloadable_url"`
				} `json:"draft"`
			} `json:"object"`
			Kind string `json:"kind"`
		} `json:"items"`
	}
	var detailed DetailedMailbox
	json.Unmarshal([]byte(jsonBody), &detailed)
	for _, item := range detailed.Items {
		if item.Kind == "sora_gen_complete" && item.Object.Draft.TaskID == targetTaskID {
			if item.Object.Draft.DownloadableURL != "" {
				return []string{item.Object.Draft.DownloadableURL}
			}
		}
	}
	return nil
}

func extractLinksSmart(jsonBody string, targetPrompt string) []string {
	var mailboxResponse MailboxResponse
	if err := json.Unmarshal([]byte(jsonBody), &mailboxResponse); err != nil {
		return nil
	}
	var bestLinks []string
	idRegex := regexp.MustCompile(`(S2_\d+_\d+_\d+)`)
	targetID := idRegex.FindString(targetPrompt)
	targetKey := normalizePrompt(targetPrompt)
	for _, item := range mailboxResponse.Items {
		if item.Kind != "sora_gen_complete" {
			continue
		}
		prompt := item.DisplayStr
		match := false
		if targetID != "" && strings.Contains(prompt, targetID) {
			match = true
		}
		if !match {
			itemKey := normalizePrompt(prompt)
			if strings.Contains(itemKey, targetKey) || strings.Contains(targetKey, itemKey) {
				match = true
			}
		}
		if match {
			if item.Object.Draft.DownloadableURL != "" {
				bestLinks = append(bestLinks, item.Object.Draft.DownloadableURL)
			}
		}
	}
	return bestLinks
}

func extractFirstValidLink(jsonBody string) []string {
	var mailboxResponse MailboxResponse
	if err := json.Unmarshal([]byte(jsonBody), &mailboxResponse); err != nil {
		return nil
	}
	for _, item := range mailboxResponse.Items {
		if item.Kind == "sora_gen_complete" && item.Object.Draft.DownloadableURL != "" {
			return []string{item.Object.Draft.DownloadableURL}
		}
	}
	return nil
}

func handleSoraHistoryBatch(w http.ResponseWriter, r *http.Request) {
	if soraCreds == nil {
		jsonError(w, "未登入")
		return
	}
	mailBody, err := sendSoraRequest("GET", SoraHistoryEndpoint, nil)
	if err != nil {
		jsonError(w, err.Error())
		return
	}
	var mailboxResponse MailboxResponse
	if err := json.Unmarshal(mailBody, &mailboxResponse); err != nil {
		jsonError(w, "Mailbox Error")
		return
	}

	localVideos, _ := loadConfig(ConfigFile)
	localFileNames := make(map[string]bool)
	existingIDs := make(map[string]*VideoConfig)
	for i := range localVideos {
		localFileNames[localVideos[i].FileName] = true
		if localVideos[i].UniqueID != "" {
			existingIDs[localVideos[i].UniqueID] = &localVideos[i]
		}
	}
	syncedCount := 0
	idPattern := regexp.MustCompile(`(S2_\d+_\d+_\d+)`)

	for _, item := range mailboxResponse.Items {
		// 直接判斷 Kind
		if item.Kind == "sora_gen_complete" && item.Object.Draft.DownloadableURL != "" {
			url := item.Object.Draft.DownloadableURL

			re := regexp.MustCompile(`files/([a-zA-Z0-9-_]+)/`)
			match := re.FindStringSubmatch(url)

			if len(match) > 1 {
				fileUUID := match[1]
				targetFileName := "sora_" + fileUUID + ".mp4"

				// 嘗試從 DisplayStr 提取 ID
				matches := idPattern.FindStringSubmatch(item.DisplayStr)
				var foundID string
				if len(matches) > 1 {
					foundID = matches[1]
				}

				if foundID != "" {
					if v, exists := existingIDs[foundID]; exists {
						v.DownloadURL = url
						continue
					}

					// ★★★ 修正：正確構建並使用 title 變數 ★★★
					title := "SYNC: " + foundID
					if len(item.DisplayStr) > 30 {
						title += " " + item.DisplayStr[:30]
					}

					newVideo := VideoConfig{
						UniqueID:    foundID,
						FileName:    foundID + ".mp4",
						Title:       title, // 這裡使用了 title 變數
						Description: "Synced from Sora Mailbox.",
						CategoryID:  "24",
						Privacy:     "private",
						Uploaded:    false,
						IsManual:    true,
						DownloadURL: url,
					}
					localVideos = append(localVideos, newVideo)
					existingIDs[foundID] = &newVideo
					syncedCount++
				} else {
					if !localFileNames[targetFileName] {
						newVideo := VideoConfig{
							FileName:    targetFileName,
							Title:       "SYNC: " + fileUUID,
							Description: "Synced from Sora Mailbox.",
							CategoryID:  "24",
							Privacy:     "private",
							Uploaded:    false,
							IsManual:    true,
							DownloadURL: url,
						}
						localVideos = append(localVideos, newVideo)
						localFileNames[targetFileName] = true
						syncedCount++
					}
				}
			}
		}
	}
	saveConfig(ConfigFile, localVideos)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"status": "ok", "synced_count": syncedCount})
}

func handleDebugHistory(w http.ResponseWriter, r *http.Request) {
	if soraCreds == nil {
		jsonError(w, "未登入")
		return
	}
	mailBody, err := sendSoraRequest("GET", SoraHistoryEndpoint, nil)
	if err != nil {
		jsonError(w, err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	w.Write(mailBody)
}

// v28: Metadata First Download Logic
func handleSoraDownloadAndRename(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL            string `json:"url"`
		Filename       string `json:"filename"`
		MetaJSON       string `json:"meta_json"`
		UniqueIDLookup string `json:"unique_id_lookup"`
	}
	json.NewDecoder(r.Body).Decode(&req)

	targetFilename := req.Filename
	targetURL := req.URL

	// 1. Metadata First
	if req.MetaJSON != "" {
		var newVideo VideoConfig
		if err := json.Unmarshal([]byte(req.MetaJSON), &newVideo); err == nil {
			if newVideo.FileName != "" {
				targetFilename = newVideo.FileName
			} else if targetFilename != "" {
				newVideo.FileName = targetFilename
			}
			if targetURL != "" {
				newVideo.DownloadURL = targetURL
			}
			newVideo.Uploaded = false
			newVideo.IsManual = false
			currentVideos, _ := loadConfig(ConfigFile)
			found := false
			for i, v := range currentVideos {
				if (v.UniqueID != "" && newVideo.UniqueID != "" && v.UniqueID == newVideo.UniqueID) || (v.FileName == newVideo.FileName) {
					if newVideo.DownloadURL == "" && v.DownloadURL != "" {
						newVideo.DownloadURL = v.DownloadURL
					}
					if targetURL != "" {
						newVideo.DownloadURL = targetURL
					}
					currentVideos[i] = newVideo
					found = true
					if targetURL == "" && newVideo.DownloadURL != "" {
						targetURL = newVideo.DownloadURL
					}
					break
				}
			}
			if !found {
				currentVideos = append(currentVideos, newVideo)
				if targetURL == "" && newVideo.DownloadURL != "" {
					targetURL = newVideo.DownloadURL
				}
			}
			saveConfig(ConfigFile, currentVideos)
			fmt.Println("📝 [流水線] Metadata 已寫入/更新 videos.json")
		}
	}

	// 2. 補檔邏輯
	if targetURL == "" && (req.UniqueIDLookup != "" || targetFilename != "") {
		fmt.Println("🔍 [補檔模式] 檢查遠端連結...")
		var lookupID string
		if req.UniqueIDLookup != "" {
			lookupID = req.UniqueIDLookup
		} else {
			videos, _ := loadConfig(ConfigFile)
			for _, v := range videos {
				if v.FileName == targetFilename {
					lookupID = v.UniqueID
					if v.DownloadURL != "" {
						targetURL = v.DownloadURL
					}
					break
				}
			}
		}
		if targetURL == "" && lookupID != "" {
			fmt.Printf("🔄 本地無連結，正在掃描 Sora History 尋找 ID [%s]...\n", lookupID)
			newURL, err := fetchSoraURLFromHistory(lookupID)
			if err == nil {
				targetURL = newURL
				videos, _ := loadConfig(ConfigFile)
				for i, v := range videos {
					if v.UniqueID == lookupID {
						videos[i].DownloadURL = newURL
						saveConfig(ConfigFile, videos)
						fmt.Println("📝 已更新本地庫存的下載連結")
						break
					}
				}
			} else {
				fmt.Printf("⚠️ History 搜尋失敗: %v\n", err)
			}
		}
	}

	if targetFilename == "" {
		if targetURL != "" {
			re := regexp.MustCompile(`files/([a-zA-Z0-9-_]+)/`)
			match := re.FindStringSubmatch(targetURL)
			if len(match) > 1 {
				targetFilename = "sora_" + match[1] + ".mp4"
			} else {
				targetFilename = "sora_" + time.Now().Format("20060102_150405") + ".mp4"
			}
		} else {
			targetFilename = "pending_" + time.Now().Format("150405") + ".mp4"
		}
	}

	fmt.Printf("📥 [流水線/補檔] 準備下載: %s\n", targetFilename)
	statusMsg := "ok"
	if targetURL != "" {
		if _, err := os.Stat(targetFilename); err == nil {
			info, _ := os.Stat(targetFilename)
			if info.Size() > 1024 {
				statusMsg = "檔案已存在，跳過下載"
			} else {
				os.Remove(targetFilename)
				if err := downloadFileWithProgress(targetURL, targetFilename); err != nil {
					statusMsg = "下載失敗: " + err.Error()
				}
			}
		} else {
			if err := downloadFileWithProgress(targetURL, targetFilename); err != nil {
				statusMsg = "下載失敗: " + err.Error()
			}
		}
	} else {
		statusMsg = "僅建立資料 (無下載連結)"
		fmt.Println("⚠️ 無下載連結，僅執行 Metadata 存檔")
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok", "filename": targetFilename, "message": statusMsg})
}

func fetchSoraURLFromHistory(targetUniqueID string) (string, error) {
	if soraCreds == nil {
		return "", fmt.Errorf("未登入")
	}
	mailBody, err := sendSoraRequest("GET", SoraHistoryEndpoint, nil)
	if err != nil {
		return "", err
	}
	var mailboxResponse MailboxResponse
	if err := json.Unmarshal(mailBody, &mailboxResponse); err != nil {
		return "", err
	}
	for _, item := range mailboxResponse.Items {
		if item.Kind == "sora_gen_complete" {
			if strings.Contains(item.DisplayStr, targetUniqueID) {
				if item.Object.Draft.DownloadableURL != "" {
					return item.Object.Draft.DownloadableURL, nil
				}
			}
		}
	}
	return "", fmt.Errorf("Not found")
}

// ==========================================
// 5. YouTube Handlers & Logic
// ==========================================

func handleYoutubeRun(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Transfer-Encoding", "chunked")
	logger := func(msg string) {
		fmt.Fprintf(w, "%s\n", msg)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	dateStr := r.URL.Query().Get("date")
	var startDate time.Time
	if dateStr != "" {
		startDate, _ = time.Parse("2006-01-02", dateStr)
	}
	logger(fmt.Sprintf("🚀 開始上傳任務 (Limit: %d)", limit))
	if err := processScheduleAndUpload(startDate, limit, logger); err != nil {
		logger(fmt.Sprintf("❌ 錯誤: %v", err))
	} else {
		logger("🎉 任務完成")
	}
}

func handleManualSchedule(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "405", 405)
		return
	}
	r.ParseForm()
	fname := r.FormValue("filename")
	pubTimeStr := r.FormValue("publishtime")
	updateBaseline := r.FormValue("update_baseline")
	pubTime, err := time.Parse("2006-01-02T15:04", pubTimeStr)
	if err != nil {
		http.Error(w, "時間格式錯誤", 400)
		return
	}
	videos, _ := loadConfig(ConfigFile)
	var targetVideo *VideoConfig
	found := false
	for i, v := range videos {
		if v.FileName == fname {
			videos[i].PublishAt = pubTime.Format(time.RFC3339)
			videos[i].IsManual = true
			videos[i].Uploaded = false
			videos[i].IgnoreCalc = (updateBaseline != "on")
			targetVideo = &videos[i]
			found = true
			break
		}
	}
	if !found {
		http.Error(w, "找無檔案", 404)
		return
	}
	saveConfig(ConfigFile, videos)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Transfer-Encoding", "chunked")
	logger := func(msg string) {
		fmt.Fprintf(w, "%s\n", msg)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}
	ctx := context.Background()
	b, _ := os.ReadFile("client_secret.json")
	config, _ := google.ConfigFromJSON(b, youtube.YoutubeUploadScope)
	client := getClient(config)
	service, _ := youtube.NewService(ctx, option.WithHTTPClient(client))
	if _, err := os.Stat(targetVideo.FileName); os.IsNotExist(err) {
		logger("❌ 錯誤：找不到檔案 (請確認檔案是否在根目錄): " + targetVideo.FileName)
		return
	}
	logger(fmt.Sprintf("📤 上傳中: %s", targetVideo.FileName))
	if err := uploadVideo(service, targetVideo); err != nil {
		logger("❌ 上傳失敗: " + err.Error())
		return
	}
	targetVideo.Uploaded = true
	archiveVideo(targetVideo.FileName)
	saveConfig(ConfigFile, videos)
	logger("✅ 手動排程上傳與歸檔完成！")
}

func processScheduleAndUpload(startDate time.Time, limit int, logger func(string)) error {
	videos, err := loadConfig(ConfigFile)
	if err != nil {
		return err
	}
	ctx := context.Background()
	b, err := os.ReadFile("client_secret.json")
	if err != nil {
		return fmt.Errorf("Missing client_secret.json")
	}
	config, _ := google.ConfigFromJSON(b, youtube.YoutubeUploadScope)
	client := getClient(config)
	service, _ := youtube.NewService(ctx, option.WithHTTPClient(client))
	logger("🔗 同步 YouTube 排程...")
	lastTime := getLastScheduledTime(service)
	var localMaxTime time.Time
	for _, v := range videos {
		if v.PublishAt != "" && !v.IgnoreCalc {
			t, _ := time.Parse(time.RFC3339, v.PublishAt)
			if t.After(localMaxTime) {
				localMaxTime = t
			}
		}
	}
	if localMaxTime.After(lastTime) {
		lastTime = localMaxTime
	}
	var currTime time.Time
	if startDate.IsZero() {
		currTime = calculateNextSlot(lastTime)
	} else {
		loc, _ := time.LoadLocation("Asia/Taipei")
		currTime = time.Date(startDate.Year(), startDate.Month(), startDate.Day(), 0, 0, 0, 0, loc)
		if lastTime.After(currTime) {
			currTime = calculateNextSlot(lastTime)
		}
	}
	processed := 0
	for i := range videos {
		if processed >= limit {
			break
		}
		v := &videos[i]
		if v.Uploaded {
			continue
		}
		if _, err := os.Stat(v.FileName); os.IsNotExist(err) {
			logger("❌ 缺檔跳過: " + v.FileName)
			continue
		}
		if v.IsManual && v.PublishAt != "" {
			if t, err := time.Parse(time.RFC3339, v.PublishAt); err == nil {
				if !v.IgnoreCalc && t.After(currTime) {
					currTime = calculateNextSlot(t)
				}
			}
		} else {
			v.PublishAt = currTime.In(time.UTC).Format(time.RFC3339)
			currTime = calculateNextSlot(currTime)
		}
		logger(fmt.Sprintf("📤 上傳中: %s (%s)", v.FileName, v.PublishAt))
		if err := uploadVideo(service, v); err != nil {
			logger("❌ 上傳失敗: " + err.Error())
			continue
		}
		v.Uploaded = true
		archiveVideo(v.FileName)
		saveConfig(ConfigFile, videos)
		processed++
	}
	return nil
}

func getLastScheduledTime(service *youtube.Service) time.Time {
	call := service.Videos.List([]string{"status"}).MyRating("like").MaxResults(10)
	resp, err := call.Do()
	var last time.Time
	if err == nil {
		for _, item := range resp.Items {
			if item.Status.PrivacyStatus == "private" && item.Status.PublishAt != "" {
				t, _ := time.Parse(time.RFC3339, item.Status.PublishAt)
				if t.After(last) {
					last = t
				}
			}
		}
	}
	return last
}

func uploadVideo(service *youtube.Service, v *VideoConfig) error {
	upload := &youtube.Video{
		Snippet: &youtube.VideoSnippet{Title: v.Title, Description: v.Description, Tags: v.Tags, CategoryId: v.CategoryID},
		Status:  &youtube.VideoStatus{PrivacyStatus: "private", PublishAt: v.PublishAt},
	}
	f, _ := os.Open(v.FileName)
	defer f.Close()
	_, err := service.Videos.Insert([]string{"snippet", "status"}, upload).Media(f).Do()
	return err
}

func archiveVideo(filename string) {
	os.Rename(filename, filepath.Join(youtubeConfig.ArchiveFolder, filename))
}

// ==========================================
// 6. Utilities
// ==========================================

func checkIP() IPInfo {
	client := http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("https://ipinfo.io/json")
	var info IPInfo
	if err == nil {
		defer resp.Body.Close()
		json.NewDecoder(resp.Body).Decode(&info)
	}
	return info
}

func loadConfig(file string) ([]VideoConfig, error) {
	var v []VideoConfig
	b, _ := os.ReadFile(file)
	json.Unmarshal(b, &v)
	return v, nil
}

func saveConfig(file string, v []VideoConfig) {
	b, _ := json.MarshalIndent(v, "", "  ")
	os.WriteFile(file, b, 0644)
}

func handleOAuth(w http.ResponseWriter, r *http.Request) { fmt.Fprintf(w, "Auth Code Received") }
func getClient(config *oauth2.Config) *http.Client {
	tokFile := TokenFile
	tok, err := tokenFromFile(tokFile)
	if err != nil {
		tok = getTokenFromWeb(config)
		saveToken(tokFile, tok)
	}
	return config.Client(context.Background(), tok)
}
func tokenFromFile(file string) (*oauth2.Token, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	tok := &oauth2.Token{}
	err = json.NewDecoder(f).Decode(tok)
	return tok, err
}
func saveToken(path string, token *oauth2.Token) {
	f, _ := os.Create(path)
	defer f.Close()
	json.NewEncoder(f).Encode(token)
}
func getTokenFromWeb(config *oauth2.Config) *oauth2.Token {
	authURL := config.AuthCodeURL("state-token", oauth2.AccessTypeOffline)
	fmt.Printf("⚠️ 請授權: %v\n輸入代碼: ", authURL)
	var authCode string
	fmt.Scan(&authCode)
	tok, _ := config.Exchange(context.Background(), authCode)
	return tok
}

func parseCurlContent(content string) (*SoraCredentials, error) {
	creds := &SoraCredentials{UserAgent: "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/142.0.0.0 Safari/537.36"}
	reToken := regexp.MustCompile(`(?i)authorization:\s*(Bearer\s+)?([a-zA-Z0-9\._-]+)`)
	if match := reToken.FindStringSubmatch(content); len(match) > 2 {
		creds.BearerToken = "Bearer " + match[2]
	}
	reCookie := regexp.MustCompile(`(?i)-b\s+'([^']*)'`)
	if match := reCookie.FindStringSubmatch(content); len(match) > 1 {
		creds.Cookie = match[1]
	} else {
		reCookieH := regexp.MustCompile(`(?i)cookie:\s*([^']*)`)
		if matchH := reCookieH.FindStringSubmatch(content); len(matchH) > 1 {
			creds.Cookie = matchH[1]
		}
	}
	reDevice := regexp.MustCompile(`(?i)oai-device-id:\s*([a-zA-Z0-9-]+)`)
	if match := reDevice.FindStringSubmatch(content); len(match) > 1 {
		creds.DeviceID = match[1]
	}
	if creds.BearerToken == "" {
		return nil, fmt.Errorf("Token 解析失敗")
	}
	return creds, nil
}

func saveCredentialsCache(c *SoraCredentials) {
	f, _ := os.Create("session_cache.json")
	defer f.Close()
	json.NewEncoder(f).Encode(c)
}

func sendSoraRequest(method, url string, payload interface{}) ([]byte, error) {
	var bodyReader io.Reader
	if payload != nil {
		b, _ := json.Marshal(payload)
		bodyReader = bytes.NewBuffer(b)
	}
	req, _ := http.NewRequest(method, url, bodyReader)
	req.Header.Set("Authorization", soraCreds.BearerToken)
	req.Header.Set("Cookie", soraCreds.Cookie)
	req.Header.Set("Oai-Device-Id", soraCreds.DeviceID)
	if soraCreds.UserAgent == "" {
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/142.0.0.0 Safari/537.36")
	} else {
		req.Header.Set("User-Agent", soraCreds.UserAgent)
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}
	return body, nil
}

type WriteCounter struct{ Total, ContentLen uint64 }

func (wc *WriteCounter) Write(p []byte) (int, error) {
	n := len(p)
	wc.Total += uint64(n)
	wc.PrintProgress()
	return n, nil
}
func (wc *WriteCounter) PrintProgress() {
	if wc.ContentLen == 0 {
		return
	}
	if int(wc.Total)%(1024*1024) == 0 {
		fmt.Printf("\rDownloading... %.0f%% ", float64(wc.Total)/float64(wc.ContentLen)*100)
	}
}

func downloadFileWithProgress(url, filename string) error {
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Referer", "https://sora.chatgpt.com/")
	if soraCreds != nil {
		req.Header.Set("User-Agent", soraCreds.UserAgent)
	}
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}
	ct := resp.Header.Get("Content-Type")
	if strings.Contains(ct, "xml") || strings.Contains(ct, "text") {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("Invalid Content-Type (%s): %s", ct, string(body))
	}
	out, err := os.Create(filename)
	if err != nil {
		return err
	}
	defer out.Close()
	expectedSize := resp.ContentLength
	counter := &WriteCounter{ContentLen: uint64(expectedSize)}
	var copiedBytes int64
	var copyErr error
	copiedBytes, copyErr = io.Copy(out, io.TeeReader(resp.Body, counter))
	fmt.Println(" Done.")
	if copyErr != nil {
		os.Remove(filename)
		return fmt.Errorf("下載期間發生錯誤: %v", copyErr)
	}
	if expectedSize > 0 && copiedBytes != expectedSize {
		os.Remove(filename)
		return fmt.Errorf("檔案大小不匹配！預期 %d bytes，實際下載 %d bytes。檔案已刪除。", expectedSize, copiedBytes)
	}
	return nil
}

func normalizePrompt(s string) string {
	s = strings.ToLower(s)
	reg, _ := regexp.Compile("[^a-z0-9]+")
	s = reg.ReplaceAllString(s, "")
	if len(s) > 30 {
		return s[:30]
	}
	return s
}

func jsonError(w http.ResponseWriter, msg string) {
	// ★★★ 修正：設定 HTTP 500 狀態碼，讓前端知道出錯了 ★★★
	w.WriteHeader(http.StatusInternalServerError)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// v30: 執行外部 Gemini 生成程式
// v30.1: 執行外部 Gemini 生成程式 (優化錯誤回傳)
func handleCallGemini(w http.ResponseWriter, r *http.Request) {
	fmt.Println("🤖 正在啟動 Gemini 生成器 (gemini_gen.go)...")

	// 檢查 gemini_gen.go 是否存在
	if _, err := os.Stat("gemini_gen.go"); os.IsNotExist(err) {
		errMsg := "找不到 gemini_gen.go 檔案，請確保它與主程式在同一目錄下"
		fmt.Println("❌ " + errMsg)
		jsonError(w, errMsg)
		return
	}

	// 執行 go run gemini_gen.go
	// 注意：這需要執行環境有安裝 Go 語言。
	// 如果要在沒有 Go 的環境執行，建議先將 gemini_gen.go 編譯成 gemini_gen.exe，然後改用 exec.Command("./gemini_gen.exe")
	cmd := exec.Command("go", "run", "gemini_gen.go")

	// 捕獲標準輸出與錯誤輸出
	var out bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr

	err := cmd.Run()

	if err != nil {
		// 將詳細的錯誤訊息 (stderr) 回傳給前端
		detailedError := fmt.Sprintf("執行失敗: %v | 詳細訊息: %s", err, stderr.String())
		fmt.Printf("❌ AI 生成失敗: %s\n", detailedError)
		jsonError(w, detailedError)
		return
	}

	// 檢查輸出是否包含成功訊號
	outputStr := out.String()
	if strings.Contains(outputStr, "SUCCESS") {
		fmt.Println("✅ AI 故事生成完畢 (story.json 已更新)")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok", "message": "Story generated successfully"})
	} else {
		fmt.Println("⚠️ AI 執行完成但未檢測到成功訊號，可能未生成檔案")
		// 這裡也可以視為一種錯誤
		jsonError(w, "AI 程式執行完成但無回應 (No SUCCESS signal)")
	}
}
