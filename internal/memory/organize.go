package memory

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// OrganizeResult represents the outcome of the vault reorganization.
type OrganizeResult struct {
	Success    bool
	Message    string
	Diff       string
	TokensUsed int
}

// GetEstimatedTokens estimates token count based on vault file sizes.
func (s *Store) GetEstimatedTokens() int {
	files, err := s.List()
	if err != nil {
		return 800
	}
	var totalChars int64
	for _, f := range files {
		if f.Name == "unified_memory.md" || f.Name == ".audit.log" {
			continue
		}
		totalChars += f.Size
	}
	// 4 characters per token + 800 tokens system prompt overhead
	return int(totalChars/4) + 800
}

// OrganizeVault executes a smart LLM consolidation batch across all memory files.
func (s *Store) OrganizeVault() OrganizeResult {
	return s.OrganizeVaultWithAgent("Direct LLM", "")
}

// OrganizeVaultWithAgent runs consolidation using either a local/remote LLM API or an installed CLI agent command.
func (s *Store) OrganizeVaultWithAgent(agentName string, agentPath string) OrganizeResult {
	// 1. Gather all files and compile them into a unified payload
	files, err := s.List()
	if err != nil {
		return OrganizeResult{Success: false, Message: fmt.Sprintf("Failed to list files: %v", err)}
	}

	var vaultPayload strings.Builder
	for _, f := range files {
		if f.Name == "unified_memory.md" || f.Name == ".audit.log" {
			continue
		}
		content, err := s.View(f.Name)
		if err != nil {
			continue
		}
		vaultPayload.WriteString(fmt.Sprintf("=== FILE: %s ===\n%s\n=== END ===\n\n", f.Name, content))
	}

	if vaultPayload.Len() == 0 {
		return OrganizeResult{Success: true, Message: "Memory vault is empty. Nothing to organize."}
	}

	systemPrompt := `You are an expert Auxly Memory Architect. Your task is to analyze, consolidate, and organize the user's shared memory vault to make it clean, brief, readable, and token-optimized for any AI agents.

Follow these strict principles:
1. DE-DUPLICATION: Scan all files to find identical or highly overlapping chronological facts. Consolidate them into a single, polished sentence or bullet.
2. BRIEFS & SUMMARIZATION: Rewrite walls of text or disorganized daily logs into brief, structured summaries. Clean up messy notes into clean markdown lists.
3. FACTUAL INTEGRITY: Never lose or hallucinate critical facts, developer identity details (e.g. name, contact, portfolio), active project names, or technology scopes. You are organizing, not deleting information.
4. BOUNDARY RESPECT: Retain the strict separation of markdown files (e.g. identity.md, infra.md, preferences.md, projects.md, daily.md, business.md). Do not mix up the sections.
5. TOKEN EFFICIENCY: Make the content clear and concise so it occupies minimal tokens while remaining fully readable.
6. JSON OUTPUT FORMAT: Output your response strictly as a single valid JSON object matching the schema below. Do not add any conversational text or markdown code fences outside this JSON payload:
{
  "files": [
    {
      "name": "filename.md",
      "content": "Full clean, consolidated, readable, and optimized markdown content for this file"
    }
  ]
}`

	userPrompt := fmt.Sprintf("Here is the current memory vault contents to organize:\n\n%s", vaultPayload.String())
	fullPrompt := fmt.Sprintf("%s\n\n%s", systemPrompt, userPrompt)

	var jsonContent string
	var modelUsed string
	var tokensUsed int

	// 2. Route Execution based on agentPath presence
	if agentPath != "" {
		// Guard: only fork/exec an actual executable file. A config directory
		// (e.g. ~/.gemini/antigravity-cli) would fail with a cryptic
		// "permission denied"; fail clearly instead.
		if fi, statErr := os.Stat(agentPath); statErr != nil {
			return OrganizeResult{Success: false, Message: fmt.Sprintf("CLI agent %s is not runnable: %v", agentName, statErr)}
		} else if fi.IsDir() {
			return OrganizeResult{Success: false, Message: fmt.Sprintf("CLI agent %s path is a directory, not an executable: %s", agentName, agentPath)}
		}

		// Run via CLI command (Claude Code, Gemini CLI, etc.)
		var cmd *exec.Cmd
		provider := strings.ToLower(agentName)
		if strings.Contains(provider, "claude") {
			// Claude Code: claude -p "prompt"
			cmd = exec.Command(agentPath, "-p", fullPrompt)
		} else {
			// Generic command-line fallback: execute the binary passing the prompt as an argument
			cmd = exec.Command(agentPath, fullPrompt)
		}

		// Prevent hanging/waiting on TTY stdin by sending EOF immediately
		cmd.Stdin = strings.NewReader("")

		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr

		// Set execution timeout (300 seconds) to prevent hanging
		done := make(chan error, 1)
		go func() {
			done <- cmd.Run()
		}()

		var execErr error
		select {
		case <-time.After(300 * time.Second):
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			return OrganizeResult{Success: false, Message: fmt.Sprintf("CLI agent %s execution timed out after 300s. Stderr: %s", agentName, stderr.String())}
		case err := <-done:
			execErr = err
		}

		if execErr != nil {
			return OrganizeResult{Success: false, Message: fmt.Sprintf("CLI agent %s execution failed: %v\nStderr: %s\nStdout: %s", agentName, execErr, stderr.String(), stdout.String())}
		}

		output := stdout.String()
		if strings.TrimSpace(output) == "" {
			return OrganizeResult{Success: false, Message: fmt.Sprintf("CLI agent %s returned empty output.", agentName)}
		}

		jsonContent = extractJSON(output)
		modelUsed = agentName
		tokensUsed = (len(fullPrompt) + len(jsonContent)) / 4
	} else {
		// Run via direct LLM API calls (Ollama / OpenAI / Gemini)
		apiURL := "http://localhost:11434/v1/chat/completions" // Ollama default
		apiKey := ""
		model := "qwen2.5-coder:7b" // Default fast local model

		if os.Getenv("OPENAI_API_KEY") != "" {
			apiURL = "https://api.openai.com/v1/chat/completions"
			apiKey = os.Getenv("OPENAI_API_KEY")
			model = "gpt-4o-mini"
		} else if os.Getenv("GEMINI_API_KEY") != "" {
			apiURL = "https://generativelanguage.googleapis.com/v1beta/openai/chat/completions"
			apiKey = os.Getenv("GEMINI_API_KEY")
			model = "gemini-1.5-flash"
		} else if os.Getenv("OLLAMA_HOST") != "" {
			apiURL = os.Getenv("OLLAMA_HOST") + "/v1/chat/completions"
		} else if base := strings.TrimRight(os.Getenv("AUXLY_LLM_BASE"), "/"); base != "" {
			// Any OpenAI-compatible endpoint (vLLM, LM Studio, a gateway, etc.).
			apiURL = base + "/v1/chat/completions"
		} else {
			// Last resort: probe a local OpenAI-compatible server on the
			// conventional localhost port (vLLM / LM Studio default). Stays on
			// the loopback interface — never reaches out to the network.
			client := &http.Client{Timeout: 800 * time.Millisecond}
			if resp, err := client.Get("http://localhost:8000/v1/models"); err == nil {
				resp.Body.Close()
				apiURL = "http://localhost:8000/v1/chat/completions"
			}
		}

		// Dynamic self-healing model selector: query installed models on Ollama/vLLM to prevent 404s!
		modelsURL := strings.Replace(apiURL, "/chat/completions", "/models", 1)
		if client := (&http.Client{Timeout: 800 * time.Millisecond}); client != nil {
			if resp, err := client.Get(modelsURL); err == nil {
				defer resp.Body.Close()
				type modelInfo struct {
					ID string `json:"id"`
				}
				type modelsResp struct {
					Data []modelInfo `json:"data"`
				}
				var mr modelsResp
				if err := json.NewDecoder(resp.Body).Decode(&mr); err == nil && len(mr.Data) > 0 {
					model = mr.Data[0].ID
				}
			}
		}

		type msg struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		}
		type reqPayload struct {
			Model          string `json:"model"`
			Messages       []msg  `json:"messages"`
			ResponseFormat *struct {
				Type string `json:"type"`
			} `json:"response_format,omitempty"`
		}

		payload := reqPayload{
			Model: model,
			Messages: []msg{
				{Role: "system", Content: systemPrompt},
				{Role: "user", Content: userPrompt},
			},
		}

		payload.ResponseFormat = &struct {
			Type string `json:"type"`
		}{Type: "json_object"}

		jsonData, err := json.Marshal(payload)
		if err != nil {
			return OrganizeResult{Success: false, Message: fmt.Sprintf("Failed to encode request payload: %v", err)}
		}

		req, err := http.NewRequest("POST", apiURL, bytes.NewBuffer(jsonData))
		if err != nil {
			return OrganizeResult{Success: false, Message: fmt.Sprintf("Failed to create HTTP request: %v", err)}
		}
		req.Header.Set("Content-Type", "application/json")
		if apiKey != "" {
			req.Header.Set("Authorization", "Authorization: Bearer "+apiKey)
		}

		httpClient := &http.Client{Timeout: 300 * time.Second}
		resp, err := httpClient.Do(req)
		if err != nil {
			return OrganizeResult{Success: false, Message: fmt.Sprintf("LLM service is unreachable at %s: %v", apiURL, err)}
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			bodyBytes, _ := io.ReadAll(resp.Body)
			return OrganizeResult{Success: false, Message: fmt.Sprintf("LLM request failed (Status %d): %s", resp.StatusCode, string(bodyBytes))}
		}

		type chatChoice struct {
			Message msg `json:"message"`
		}
		type chatUsage struct {
			TotalTokens int `json:"total_tokens"`
		}
		type chatResponse struct {
			Choices []chatChoice `json:"choices"`
			Usage   chatUsage    `json:"usage"`
		}

		var chatResp chatResponse
		if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
			return OrganizeResult{Success: false, Message: fmt.Sprintf("Failed to parse chat response: %v", err)}
		}

		if len(chatResp.Choices) == 0 {
			return OrganizeResult{Success: false, Message: "LLM returned an empty response choices list."}
		}

		llmJSONContent := chatResp.Choices[0].Message.Content
		llmJSONContent = strings.TrimPrefix(llmJSONContent, "```json")
		llmJSONContent = strings.TrimPrefix(llmJSONContent, "```")
		llmJSONContent = strings.TrimSuffix(llmJSONContent, "```")
		jsonContent = strings.TrimSpace(llmJSONContent)
		modelUsed = model
		tokensUsed = chatResp.Usage.TotalTokens
		if tokensUsed == 0 {
			tokensUsed = (len(fullPrompt) + len(jsonContent)) / 4
		}
	}

	// 3. Decode Response
	type responseFile struct {
		Name    string `json:"name"`
		Content string `json:"content"`
	}
	type responseObj struct {
		Files []responseFile `json:"files"`
	}

	var parsed responseObj
	if err := json.Unmarshal([]byte(jsonContent), &parsed); err != nil {
		return OrganizeResult{Success: false, Message: fmt.Sprintf("Failed to parse JSON vault payload: %v\nOutput content was: %s", err, jsonContent)}
	}

	// 4. Safely overwrite refined files back to disk
	var diffBuilder strings.Builder
	for _, rf := range parsed.Files {
		cleanedName := filepath.Clean(rf.Name)
		if strings.HasPrefix(cleanedName, "..") || filepath.IsAbs(cleanedName) {
			continue
		}
		scope := "global"
		if s.WorkspaceRoot != "" {
			localFile := filepath.Join(s.WorkspaceRoot, cleanedName)
			if _, err := os.Stat(localFile); err == nil {
				scope = "workspace"
			}
		}
		oldContent, _ := s.View(rf.Name)
		_ = s.WriteScoped(cleanedName, rf.Content, scope)

		fileDiff := generateDiff(cleanedName, oldContent, rf.Content)
		if fileDiff != "" {
			diffBuilder.WriteString(fileDiff + "\n")
		}
	}

	return OrganizeResult{Success: true, Message: fmt.Sprintf("✓ Memory vault organized successfully using %s!", modelUsed), Diff: diffBuilder.String(), TokensUsed: tokensUsed}
}

// extractJSON isolates valid JSON brackets from any markdown code blocks or surrounding filler text.
func extractJSON(input string) string {
	if startIdx := strings.Index(input, "```json"); startIdx != -1 {
		rest := input[startIdx+7:]
		if endIdx := strings.Index(rest, "```"); endIdx != -1 {
			return strings.TrimSpace(rest[:endIdx])
		}
	}
	if startIdx := strings.Index(input, "```"); startIdx != -1 {
		rest := input[startIdx+3:]
		if endIdx := strings.Index(rest, "```"); endIdx != -1 {
			return strings.TrimSpace(rest[:endIdx])
		}
	}
	firstBrace := strings.Index(input, "{")
	lastBrace := strings.LastIndex(input, "}")
	if firstBrace != -1 && lastBrace != -1 && lastBrace > firstBrace {
		return input[firstBrace : lastBrace+1]
	}
	return input
}

// OrganizeVaultWithCustom performs memory vault consolidation against a custom HTTP LLM API (like local Ollama, LM Studio, etc.).
func (s *Store) OrganizeVaultWithCustom(endpoint string, model string) OrganizeResult {
	files, err := s.List()
	if err != nil {
		return OrganizeResult{Success: false, Message: fmt.Sprintf("Failed to list files: %v", err)}
	}

	var vaultPayload strings.Builder
	for _, f := range files {
		if f.Name == "unified_memory.md" || f.Name == ".audit.log" {
			continue
		}
		content, err := s.View(f.Name)
		if err != nil {
			continue
		}
		vaultPayload.WriteString(fmt.Sprintf("=== FILE: %s ===\n%s\n=== END ===\n\n", f.Name, content))
	}

	if vaultPayload.Len() == 0 {
		return OrganizeResult{Success: true, Message: "Memory vault is empty. Nothing to organize."}
	}

	systemPrompt := `You are an expert Auxly Memory Architect. Your task is to analyze, consolidate, and organize the user's shared memory vault to make it clean, brief, readable, and token-optimized for any AI agents.

Follow these strict principles:
1. DE-DUPLICATION: Scan all files to find identical or highly overlapping chronological facts. Consolidate them into a single, polished sentence or bullet.
2. BRIEFS & SUMMARIZATION: Rewrite walls of text or disorganized daily logs into brief, structured summaries. Clean up messy notes into clean markdown lists.
3. FACTUAL INTEGRITY: Never lose or hallucinate critical facts, developer identity details (e.g. name, contact, portfolio), active project names, or technology scopes. You are organizing, not deleting information.
4. BOUNDARY RESPECT: Retain the strict separation of markdown files (e.g. identity.md, infra.md, preferences.md, projects.md, daily.md, business.md). Do not mix up the sections.
5. TOKEN EFFICIENCY: Make the content clear and concise so it occupies minimal tokens while remaining fully readable.
6. JSON OUTPUT FORMAT: Output your response strictly as a single valid JSON object matching the schema below. Do not add any conversational text or markdown code fences outside this JSON payload:
{
  "files": [
    {
      "name": "filename.md",
      "content": "Full clean, consolidated, readable, and optimized markdown content for this file"
    }
  ]
}`

	userPrompt := fmt.Sprintf("Here is the current memory vault contents to organize:\n\n%s", vaultPayload.String())
	fullPrompt := fmt.Sprintf("%s\n\n%s", systemPrompt, userPrompt)

	apiURL := strings.TrimRight(endpoint, "/")
	if !strings.HasSuffix(apiURL, "/v1/chat/completions") && !strings.HasSuffix(apiURL, "/chat/completions") {
		apiURL = apiURL + "/v1/chat/completions"
	}

	type msg struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	type reqPayload struct {
		Model          string `json:"model"`
		Messages       []msg  `json:"messages"`
		ResponseFormat *struct {
			Type string `json:"type"`
		} `json:"response_format,omitempty"`
	}

	payload := reqPayload{
		Model: model,
		Messages: []msg{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt},
		},
	}

	payload.ResponseFormat = &struct {
		Type string `json:"type"`
	}{Type: "json_object"}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return OrganizeResult{Success: false, Message: fmt.Sprintf("Failed to encode request payload: %v", err)}
	}

	req, err := http.NewRequest("POST", apiURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return OrganizeResult{Success: false, Message: fmt.Sprintf("Failed to create HTTP request: %v", err)}
	}
	req.Header.Set("Content-Type", "application/json")

	httpClient := &http.Client{Timeout: 300 * time.Second}
	resp, err := httpClient.Do(req)
	if err != nil {
		return OrganizeResult{Success: false, Message: fmt.Sprintf("LLM service is unreachable at %s: %v", apiURL, err)}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return OrganizeResult{Success: false, Message: fmt.Sprintf("LLM request failed (Status %d): %s", resp.StatusCode, string(bodyBytes))}
	}

	type chatChoice struct {
		Message msg `json:"message"`
	}
	type chatUsage struct {
		TotalTokens int `json:"total_tokens"`
	}
	type chatResponse struct {
		Choices []chatChoice `json:"choices"`
		Usage   chatUsage    `json:"usage"`
	}

	var chatResp chatResponse
	if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
		return OrganizeResult{Success: false, Message: fmt.Sprintf("Failed to parse chat response: %v", err)}
	}

	if len(chatResp.Choices) == 0 {
		return OrganizeResult{Success: false, Message: "LLM returned an empty response choices list."}
	}

	llmJSONContent := chatResp.Choices[0].Message.Content
	llmJSONContent = strings.TrimPrefix(llmJSONContent, "```json")
	llmJSONContent = strings.TrimPrefix(llmJSONContent, "```")
	llmJSONContent = strings.TrimSuffix(llmJSONContent, "```")
	jsonContent := strings.TrimSpace(llmJSONContent)

	tokensUsed := chatResp.Usage.TotalTokens
	if tokensUsed == 0 {
		tokensUsed = (len(fullPrompt) + len(jsonContent)) / 4
	}

	type responseFile struct {
		Name    string `json:"name"`
		Content string `json:"content"`
	}
	type responseObj struct {
		Files []responseFile `json:"files"`
	}

	var parsed responseObj
	if err := json.Unmarshal([]byte(jsonContent), &parsed); err != nil {
		return OrganizeResult{Success: false, Message: fmt.Sprintf("Failed to parse JSON vault payload: %v\nOutput content was: %s", err, jsonContent)}
	}

	var diffBuilder strings.Builder
	for _, rf := range parsed.Files {
		cleanedName := filepath.Clean(rf.Name)
		if strings.HasPrefix(cleanedName, "..") || filepath.IsAbs(cleanedName) {
			continue
		}
		scope := "global"
		if s.WorkspaceRoot != "" {
			localFile := filepath.Join(s.WorkspaceRoot, cleanedName)
			if _, err := os.Stat(localFile); err == nil {
				scope = "workspace"
			}
		}
		oldContent, _ := s.View(rf.Name)
		_ = s.WriteScoped(cleanedName, rf.Content, scope)

		fileDiff := generateDiff(cleanedName, oldContent, rf.Content)
		if fileDiff != "" {
			diffBuilder.WriteString(fileDiff + "\n")
		}
	}

	return OrganizeResult{Success: true, Message: fmt.Sprintf("✓ Memory vault organized successfully using custom model %s!", model), Diff: diffBuilder.String(), TokensUsed: tokensUsed}
}

// generateDiff creates a clean line-by-line de-duplication diff showing exactly which lines were removed (-) and added (+).
func generateDiff(filename, oldStr, newStr string) string {
	if oldStr == newStr {
		return ""
	}
	oldLines := strings.Split(oldStr, "\n")
	newLines := strings.Split(newStr, "\n")

	var diff strings.Builder
	diff.WriteString(fmt.Sprintf("### 📄 %s\n", filename))
	diff.WriteString("```diff\n")

	oldMap := make(map[string]bool)
	for _, l := range oldLines {
		if strings.TrimSpace(l) != "" {
			oldMap[strings.TrimSpace(l)] = true
		}
	}

	newMap := make(map[string]bool)
	for _, l := range newLines {
		if strings.TrimSpace(l) != "" {
			newMap[strings.TrimSpace(l)] = true
		}
	}

	deletedCount := 0
	for _, l := range oldLines {
		tr := strings.TrimSpace(l)
		if tr != "" && !newMap[tr] {
			diff.WriteString(fmt.Sprintf("- %s\n", l))
			deletedCount++
		}
	}

	addedCount := 0
	for _, l := range newLines {
		tr := strings.TrimSpace(l)
		if tr != "" && !oldMap[tr] {
			diff.WriteString(fmt.Sprintf("+ %s\n", l))
			addedCount++
		}
	}

	diff.WriteString("```\n")
	if deletedCount == 0 && addedCount == 0 {
		return ""
	}
	return diff.String()
}
