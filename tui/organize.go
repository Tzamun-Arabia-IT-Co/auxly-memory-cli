package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/audit"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/detect"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/memory"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type orgMode int

const (
	orgIdle orgMode = iota
	orgRunning
	orgReview
	orgEditing
	orgDone
)

type orgDecision int

const (
	decPending orgDecision = iota
	decApproved
	decRejected
)

type orgFocus int

const (
	focusProvider orgFocus = iota
	focusModel
)

type orgProvider struct {
	kind    string // "agent", "api", or "separator"
	id      string
	label   string
	command string
}

var apiOrgProviders = []orgProvider{
	{kind: "api", id: "ollama", label: "Ollama (local)"},
	{kind: "api", id: "openai", label: "OpenAI"},
	{kind: "api", id: "gemini", label: "Gemini"},
	{kind: "api", id: "custom", label: "Custom URL..."},
}

var apiDefaultModels = map[string]string{
	"ollama": "qwen2.5-coder:7b",
	"openai": "gpt-4o-mini",
	"gemini": "gemini-1.5-flash",
	"custom": "qwen2.5-coder:7b",
}

var agentModels = map[string][]string{
	"claude":      []string{"haiku (fast)", "sonnet", "opus"},
	"codex":       []string{"(default)", "gpt-5.2-codex", "gpt-5.2"},
	"antigravity": []string{"(uses its configured model)"},
	"gemini":      []string{"(default)", "gemini-2.5-flash", "gemini-2.5-pro"},
	"cursor":      []string{"(default)", "sonnet-4", "gpt-5"},
}

var (
	orgPanelStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(ColorDim).
			Padding(0, 1)
	orgPanelTitleStyle = lipgloss.NewStyle().Bold(true).Foreground(ColorPrimary)
	orgDimStyle        = lipgloss.NewStyle().Foreground(ColorDim)
	orgGoodStyle       = lipgloss.NewStyle().Foreground(ColorSuccess)
	orgWarnStyle       = lipgloss.NewStyle().Foreground(ColorWarning)
	orgBadStyle        = lipgloss.NewStyle().Foreground(ColorDanger)
	orgBoldStyle       = lipgloss.NewStyle().Bold(true)
	// Diff line styles for the before/after panes.
	orgRemovedStyle = lipgloss.NewStyle().Foreground(ColorDanger)
	orgAddedStyle   = lipgloss.NewStyle().Foreground(ColorSuccess)
)

type organizeModel struct {
	store      *memory.Store
	logger     *audit.Logger
	memoryPath string
	mode       orgMode
	width      int
	height     int

	provIdx     int
	focus       orgFocus
	providers   []orgProvider
	customURL   string
	customInput textinput.Model

	models     []string
	modelIdx   int
	fetching   bool
	fetchErr   string
	picked     string
	urlEditing bool
	confirming bool // [y]/[n] confirmation popup is up before a run

	lastRun          string
	lastTokensUsed   int
	lastFilesChanged int
	estTokens        int // approx tokens the vault will send (shown pre-run)
	estFiles         int // organizable files that will be sent
	spin             int
	runProvider      string             // provider label shown on the running screen
	runModel         string             // model label shown on the running screen
	runCancel        context.CancelFunc // cancels the in-flight agent/API run (esc on the running screen)

	proposal  memory.OrganizeProposal
	changes   []memory.ProposedChange
	decisions []orgDecision

	fileCursor int
	beforeVP   viewport.Model
	afterVP    viewport.Model
	editor     textarea.Model

	status  string
	errMsg  string
	summary string
	diff    string
}

type orgRunMsg struct {
	prop memory.OrganizeProposal
	res  memory.OrganizeResult
}

type orgSpinTickMsg struct{}

type organizeStats struct {
	LastRun      string `json:"last_run"`
	TokensUsed   int    `json:"tokens_used"`
	FilesChanged int    `json:"files_changed"`
}

type orgModelsFetchedMsg struct {
	success bool
	models  []string
	err     string
}

func orgSpinTick() tea.Cmd {
	return tea.Tick(120*time.Millisecond, func(time.Time) tea.Msg { return orgSpinTickMsg{} })
}

func newOrganizeModel(store *memory.Store, memoryPath string, logger *audit.Logger) organizeModel {
	editor := textarea.New()
	editor.Prompt = ""
	editor.ShowLineNumbers = true
	editor.CharLimit = 0
	editor.SetWidth(80)
	editor.SetHeight(18)
	editor.Blur()

	customInput := textinput.New()
	customInput.Prompt = ""
	customInput.SetValue("http://localhost:11434")
	customInput.CharLimit = 2048
	customInput.Blur()

	lastRun, tokens, filesChanged := readOrganizeStats(memoryPath)
	providers, initialIdx := buildOrgProviders(detect.InstalledAgents())
	models := modelsForProvider(providers[initialIdx])
	picked := modelValueForProvider(providers[initialIdx], firstModelLabel(providers[initialIdx], models))
	mdl := organizeModel{
		store:            store,
		logger:           logger,
		memoryPath:       memoryPath,
		providers:        providers,
		provIdx:          initialIdx,
		customURL:        "http://localhost:11434",
		customInput:      customInput,
		models:           models,
		picked:           picked,
		lastRun:          lastRun,
		lastTokensUsed:   tokens,
		lastFilesChanged: filesChanged,
		beforeVP:         viewport.New(40, 10),
		afterVP:          viewport.New(40, 10),
		editor:           editor,
	}
	mdl.refreshEstimate()
	return mdl
}

// refreshEstimate recomputes the approximate token cost and organizable-file count
// of the current vault, shown on the idle screen so the user knows the payload size
// before launching a run. Recomputed at startup and after each write.
func (m *organizeModel) refreshEstimate() {
	m.estTokens = m.store.GetEstimatedTokens()
	m.estFiles = 0
	if files, err := m.store.List(); err == nil {
		for _, f := range files {
			if memory.IsOrganizableFile(f.Name) {
				m.estFiles++
			}
		}
	}
}

func buildOrgProviders(agents []detect.Agent) ([]orgProvider, int) {
	var agentProviders []orgProvider
	seen := map[string]bool{}
	for _, a := range agents {
		isCLI := strings.Contains(a.Name, "CLI") || strings.Contains(a.Name, "Code") || a.Connection == "MCP+Shell" || a.Connection == "Shell"
		// Only offer agents whose headless, isolated invocation is VERIFIED
		// (orgProviderKey recognizes them: claude, codex, antigravity, gemini,
		// cursor). An unrecognized CLI would fall back to a bare `-p` that may open
		// interactive mode and hang — so exclude it from the organize picker.
		if !isCLI || a.Command == "" || seen[a.Provider] || orgProviderKey(a.Name) == "" {
			continue
		}
		seen[a.Provider] = true
		label := a.Name
		if orgProviderKey(a.Name) == "claude" {
			label = "Claude Code (Recommended)"
			agentProviders = append([]orgProvider{{kind: "agent", id: a.Name, label: label, command: a.Command}}, agentProviders...)
			continue
		}
		agentProviders = append(agentProviders, orgProvider{kind: "agent", id: a.Name, label: label, command: a.Command})
	}
	providers := append([]orgProvider{}, agentProviders...)
	providers = append(providers, orgProvider{kind: "separator", label: "── Local / API ──"})
	providers = append(providers, apiOrgProviders...)

	initial := 0
	if len(agentProviders) == 0 {
		initial = 1
	}
	return providers, initial
}

func orgProviderKey(name string) string {
	switch p := strings.ToLower(name); {
	case strings.Contains(p, "claude"):
		return "claude"
	case strings.Contains(p, "codex"):
		return "codex"
	case strings.Contains(p, "antigravity") || strings.Contains(p, "agy"):
		return "antigravity"
	case strings.Contains(p, "gemini"):
		return "gemini"
	case strings.Contains(p, "cursor"):
		return "cursor"
	default:
		return ""
	}
}

func modelsForProvider(p orgProvider) []string {
	if p.kind == "agent" {
		if models := agentModels[orgProviderKey(p.id)]; len(models) > 0 {
			return append([]string(nil), models...)
		}
		return []string{"(default)"}
	}
	if p.kind == "api" {
		return nil
	}
	return nil
}

func firstModelLabel(p orgProvider, models []string) string {
	if len(models) > 0 {
		return models[0]
	}
	if p.kind == "api" {
		return apiDefaultModels[p.id]
	}
	return "(default)"
}

func modelValueForProvider(p orgProvider, label string) string {
	if p.kind == "api" {
		if label != "" {
			return label
		}
		return apiDefaultModels[p.id]
	}
	if strings.HasPrefix(label, "(") {
		return ""
	}
	if label == "haiku (fast)" {
		return "haiku"
	}
	return strings.Fields(label)[0]
}

func (m organizeModel) Refresh() tea.Cmd { return nil }

func (m organizeModel) capturesInput() bool {
	// orgRunning captures input so esc/ctrl+c route here to cancel the run rather than
	// quitting the app or switching tabs; the screen is focus-locked until the run
	// finishes or the user cancels.
	return m.mode == orgEditing || m.urlEditing || m.confirming || m.mode == orgRunning
}

func readOrganizeStats(memoryPath string) (lastRun string, tokens, filesChanged int) {
	var stats organizeStats
	data, err := os.ReadFile(filepath.Join(memoryPath, ".last_organize.json"))
	if err != nil {
		return "", 0, 0
	}
	_ = json.Unmarshal(data, &stats)
	return stats.LastRun, stats.TokensUsed, stats.FilesChanged
}

func (m organizeModel) fetchModelsCmd(provider, customURL string) tea.Cmd {
	return func() tea.Msg {
		baseURL, apiKey, errMsg, ok := resolveOrgModelsProvider(provider, customURL)
		if !ok {
			return orgModelsFetchedMsg{success: false, err: errMsg}
		}
		endpoint := strings.TrimRight(baseURL, "/") + "/v1/models"
		client := &http.Client{Timeout: 3 * time.Second}
		resp, err := getOrgModels(client, endpoint, apiKey)
		if err != nil && provider == "ollama" {
			endpoint = strings.TrimRight(baseURL, "/") + "/api/tags"
			resp, err = getOrgModels(client, endpoint, apiKey)
		}
		if err != nil {
			return orgModelsFetchedMsg{success: false, err: "Endpoint is unreachable: " + err.Error()}
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return orgModelsFetchedMsg{success: false, err: fmt.Sprintf("HTTP status %d", resp.StatusCode)}
		}

		bodyBytes, _ := io.ReadAll(resp.Body)

		type modelItem struct {
			ID string `json:"id"`
		}
		type modelsResp struct {
			Data []modelItem `json:"data"`
		}

		type ollamaModelItem struct {
			Name string `json:"name"`
		}
		type ollamaModelsResp struct {
			Models []ollamaModelItem `json:"models"`
		}

		var mResp modelsResp
		if err := json.Unmarshal(bodyBytes, &mResp); err == nil && len(mResp.Data) > 0 {
			var list []string
			for _, item := range mResp.Data {
				list = append(list, item.ID)
			}
			return orgModelsFetchedMsg{success: true, models: list}
		}

		var oResp ollamaModelsResp
		if err := json.Unmarshal(bodyBytes, &oResp); err == nil && len(oResp.Models) > 0 {
			var list []string
			for _, item := range oResp.Models {
				list = append(list, item.Name)
			}
			return orgModelsFetchedMsg{success: true, models: list}
		}

		return orgModelsFetchedMsg{success: false, err: "Failed to parse model list from endpoint response"}
	}
}

func getOrgModels(client *http.Client, endpoint, apiKey string) (*http.Response, error) {
	req, err := http.NewRequest("GET", endpoint, nil)
	if err != nil {
		return nil, err
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	return client.Do(req)
}

func resolveOrgModelsProvider(provider, customURL string) (baseURL, apiKey, errMsg string, ok bool) {
	switch provider {
	case "ollama":
		return "http://localhost:11434", "", "", true
	case "openai":
		key := os.Getenv("OPENAI_API_KEY")
		if key == "" {
			return "", "", "OPENAI_API_KEY not set - export it and relaunch", false
		}
		return "https://api.openai.com", key, "", true
	case "gemini":
		key := os.Getenv("GEMINI_API_KEY")
		if key == "" {
			return "", "", "GEMINI_API_KEY not set - export it and relaunch", false
		}
		return "https://generativelanguage.googleapis.com/v1beta/openai", key, "", true
	case "custom":
		base := strings.TrimRight(strings.TrimSpace(customURL), "/")
		if base == "" {
			return "", "", "Custom endpoint URL is empty", false
		}
		return base, os.Getenv("AUXLY_LLM_KEY"), "", true
	default:
		return "", "", fmt.Sprintf("Unknown provider %q", provider), false
	}
}

func (m organizeModel) currentProvider() orgProvider {
	if m.provIdx < 0 || m.provIdx >= len(m.providers) {
		for _, p := range m.providers {
			if p.kind != "separator" {
				return p
			}
		}
		return apiOrgProviders[0]
	}
	return m.providers[m.provIdx]
}

func (m organizeModel) selectedModel() string {
	if len(m.models) > 0 && m.modelIdx >= 0 && m.modelIdx < len(m.models) {
		return modelValueForProvider(m.currentProvider(), m.models[m.modelIdx])
	}
	if m.picked != "" {
		return m.picked
	}
	return modelValueForProvider(m.currentProvider(), firstModelLabel(m.currentProvider(), nil))
}

func (m organizeModel) planTarget() (provider, target, model string) {
	p := m.currentProvider()
	if p.kind == "agent" {
		return p.id, p.command, m.selectedModel()
	}
	return p.id, m.customURL, m.selectedModel()
}

func (m organizeModel) runPlanCmd(ctx context.Context) tea.Cmd {
	store := m.store
	provider, target, model := m.planTarget()
	prov := m.currentProvider()
	return func() tea.Msg {
		var prop memory.OrganizeProposal
		var res memory.OrganizeResult
		if prov.kind == "agent" {
			prop, res = store.PlanOrganizeWithAgent(ctx, provider, target, model)
		} else {
			prop, res = store.PlanOrganizeWithProvider(ctx, provider, target, model)
		}
		return orgRunMsg{prop: prop, res: res}
	}
}

func (m organizeModel) Update(msg tea.Msg) (organizeModel, tea.Cmd) {
	switch msg := msg.(type) {
	case orgModelsFetchedMsg:
		m.fetching = false
		if msg.success {
			m.models = msg.models
			m.modelIdx = 0
			m.fetchErr = ""
			if len(m.models) > 0 {
				m.picked = modelValueForProvider(m.currentProvider(), m.models[0])
			}
		} else {
			m.fetchErr = msg.err
			m.models = nil
		}
		return m, nil
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.resizeReviewPanes()
		m.resizeCustomInput()
		return m, nil
	case orgSpinTickMsg:
		if m.mode == orgRunning {
			m.spin++
			return m, orgSpinTick()
		}
		return m, nil
	case orgRunMsg:
		// If the user cancelled (we already left orgRunning), drop the late result —
		// don't flash the ctx-cancelled error or jump into a review they abandoned.
		if m.mode != orgRunning {
			return m, nil
		}
		// The run finished naturally — release the context's resources (the cancel
		// func MUST be called even on success) before clearing it.
		if m.runCancel != nil {
			m.runCancel()
			m.runCancel = nil
		}
		if !msg.res.Success {
			m.mode = orgIdle
			m.errMsg = msg.res.Message
			m.status = ""
			return m, nil
		}
		filtered := make([]memory.ProposedChange, 0, len(msg.prop.Changes))
		for _, c := range msg.prop.Changes {
			if hasRealChange(c) {
				filtered = append(filtered, c)
			}
		}
		if len(filtered) == 0 {
			m.mode = orgIdle
			m.status = "Nothing to organize."
			m.errMsg = ""
			return m, nil
		}
		m.proposal = msg.prop
		m.changes = append([]memory.ProposedChange(nil), filtered...)
		m.decisions = make([]orgDecision, len(m.changes))
		m.fileCursor = 0
		m.mode = orgReview
		m.status = ""
		m.errMsg = ""
		m.loadCurrentChange()
		return m, nil
	case tea.MouseMsg:
		return m.handleMouse(msg)
	case tea.KeyMsg:
		switch m.mode {
		case orgIdle:
			return m.updateIdle(msg)
		case orgRunning:
			return m.updateRunning(msg)
		case orgReview:
			return m.updateReview(msg)
		case orgEditing:
			return m.updateEditing(msg)
		case orgDone:
			m.reset()
			return m, nil
		}
	}
	return m, nil
}

// updateRunning lets the user abort an in-flight organize run. esc/ctrl+c cancels
// the context (killing the agent subprocess or HTTP request) and returns to idle;
// the late orgRunMsg that follows is dropped by the mode guard in Update.
func (m organizeModel) updateRunning(msg tea.KeyMsg) (organizeModel, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c", "q":
		if m.runCancel != nil {
			m.runCancel()
			m.runCancel = nil
		}
		m.mode = orgIdle
		m.status = "Run cancelled."
		m.errMsg = ""
		return m, nil
	}
	return m, nil
}

// contentTopY is the absolute terminal row (0-based) where the Memory Org body
// begins, i.e. just below the app's logo banner + tab bar + blank separator. The
// app renders `banner\n tabs\n\n content`, so content starts at bannerH + 2 (tabs
// height) + 1 (the blank row).
func (m organizeModel) contentTopY() int {
	bannerH := 7
	if m.width > 0 {
		bannerH = lipgloss.Height(renderBanner(m.width))
	}
	return bannerH + 3
}

func (m organizeModel) handleMouse(msg tea.MouseMsg) (organizeModel, tea.Cmd) {
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		if m.mode == orgReview {
			m.beforeVP.ScrollUp(2)
			m.afterVP.ScrollUp(2)
		} else if m.mode == orgIdle {
			return m.updateIdle(tea.KeyMsg{Type: tea.KeyUp})
		}
		return m, nil
	case tea.MouseButtonWheelDown:
		if m.mode == orgReview {
			m.beforeVP.ScrollDown(2)
			m.afterVP.ScrollDown(2)
		} else if m.mode == orgIdle {
			return m.updateIdle(tea.KeyMsg{Type: tea.KeyDown})
		}
		return m, nil
	}
	if msg.Button != tea.MouseButtonLeft || msg.Action != tea.MouseActionPress {
		return m, nil
	}
	if m.mode == orgReview {
		return m.handleReviewClick(msg)
	}
	return m, nil
}

// reviewActionButtons returns the action-bar buttons in render order, matching the
// actions line in reviewView (used for click hit-testing).
func reviewActionButtons() []struct{ text, act string } {
	return []struct{ text, act string }{
		{"[a] Approve", "approve"},
		{"[r] Reject", "reject"},
		{"[e] Edit", "edit"},
		{"[A] Approve all", "approveall"},
	}
}

func (m organizeModel) handleReviewClick(msg tea.MouseMsg) (organizeModel, tea.Cmd) {
	top := m.contentTopY()
	statusOff := 0
	if m.status != "" {
		statusOff = 1
	}
	stripY := top + statusOff + 3
	panelBoxH := m.beforeVP.Height + 4 // in-panel title + content + top/bottom border
	actionsY := top + statusOff + 4 + panelBoxH

	// Click a file dot in the strip → jump to that file. The strip is
	// "Files: " (7 cols) followed by ~2-col markers.
	if msg.Y == stripY {
		idx := (msg.X - 7) / 2
		if idx >= 0 && idx < len(m.changes) {
			m.fileCursor = idx
			m.loadCurrentChange()
		}
		return m, nil
	}

	// Click an action-bar button.
	if msg.Y == actionsY {
		x := 0
		for _, b := range reviewActionButtons() {
			w := lipgloss.Width(b.text)
			if msg.X >= x && msg.X < x+w {
				return m.applyAction(b.act)
			}
			x += w + 3 // the "   " separator between chips
		}
	}
	return m, nil
}

// applyAction performs a review action (from a key or a click). Approving or
// rejecting a file auto-advances to the next undecided file so the user can sweep
// through the list without manually moving; if every file is decided it lands on
// the last one (then Enter submits).
func (m organizeModel) applyAction(act string) (organizeModel, tea.Cmd) {
	advance := false
	switch act {
	case "approve":
		if m.fileCursor < len(m.decisions) {
			m.decisions[m.fileCursor] = decApproved
		}
		advance = true
	case "reject":
		if m.fileCursor < len(m.decisions) {
			m.decisions[m.fileCursor] = decRejected
		}
		advance = true
	case "approveall":
		for i := range m.decisions {
			if m.decisions[i] == decPending {
				m.decisions[i] = decApproved
			}
		}
	case "edit":
		if m.fileCursor < len(m.changes) {
			m.mode = orgEditing
			m.editor.SetValue(m.changes[m.fileCursor].NewContent)
			m.editor.Focus()
			m.resizeEditor()
		}
		return m, nil
	}
	if advance {
		m.advanceToNextUndecided()
	}
	return m, nil
}

// advanceToNextUndecided moves the cursor to the next still-pending file (wrapping
// once from the end); if none remain pending it advances one file, clamped to the
// last. It reloads the before/after panes for the new file.
func (m *organizeModel) advanceToNextUndecided() {
	n := len(m.changes)
	if n == 0 {
		return
	}
	for off := 1; off <= n; off++ {
		i := (m.fileCursor + off) % n
		if i < len(m.decisions) && m.decisions[i] == decPending {
			m.fileCursor = i
			m.loadCurrentChange()
			return
		}
	}
	// Everything decided — just step to the next file (clamped to the last).
	if m.fileCursor < n-1 {
		m.fileCursor++
		m.loadCurrentChange()
	}
}

func (m organizeModel) updateIdle(msg tea.KeyMsg) (organizeModel, tea.Cmd) {
	// Confirmation popup owns the keyboard while it is up.
	if m.confirming {
		switch msg.String() {
		case "y", "Y", "enter":
			m.confirming = false
			return m.startRun()
		case "n", "N", "esc", "q":
			m.confirming = false
		}
		return m, nil
	}
	if m.urlEditing {
		switch msg.String() {
		case "enter":
			m.customURL = strings.TrimSpace(m.customInput.Value())
			if m.customURL == "" {
				m.customURL = "http://localhost:11434"
				m.customInput.SetValue(m.customURL)
			}
			m.urlEditing = false
			m.customInput.Blur()
			// Saving the URL auto-fetches its models and jumps to the model list,
			// so the user lands straight on "pick a model".
			m.fetching = true
			m.fetchErr = ""
			m.models = nil
			m.focus = focusModel
			return m, m.fetchModelsCmd(m.currentProvider().id, m.customURL)
		case "esc":
			m.customInput.SetValue(m.customURL)
			m.urlEditing = false
			m.customInput.Blur()
			return m, nil
		}
		var cmd tea.Cmd
		m.customInput, cmd = m.customInput.Update(msg)
		return m, cmd
	}

	switch msg.String() {
	case "up", "k":
		if m.focus == focusModel {
			if len(m.models) > 0 && m.modelIdx > 0 {
				m.modelIdx--
				m.picked = modelValueForProvider(m.currentProvider(), m.models[m.modelIdx])
			}
		} else {
			if m.moveProvider(-1) {
				m.resetProviderModels()
			}
		}
	case "down", "j":
		if m.focus == focusModel {
			if len(m.models) > 0 && m.modelIdx < len(m.models)-1 {
				m.modelIdx++
				m.picked = modelValueForProvider(m.currentProvider(), m.models[m.modelIdx])
			}
		} else {
			if m.moveProvider(1) {
				m.resetProviderModels()
			}
		}
	case "tab", "right", "left":
		if m.focus == focusProvider {
			m.focus = focusModel
			// Moving to the model list on an API provider with nothing fetched yet
			// kicks off a fetch automatically, so the user never has to discover "f".
			if m.currentProvider().kind == "api" && len(m.models) == 0 && !m.fetching {
				m.fetching = true
				m.fetchErr = ""
				return m, m.fetchModelsCmd(m.currentProvider().id, m.customURL)
			}
		} else {
			m.focus = focusProvider
		}
	case "e":
		if m.currentProvider().id == "custom" && m.focus == focusProvider {
			m.customInput.SetValue(m.customURL)
			m.customInput.Focus()
			m.urlEditing = true
			m.resizeCustomInput()
		}
	case "f":
		if m.currentProvider().kind == "api" && !m.fetching {
			m.customURL = strings.TrimSpace(m.customInput.Value())
			m.fetching = true
			m.fetchErr = ""
			return m, m.fetchModelsCmd(m.currentProvider().id, m.customURL)
		}
	case "enter":
		if m.focus == focusProvider {
			// Custom URL: Enter opens the URL field to fill first.
			if m.currentProvider().id == "custom" {
				m.customInput.SetValue(m.customURL)
				m.customInput.Focus()
				m.urlEditing = true
				m.resizeCustomInput()
				return m, nil
			}
			// Otherwise advance to the Model block (auto-fetch for API providers).
			m.focus = focusModel
			if m.currentProvider().kind == "api" && len(m.models) == 0 && !m.fetching {
				m.fetching = true
				m.fetchErr = ""
				return m, m.fetchModelsCmd(m.currentProvider().id, m.customURL)
			}
			return m, nil
		}
		// Model block: lock in the chosen model and ask for confirmation.
		if len(m.models) > 0 {
			m.picked = modelValueForProvider(m.currentProvider(), m.models[m.modelIdx])
		} else {
			m.picked = modelValueForProvider(m.currentProvider(), firstModelLabel(m.currentProvider(), nil))
		}
		m.confirming = true
		return m, nil
	}
	return m, nil
}

// startRun transitions from the confirmation popup into the running state.
func (m organizeModel) startRun() (organizeModel, tea.Cmd) {
	m.mode = orgRunning
	m.spin = 0
	m.status = ""
	m.errMsg = ""
	m.runProvider = m.currentProvider().label
	if mdl := strings.TrimSpace(m.picked); mdl != "" {
		m.runModel = mdl
	} else {
		m.runModel = "default model"
	}
	// Cancellable context so esc on the running screen tears down the subprocess /
	// HTTP request rather than leaving it to run out the timeout in the background.
	ctx, cancel := context.WithCancel(context.Background())
	m.runCancel = cancel
	return m, tea.Batch(m.runPlanCmd(ctx), orgSpinTick())
}

func (m *organizeModel) moveProvider(delta int) bool {
	next := m.provIdx + delta
	for next >= 0 && next < len(m.providers) {
		if m.providers[next].kind != "separator" {
			m.provIdx = next
			return true
		}
		next += delta
	}
	return false
}

func (m *organizeModel) resetProviderModels() {
	m.models = modelsForProvider(m.currentProvider())
	m.modelIdx = 0
	m.fetchErr = ""
	m.fetching = false
	m.picked = modelValueForProvider(m.currentProvider(), firstModelLabel(m.currentProvider(), m.models))
}

func (m organizeModel) updateReview(msg tea.KeyMsg) (organizeModel, tea.Cmd) {
	switch msg.String() {
	case "left", "h", "[":
		if m.fileCursor > 0 {
			m.fileCursor--
			m.loadCurrentChange()
		}
	case "right", "l", "]":
		if m.fileCursor < len(m.changes)-1 {
			m.fileCursor++
			m.loadCurrentChange()
		}
	case "up", "k":
		m.beforeVP.ScrollUp(1)
		m.afterVP.ScrollUp(1)
	case "down", "j":
		m.beforeVP.ScrollDown(1)
		m.afterVP.ScrollDown(1)
	case "a":
		return m.applyAction("approve")
	case "r":
		return m.applyAction("reject")
	case "A":
		return m.applyAction("approveall")
	case "e":
		return m.applyAction("edit")
	case "enter":
		return m.submit()
	case "esc", "q":
		m.reset()
		return m, nil
	}
	return m, nil
}

func (m organizeModel) updateEditing(msg tea.KeyMsg) (organizeModel, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = orgReview
		m.editor.Blur()
		return m, nil
	case "ctrl+s":
		if m.fileCursor < len(m.changes) {
			m.changes[m.fileCursor].NewContent = m.editor.Value()
			m.decisions[m.fileCursor] = decApproved
			m.mode = orgReview
			m.editor.Blur()
			m.loadCurrentChange()
		}
		return m, nil
	}
	var cmd tea.Cmd
	m.editor, cmd = m.editor.Update(msg)
	return m, cmd
}

func (m organizeModel) submit() (organizeModel, tea.Cmd) {
	approved := make([]memory.ProposedChange, 0, len(m.changes))
	for i, c := range m.changes {
		if i < len(m.decisions) && m.decisions[i] == decApproved {
			approved = append(approved, c)
		}
	}
	if len(approved) == 0 {
		m.status = "No files approved - nothing applied."
		return m, nil
	}
	// Apply the approved subset, then record each file that actually changed to the
	// audit log so the write shows up as durable history in the Audit Trail tab. The
	// per-file diff is logged too, so the user can inspect exactly what was written.
	m.diff = m.store.ApplyOrganizeChanges(approved)
	// Attribute the write to a CANONICAL brand id, never the raw display label —
	// logging "Claude Code (Recommended)" created a phantom agent card. canonicalProvider
	// folds that label into the real "claude-code" brand (and leaves "antigravity" etc.
	// untouched); an unmappable label falls back to the non-card "organize" tag.
	provider := canonicalProvider(m.runProvider)
	if provider == "" {
		provider = "organize"
	}
	written := 0
	var writtenNames []string
	var summaryLines []string
	for _, c := range approved {
		if !c.Changed() {
			continue
		}
		written++
		added, removed := lineDelta(c.OldContent, c.NewContent)
		writtenNames = append(writtenNames, c.Name)
		verb := "updated"
		if c.IsNew {
			verb = "created"
		}
		summaryLines = append(summaryLines, fmt.Sprintf("  %s  %s  %s / %s",
			orgGoodStyle.Render("✓"), c.Name,
			orgAddedStyle.Render(fmt.Sprintf("+%d", added)),
			orgRemovedStyle.Render(fmt.Sprintf("-%d", removed)))+"  "+orgDimStyle.Render(verb))
		if m.logger != nil {
			// action "write" so the change shows up in the Audit Trail, Activity feed,
			// and dashboard recent-writes alongside every other memory write; provider
			// and reason mark it as an on-demand organize operation.
			diff := generateLineDiff(c.OldContent, c.NewContent)
			if diff == "" {
				diff = "~ " + c.Name + " reorganized\n"
			}
			_, _ = m.logger.Log("auxly-organize", provider, "write", c.Name,
				diff, "On-demand memory organization", "trusted")
		}
	}
	m.writeStats(written)
	m.refreshEstimate() // vault changed — keep the idle preview accurate

	if written == 0 {
		m.summary = orgWarnStyle.Render("Nothing written — the approved files matched what was already on disk.")
	} else {
		head := fmt.Sprintf("%s  %s",
			orgGoodStyle.Bold(true).Render(fmt.Sprintf("✓ Wrote %d file(s) to your memory vault", written)),
			orgDimStyle.Render("("+strings.Join(writtenNames, ", ")+")"))
		m.summary = head + "\n" + strings.Join(summaryLines, "\n") +
			"\n\n" + orgDimStyle.Render("Saved to "+m.memoryPath+"  ·  full history in Audit Trail (press 0)")
	}
	m.mode = orgDone
	return m, m.Refresh()
}

// generateLineDiff builds a plain-text +/- line diff (set-based on trimmed lines)
// for the audit-log Diff field, so the Audit Trail entry shows exactly what the
// organize write changed.
func generateLineDiff(oldStr, newStr string) string {
	oldSet := map[string]bool{}
	for _, l := range strings.Split(oldStr, "\n") {
		if t := strings.TrimSpace(l); t != "" {
			oldSet[t] = true
		}
	}
	newSet := map[string]bool{}
	for _, l := range strings.Split(newStr, "\n") {
		if t := strings.TrimSpace(l); t != "" {
			newSet[t] = true
		}
	}
	var b strings.Builder
	for _, l := range strings.Split(oldStr, "\n") {
		if t := strings.TrimSpace(l); t != "" && !newSet[t] {
			b.WriteString("- " + l + "\n")
		}
	}
	for _, l := range strings.Split(newStr, "\n") {
		if t := strings.TrimSpace(l); t != "" && !oldSet[t] {
			b.WriteString("+ " + l + "\n")
		}
	}
	return b.String()
}

// lineDelta counts added/removed non-empty lines between two file versions, using
// the same trimmed set-difference the review panes show, so the done screen's
// "+N / -M" matches the green/red lines the user just approved.
func lineDelta(oldStr, newStr string) (added, removed int) {
	oldSet := map[string]bool{}
	for _, l := range strings.Split(oldStr, "\n") {
		if t := strings.TrimSpace(l); t != "" {
			oldSet[t] = true
		}
	}
	newSet := map[string]bool{}
	for _, l := range strings.Split(newStr, "\n") {
		if t := strings.TrimSpace(l); t != "" {
			newSet[t] = true
		}
	}
	for l := range newSet {
		if !oldSet[l] {
			added++
		}
	}
	for l := range oldSet {
		if !newSet[l] {
			removed++
		}
	}
	return added, removed
}

func (m *organizeModel) writeStats(filesChanged int) {
	stats := organizeStats{
		LastRun:      time.Now().Format("2006-01-02 15:04:05"),
		TokensUsed:   m.proposal.TokensUsed,
		FilesChanged: filesChanged,
	}
	statsPath := filepath.Join(m.memoryPath, ".last_organize.json")
	if data, err := json.Marshal(stats); err == nil {
		_ = os.WriteFile(statsPath, data, 0644)
	}
	m.lastRun = stats.LastRun
	m.lastTokensUsed = stats.TokensUsed
	m.lastFilesChanged = filesChanged
}

func (m *organizeModel) reset() {
	m.mode = orgIdle
	m.proposal = memory.OrganizeProposal{}
	m.changes = nil
	m.decisions = nil
	m.fileCursor = 0
	m.summary = ""
	m.diff = ""
	m.status = ""
	m.errMsg = ""
	m.confirming = false
	m.editor.Blur()
}

func (m *organizeModel) loadCurrentChange() {
	m.resizeReviewPanes()
	if m.fileCursor >= len(m.changes) {
		return
	}
	c := m.changes[m.fileCursor]
	before, after := diffColorize(c.OldContent, c.NewContent)
	m.beforeVP.SetContent(before)
	m.afterVP.SetContent(after)
	m.beforeVP.GotoTop()
	m.afterVP.GotoTop()
}

// diffColorize renders the before/after panes with line-level highlighting: lines
// REMOVED (present in the old content, absent from the new) are red and marked "-"
// in the Before pane; lines ADDED (new only) are green and marked "+" in the After
// pane; unchanged lines are plain with a two-space gutter so columns line up. The
// add/remove test is set-based on trimmed lines (matching the diff the user applies),
// so reordered-but-identical lines are correctly shown as unchanged.
func diffColorize(oldStr, newStr string) (string, string) {
	oldLines := strings.Split(oldStr, "\n")
	newLines := strings.Split(newStr, "\n")

	inNew := make(map[string]bool, len(newLines))
	for _, l := range newLines {
		if t := strings.TrimSpace(l); t != "" {
			inNew[t] = true
		}
	}
	inOld := make(map[string]bool, len(oldLines))
	for _, l := range oldLines {
		if t := strings.TrimSpace(l); t != "" {
			inOld[t] = true
		}
	}

	var before, after strings.Builder
	for i, l := range oldLines {
		t := strings.TrimSpace(l)
		if t != "" && !inNew[t] {
			before.WriteString(orgRemovedStyle.Render("- " + l))
		} else {
			before.WriteString("  " + l)
		}
		if i < len(oldLines)-1 {
			before.WriteString("\n")
		}
	}
	for i, l := range newLines {
		t := strings.TrimSpace(l)
		if t != "" && !inOld[t] {
			after.WriteString(orgAddedStyle.Render("+ " + l))
		} else {
			after.WriteString("  " + l)
		}
		if i < len(newLines)-1 {
			after.WriteString("\n")
		}
	}
	return before.String(), after.String()
}

// reviewChromeRows is the number of rows the review screen spends on its OWN
// chrome (title, summary, decision strip, the blank line, and the two-line action
// bar) — everything in reviewView that is not the before/after panels.
const reviewChromeRows = 7

// appChromeRows estimates the rows the app frame consumes around the Memory Org
// content (full logo banner + tab bar + footer + the blank separator rows), so the
// panes fill the remaining height without being clipped by the app's clamp.
func (m organizeModel) appChromeRows() int {
	bannerH := 7
	if m.width > 0 {
		bannerH = lipgloss.Height(renderBanner(m.width))
	}
	return bannerH + 2 /*tabs*/ + 2 /*footer*/ + 2 /*blank separators*/
}

func (m *organizeModel) resizeReviewPanes() {
	w := m.width
	if w <= 0 {
		w = 100
	}
	h := m.height
	if h <= 0 {
		h = 30
	}
	panelW := (w - 6) / 2
	if panelW < 20 {
		panelW = 20
	}
	// Panels get every row left after the app frame and the review screen's own
	// header/action rows, so the user sees as much file content as the terminal allows.
	// The -4 covers the panel's own border (2) + the in-panel title line (1) + 1 safety
	// row so the app's clamp never trims the content.
	panelH := h - m.appChromeRows() - reviewChromeRows
	if panelH < 6 {
		panelH = 6
	}
	innerW := panelW - 4
	if innerW < 10 {
		innerW = 10
	}
	innerH := panelH - 4
	if innerH < 3 {
		innerH = 3
	}
	m.beforeVP.Width = innerW
	m.beforeVP.Height = innerH
	m.afterVP.Width = innerW
	m.afterVP.Height = innerH
}

// editChromeRows is the number of rows the editing screen spends on its OWN
// chrome (the title line, the blank above the editor, the blank below it, and
// the two-line Save/Discard action bar) — everything in editingView that is not
// the editor itself. Kept in sync with editingView's JoinVertical layout.
const editChromeRows = 5

func (m *organizeModel) resizeEditor() {
	w := m.width - 4
	if w < 30 {
		w = 30
	}
	// Fill the rows left after the app frame and this screen's own chrome so the
	// editor never overflows into the app's "enlarge window" clamp. The extra -1
	// is the same safety row the review panes leave.
	h := m.height - m.appChromeRows() - editChromeRows - 1
	if h < 6 {
		h = 6
	}
	m.editor.SetWidth(w)
	m.editor.SetHeight(h)
}

func (m *organizeModel) resizeCustomInput() {
	w := m.width - 8
	if w < 30 {
		w = 30
	}
	if w > 80 {
		w = 80
	}
	m.customInput.Width = w
}

func (m organizeModel) View() string {
	switch m.mode {
	case orgRunning:
		return m.runningView()
	case orgReview:
		return m.reviewView()
	case orgEditing:
		return m.editingView()
	case orgDone:
		return m.doneView()
	default:
		return m.idleView()
	}
}

// runningView shows a live, never-"stuck"-looking progress screen while the chosen
// model/agent computes the proposal. The agent run is a black box (we only get its
// output at the end), so we surface the provider/model, an animated spinner, an
// elapsed-seconds counter (derived from the 120ms spin tick), and a staged status
// message that advances over time so the user always sees motion.
func (m organizeModel) runningView() string {
	elapsed := m.spin * 120 / 1000 // seconds, from the 120ms spinner tick
	spin := lipgloss.NewStyle().Bold(true).Foreground(ColorSecondary).Render(spinnerFrame(m.spin))

	model := m.runModel
	if model == "" || model == "default model" {
		model = "default"
	}

	// A checklist of the organize pipeline. Gathering files and launching the
	// provider genuinely complete before we begin waiting (both happen synchronously
	// at the start of the run), so they show a green check; the model round-trip is
	// the active step; parsing + building the review come after the result returns.
	type step struct {
		state int // 0 done, 1 active, 2 pending
		label string
	}
	steps := []step{
		{0, "Gathered memory files"},
		{0, fmt.Sprintf("Launched %s (%s)", m.runProvider, model)},
		{1, fmt.Sprintf("Waiting for the model to reorganize your vault…  (%ds)", elapsed)},
		{2, "Parse proposed changes"},
		{2, "Build before/after review"},
	}

	var b strings.Builder
	b.WriteString(StyleTitle.Render("Memory Organization — Organizing"))
	b.WriteString("\n\n")
	for _, s := range steps {
		switch s.state {
		case 0:
			b.WriteString("  " + orgGoodStyle.Render("✓") + "  " + orgDimStyle.Render(s.label) + "\n")
		case 1:
			b.WriteString("  " + spin + "  " + orgBoldStyle.Render(s.label) + "\n")
		default:
			b.WriteString("  " + orgDimStyle.Render("○") + "  " + orgDimStyle.Render(s.label) + "\n")
		}
	}
	if elapsed >= 25 {
		b.WriteString("\n" + orgDimStyle.Render("Large vaults and CLI agents can take a minute — still working…"))
	}
	b.WriteString("\n\n")
	b.WriteString(orgDimStyle.Render("Nothing is written yet — you'll review every change before anything is saved."))
	b.WriteString("\n" + orgKeyChip("esc", "cancel this run", ColorDanger))
	return b.String()
}

// renderSelectPanel draws a bordered selection list; the focused panel gets an
// accent border + a ◂ marker so it's obvious which side the arrows control.
func renderSelectPanel(title string, focused bool, lines []string, width int) string {
	bc := ColorDim
	head := orgDimStyle.Render(title)
	if focused {
		bc = ColorPrimary
		head = orgPanelTitleStyle.Render(title + " ◂")
	}
	body := head + "\n" + strings.Join(lines, "\n")
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(bc).Padding(0, 1).Width(width).Render(body)
}

// orgSelRow renders one selectable row with a cursor; the selected row is bold and,
// when its list is focused, accent-colored.
func orgSelRow(label string, selected, focused bool) string {
	if !selected {
		return "  " + label
	}
	if focused {
		return lipgloss.NewStyle().Foreground(ColorPrimary).Bold(true).Render("▸ " + label)
	}
	return orgBoldStyle.Render("▸ " + label)
}

// providerHint describes what the selected provider needs, flagging a missing key.
func providerHint(p orgProvider) (string, bool) {
	if p.kind == "agent" {
		name := strings.TrimSuffix(p.label, " (Recommended)")
		return "Uses your " + name + " subscription — no API key needed.", false
	}
	switch p.id {
	case "ollama":
		return "Local endpoint — no API key needed.", false
	case "openai":
		if os.Getenv("OPENAI_API_KEY") == "" {
			return "Needs OPENAI_API_KEY (not set) — export it and relaunch.", true
		}
		return "Uses your OPENAI_API_KEY.", false
	case "gemini":
		if os.Getenv("GEMINI_API_KEY") == "" {
			return "Needs GEMINI_API_KEY (not set) — export it and relaunch.", true
		}
		return "Uses your GEMINI_API_KEY.", false
	case "custom":
		return "Your OpenAI-compatible endpoint — set the URL, models auto-fetch.", false
	}
	return "", false
}

// confirmView renders the [y]/[n] popup shown after the user picks a model.
func (m organizeModel) confirmView() string {
	model := strings.TrimSpace(m.picked)
	if model == "" {
		model = "default"
	}
	var inner strings.Builder
	inner.WriteString(orgPanelTitleStyle.Render("Confirm run") + "\n\n")
	inner.WriteString(orgBoldStyle.Render("Provider  ") + m.currentProvider().label + "\n")
	inner.WriteString(orgBoldStyle.Render("Model     ") + model + "\n")
	inner.WriteString(orgBoldStyle.Render("Scope     ") + orgDimStyle.Render("user-memory files only — agents.md & setup skipped") + "\n")
	if m.estFiles > 0 {
		inner.WriteString(orgBoldStyle.Render("Payload   ") + orgDimStyle.Render(fmt.Sprintf("~%d tokens · %d file(s)", m.estTokens, m.estFiles)) + "\n")
	}
	inner.WriteString("\n")
	inner.WriteString(orgGoodStyle.Render("[y] Yes, analyze") + "     " + orgDimStyle.Render("[n] / esc  Cancel"))
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(ColorPrimary).Padding(1, 3).Render(inner.String())

	var b strings.Builder
	b.WriteString(StyleTitle.Render("Memory Organization"))
	b.WriteString("\n\n")
	b.WriteString(box)
	b.WriteString("\n\n")
	b.WriteString(orgDimStyle.Render("Nothing is written until you approve each change in the review."))
	return b.String()
}

func (m organizeModel) idleView() string {
	if m.confirming {
		return m.confirmView()
	}
	cp := m.currentProvider()

	panelW := (m.width - 8) / 2
	if panelW > 42 {
		panelW = 42
	}
	if panelW < 22 {
		panelW = 22
	}

	// Provider column.
	var provLines []string
	for i, p := range m.providers {
		if p.kind == "separator" {
			provLines = append(provLines, orgDimStyle.Render(p.label))
			continue
		}
		provLines = append(provLines, orgSelRow(p.label, i == m.provIdx, m.focus == focusProvider))
	}

	// Model column.
	var modelLines []string
	switch {
	case m.fetching:
		modelLines = []string{orgWarnStyle.Render("⟳ querying models…")}
	case len(m.models) > 0:
		for i, mdl := range m.models {
			modelLines = append(modelLines, orgSelRow(mdl, i == m.modelIdx, m.focus == focusModel))
		}
	default:
		modelLines = []string{"  " + orgDimStyle.Render(firstModelLabel(cp, nil)+"  (default)")}
		if cp.kind == "api" {
			modelLines = append(modelLines, "", orgDimStyle.Render("Tab → here to fetch"))
		}
	}

	panels := lipgloss.JoinHorizontal(lipgloss.Top,
		renderSelectPanel("Provider", m.focus == focusProvider, provLines, panelW),
		"  ",
		renderSelectPanel("Model", m.focus == focusModel, modelLines, panelW),
	)

	var b strings.Builder
	b.WriteString(StyleTitle.Render("Memory Organization"))
	b.WriteString("\n")
	b.WriteString(orgDimStyle.Render("Pick a provider + model, then Enter. Nothing is saved until you approve."))
	if m.estFiles > 0 {
		b.WriteString("\n" + orgDimStyle.Render(fmt.Sprintf("Will send ~%s tokens across %s (user-memory files only).",
			orgBoldStyle.Render(fmt.Sprintf("%d", m.estTokens)),
			orgBoldStyle.Render(fmt.Sprintf("%d file(s)", m.estFiles)))))
	}
	b.WriteString("\n\n")
	b.WriteString(panels)
	b.WriteString("\n")

	// Custom endpoint URL field.
	if cp.id == "custom" {
		b.WriteString("\n" + orgBoldStyle.Render("Endpoint URL") + "\n")
		if m.urlEditing {
			b.WriteString("  " + m.customInput.View() + "\n")
		} else {
			b.WriteString("  " + orgDimStyle.Render(m.customURL) + "   " + StyleFooter.Render("(e edit · Enter saves & fetches models)") + "\n")
		}
	}

	// Provider hint (what it needs).
	if hint, isErr := providerHint(cp); hint != "" {
		style := orgDimStyle
		if isErr {
			style = orgBadStyle
		}
		b.WriteString("\n" + style.Render(hint) + "\n")
	}

	if m.fetchErr != "" {
		b.WriteString(orgBadStyle.Render("Model fetch: "+m.fetchErr) + "\n")
	}
	if m.errMsg != "" {
		b.WriteString(orgBadStyle.Render(m.errMsg) + "\n")
	}
	if m.status != "" {
		b.WriteString(orgGoodStyle.Render(m.status) + "\n")
	}
	if m.lastRun != "" {
		filesPart := ""
		if m.lastFilesChanged > 0 {
			filesPart = fmt.Sprintf(" · %s", orgGoodStyle.Render(fmt.Sprintf("%d file(s) updated", m.lastFilesChanged)))
		}
		b.WriteString(orgDimStyle.Render(fmt.Sprintf("Last run: %s · %d tokens", m.lastRun, m.lastTokensUsed)) +
			filesPart + orgDimStyle.Render(" · history in Audit Trail (0)") + "\n")
	}

	b.WriteString("\n" + StyleFooter.Render("↑↓ choose · Enter: Provider→Model→confirm · Tab switch · e edit URL · f refetch · 1-9/0 tabs"))
	return b.String()
}

func (m organizeModel) reviewView() string {
	if len(m.changes) == 0 || m.fileCursor >= len(m.changes) {
		return m.idleView()
	}
	c := m.changes[m.fileCursor]
	approved, rejected, pending := m.decisionCounts()

	// Summary line: which file, total CHANGED files, and the running tally.
	decTag := m.decisionTag(m.fileCursor)
	summary := fmt.Sprintf("File %d of %d changed   %s   %s",
		m.fileCursor+1, len(m.changes), decTag,
		orgDimStyle.Render(fmt.Sprintf("✓ %d approved · ✗ %d rejected · ○ %d pending", approved, rejected, pending)))
	fileLine := orgBoldStyle.Render(c.Name) + "  " + orgDimStyle.Render("("+c.Scope+")")

	strip := orgDimStyle.Render("Files: ") + m.decisionStrip()

	panelW := (m.width - 6) / 2
	if panelW < 20 {
		panelW = 20
	}
	panelH := m.beforeVP.Height + 2
	before := orgPanelStyle.Width(panelW).Height(panelH).Render(m.paneTitle("Before", m.beforeVP) + "\n" + m.beforeVP.View())
	after := orgPanelStyle.Width(panelW).Height(panelH).Render(m.paneTitle("After", m.afterVP) + "\n" + m.afterVP.View())
	panels := lipgloss.JoinHorizontal(lipgloss.Top, before, "  ", after)

	// Two-line, color-coded action bar so the controls are always obvious.
	actions := strings.Join([]string{
		orgKeyChip("a", "Approve", ColorSuccess),
		orgKeyChip("r", "Reject", ColorDanger),
		orgKeyChip("e", "Edit", ColorSecondary),
		orgKeyChip("A", "Approve all", ColorSuccess),
	}, "   ")
	nav := strings.Join([]string{
		orgKeyChip("←/→", "prev/next file", ColorPrimary),
		orgKeyChip("↑/↓", "scroll", ColorPrimary),
		orgKeyChip("enter", "submit approved", ColorPrimary),
		orgKeyChip("esc", "cancel", ColorDim),
	}, "   ")
	hint := orgDimStyle.Render("Tip: mouse wheel scrolls · click a file dot to jump · click Approve/Reject below")

	parts := []string{
		StyleTitle.Render("Memory Organization — Review"),
		summary,
		fileLine,
		strip,
		panels,
		actions,
		nav,
		hint,
	}
	if m.status != "" {
		parts = append([]string{parts[0]}, append([]string{orgWarnStyle.Render(m.status)}, parts[1:]...)...)
	}
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

// paneTitle renders a panel heading with a scroll-position hint so the user knows
// there is more content above/below than the viewport currently shows.
func (m organizeModel) paneTitle(name string, vp viewport.Model) string {
	title := orgPanelTitleStyle.Render(name)
	var marks []string
	if !vp.AtTop() {
		marks = append(marks, "▲ more above")
	}
	if !vp.AtBottom() {
		marks = append(marks, "▼ more below")
	}
	if len(marks) > 0 {
		title += "  " + orgDimStyle.Render(strings.Join(marks, " · "))
	}
	return title
}

// decisionTag renders the current file's decision as a colored, bracketed label.
func (m organizeModel) decisionTag(i int) string {
	switch m.decisionLabel(i) {
	case "APPROVED":
		return orgGoodStyle.Render("[APPROVED]")
	case "REJECTED":
		return orgBadStyle.Render("[REJECTED]")
	default:
		return orgWarnStyle.Render("[PENDING]")
	}
}

// orgKeyChip renders "[key] label" with the key highlighted in the given color.
func orgKeyChip(key, label string, c lipgloss.TerminalColor) string {
	k := lipgloss.NewStyle().Bold(true).Foreground(c).Render("[" + key + "]")
	return k + " " + label
}

func (m organizeModel) editingView() string {
	name := ""
	if m.fileCursor < len(m.changes) {
		name = m.changes[m.fileCursor].Name
	}
	header := StyleTitle.Render("Editing " + name)
	// A prominent, color-coded action bar so Save/Discard are always obvious —
	// mirrors the review screen's bar instead of hiding the keys in the title.
	actions := strings.Join([]string{
		orgKeyChip("ctrl+s", "Save & approve", ColorSuccess),
		orgKeyChip("esc", "Discard changes", ColorDanger),
	}, "    ")
	hint := orgDimStyle.Render("Saving keeps your edits and marks this file approved.")
	return lipgloss.JoinVertical(lipgloss.Left, header, "", m.editor.View(), "", actions, hint)
}

func (m organizeModel) doneView() string {
	body := m.summary
	body += "\n\n" + StyleFooter.Render("Enter / esc — back to providers   ·   press 0 to open the Audit Trail")
	return lipgloss.JoinVertical(lipgloss.Left, StyleTitle.Render("Memory Organization — Done"), "", body)
}

func (m organizeModel) decisionLabel(i int) string {
	if i >= len(m.decisions) {
		return "PENDING"
	}
	switch m.decisions[i] {
	case decApproved:
		return "APPROVED"
	case decRejected:
		return "REJECTED"
	default:
		return "PENDING"
	}
}

// hasRealChange reports whether a proposed change differs from disk in a way the
// user would actually care about — ignoring trailing whitespace on each line and
// trailing blank lines, so a file the model returned byte-for-byte the same except
// for a stray newline is NOT shown as a change.
func hasRealChange(c memory.ProposedChange) bool {
	return normalizeForCompare(c.OldContent) != normalizeForCompare(c.NewContent)
}

func normalizeForCompare(s string) string {
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " \t")
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n ")
}

// decisionCounts tallies approved/rejected/pending across the proposal.
func (m organizeModel) decisionCounts() (approved, rejected, pending int) {
	for _, d := range m.decisions {
		switch d {
		case decApproved:
			approved++
		case decRejected:
			rejected++
		default:
			pending++
		}
	}
	return
}

func (m organizeModel) decisionStrip() string {
	var parts []string
	for i, d := range m.decisions {
		marker := orgDimStyle.Render("o")
		switch d {
		case decApproved:
			marker = orgGoodStyle.Render("✓")
		case decRejected:
			marker = orgBadStyle.Render("x")
		}
		if i == m.fileCursor {
			marker = orgBoldStyle.Render("[" + marker + "]")
		}
		parts = append(parts, marker)
	}
	return strings.Join(parts, " ")
}
