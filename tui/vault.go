package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/clipboard"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/embed"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/memory"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/vaultcrypt"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ─────────────────────────────────────────────────────────────────
//  Settings → Vault sub-tab: encryption-at-rest + semantic index admin.
//
//  Mirrors customizationsModel's shape (confirm → applying → result), and
//  reuses the exact same core the CLI does: internal/vaultcrypt.Keystore +
//  Store.EncryptFile/DecryptFile/EncryptedFileCount (cmd/encrypt.go's own
//  call path) for encryption, Store.RebuildIndex/IndexStatus (cmd/index.go's
//  own call path) for the index. No cmd package logic is reimplemented here —
//  every keychain/disk call runs inside a tea.Cmd, never in Update() or View().
// ─────────────────────────────────────────────────────────────────

// copyVaultKey is a package-level var (not a direct clipboard.Copy call) so
// tests can stub it out — mirrors ssh.go's copyInvite for the same reason.
var copyVaultKey = clipboard.Copy

// vault sub-modes (mode == "" is the idle list/status view).
const (
	vaultModeInitChoice   = "initChoice"   // pick keypair vs passphrase
	vaultModePass1        = "pass1"        // passphrase entry, first pass
	vaultModePass2        = "pass2"        // passphrase entry, confirm pass
	vaultModeConfirmKey   = "confirmKey"   // keypair init: hard warning, y/n
	vaultModeConfirmPass  = "confirmPass"  // passphrase init: hard no-recovery warning, y/n
	vaultModeBackupKey    = "backupKey"    // keypair backup key shown ONCE
	vaultModeConfirmDecry = "confirmDecry" // decrypt-file confirm gate
)

const minVaultPassphraseLen = 8

type vaultModel struct {
	memoryPath string
	width      int
	height     int

	// status, populated by refreshCmd on Vault sub-tab entry.
	loaded       bool
	keyExists    bool
	keyMode      string // "keypair" | "passphrase" (meaningless until keyExists)
	keySource    string // "env" | "keychain" | "file"
	keySourceErr string // set when the key exists but isn't reachable right now
	encCount     int
	totalFiles   int
	embedEnabled bool
	idx          memory.IndexStatus

	files   []string // vault file names (source files only — unified_memory.md excluded)
	fileEnc map[string]bool
	cursor  int

	mode          string
	passBuf1      string
	passBuf2      string
	passErr       string
	decryptTarget string
	backupKey     string
	status        string
	statusErr     bool

	busy      bool // a tea.Cmd (keychain/disk/embedder call) is in flight
	busyLabel string
	spin      int
}

func newVaultModel(memPath string) vaultModel {
	return vaultModel{memoryPath: memPath}
}

// vaultKeystore builds the same Keystore cmd/encrypt.go does: rooted at the
// directory ABOVE the memory vault (keys live outside it so a git-synced
// vault never ships the key next to its own ciphertext).
func vaultKeystore(memPath string) *vaultcrypt.Keystore {
	return vaultcrypt.NewKeystore(filepath.Dir(memPath))
}

type vaultRefreshMsg struct {
	keyExists    bool
	keyMode      string
	keySource    string
	keySourceErr string
	encCount     int
	totalFiles   int
	embedEnabled bool
	idx          memory.IndexStatus
	files        []string
	fileEnc      map[string]bool
}

type vaultActionMsg struct {
	kind      string // "init" | "encrypt" | "decrypt" | "rebuild"
	ok        bool
	err       error
	backupKey string // set on a successful keypair init
	chunks    int    // set on a successful rebuild
}

type vaultSpinTickMsg struct{}

func vaultSpinTick() tea.Cmd {
	return tea.Tick(120*time.Millisecond, func(time.Time) tea.Msg { return vaultSpinTickMsg{} })
}

// refreshCmd reads everything the panel shows: key reachability (may hit the
// macOS keychain, up to vaultcrypt's own 10s timeout), the vault file list +
// each file's encrypted-at-rest header (cheap, no key needed — same sniff
// cmd/encrypt.go's encryptedFileNames and Store.EncryptedFileCount use), and
// the index status + embedder availability. All of it is disk/keychain IO, so
// it only ever runs inside this tea.Cmd — never inline in Update or View.
func (m vaultModel) refreshCmd() tea.Cmd {
	memPath := m.memoryPath
	return func() tea.Msg {
		store := memory.NewStore(memPath)
		ks := vaultKeystore(memPath)

		keyExists := ks.Exists()
		keyMode := ks.Mode()
		var keySource, keySourceErr string
		if src, err := ks.Source(); err == nil {
			keySource = src
		} else {
			keySourceErr = err.Error()
		}

		infos, _ := store.List()
		var names []string
		fileEnc := map[string]bool{}
		for _, f := range infos {
			if f.IsDir || f.Name == "unified_memory.md" {
				continue // compiled artifact, not an encrypt/decrypt target
			}
			names = append(names, f.Name)
			if raw, err := os.ReadFile(f.Path); err == nil {
				fileEnc[f.Name] = vaultcrypt.IsEncrypted(raw)
			}
		}
		sort.Strings(names)

		idx, _ := store.IndexStatus()

		return vaultRefreshMsg{
			keyExists: keyExists, keyMode: keyMode,
			keySource: keySource, keySourceErr: keySourceErr,
			encCount: store.EncryptedFileCount(), totalFiles: len(names),
			embedEnabled: embed.New().Enabled(), idx: idx,
			files: names, fileEnc: fileEnc,
		}
	}
}

func generateKeypairCmd(memPath string) tea.Cmd {
	return func() tea.Msg {
		ks := vaultKeystore(memPath)
		if err := ks.Generate(); err != nil {
			return vaultActionMsg{kind: "init", ok: false, err: err}
		}
		key, err := ks.ExportKey()
		if err != nil {
			return vaultActionMsg{kind: "init", ok: false, err: err}
		}
		return vaultActionMsg{kind: "init", ok: true, backupKey: key}
	}
}

func generatePassphraseCmd(memPath, pass string) tea.Cmd {
	return func() tea.Msg {
		ks := vaultKeystore(memPath)
		if err := ks.GeneratePassphrase(pass); err != nil {
			return vaultActionMsg{kind: "init", ok: false, err: err}
		}
		return vaultActionMsg{kind: "init", ok: true}
	}
}

func vaultEncryptFileCmd(memPath, name string) tea.Cmd {
	return func() tea.Msg {
		err := memory.NewStore(memPath).EncryptFile(name)
		return vaultActionMsg{kind: "encrypt", ok: err == nil, err: err}
	}
}

func vaultDecryptFileCmd(memPath, name string) tea.Cmd {
	return func() tea.Msg {
		err := memory.NewStore(memPath).DecryptFile(name)
		return vaultActionMsg{kind: "decrypt", ok: err == nil, err: err}
	}
}

// rebuildIndexTimeout bounds a full vault re-embed so a stalled/unreachable
// embedder can't hang the command forever — RebuildIndex itself has no
// internal deadline beyond the embedder's own per-request timeout.
const rebuildIndexTimeout = 5 * time.Minute

func vaultRebuildIndexCmd(memPath string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), rebuildIndexTimeout)
		defer cancel()
		n, err := memory.NewStore(memPath).RebuildIndex(ctx, embed.New())
		return vaultActionMsg{kind: "rebuild", ok: err == nil, err: err, chunks: n}
	}
}

func (m vaultModel) capturesInput() bool {
	return m.busy || m.mode != ""
}

func (m vaultModel) cursorFile() (string, bool) {
	if m.cursor < 0 || m.cursor >= len(m.files) {
		return "", false
	}
	return m.files[m.cursor], true
}

func (m vaultModel) Update(msg tea.Msg) (vaultModel, tea.Cmd) {
	switch msg := msg.(type) {
	case vaultRefreshMsg:
		m.loaded = true
		m.keyExists, m.keyMode = msg.keyExists, msg.keyMode
		m.keySource, m.keySourceErr = msg.keySource, msg.keySourceErr
		m.encCount, m.totalFiles = msg.encCount, msg.totalFiles
		m.embedEnabled, m.idx = msg.embedEnabled, msg.idx
		m.files, m.fileEnc = msg.files, msg.fileEnc
		if m.cursor >= len(m.files) {
			m.cursor = 0
		}
		return m, nil

	case vaultActionMsg:
		m.busy = false
		switch msg.kind {
		case "init":
			if !msg.ok {
				m.mode = ""
				m.status, m.statusErr = "✗ "+msg.err.Error(), true
				return m, nil
			}
			if msg.backupKey != "" {
				m.mode = vaultModeBackupKey
				m.backupKey = msg.backupKey
				return m, nil
			}
			m.mode = ""
			m.status, m.statusErr = "🔐 Vault password set.", false
			return m, m.refreshCmd()
		case "encrypt":
			m.mode = ""
			if !msg.ok {
				m.status, m.statusErr = "✗ "+msg.err.Error(), true
			} else {
				m.status, m.statusErr = "✅ encrypted at rest.", false
			}
			return m, m.refreshCmd()
		case "decrypt":
			m.mode, m.decryptTarget = "", ""
			if !msg.ok {
				m.status, m.statusErr = "✗ "+msg.err.Error(), true
			} else {
				m.status, m.statusErr = "🔓 now plaintext.", false
			}
			return m, m.refreshCmd()
		case "rebuild":
			if !msg.ok {
				m.status, m.statusErr = "✗ "+msg.err.Error(), true
			} else {
				m.status, m.statusErr = fmt.Sprintf("✓ index rebuilt — %d chunk(s)", msg.chunks), false
			}
			return m, m.refreshCmd()
		}
		return m, nil

	case vaultSpinTickMsg:
		if m.busy {
			m.spin++
			return m, vaultSpinTick()
		}
		return m, nil
	}
	return m, nil
}

func (m vaultModel) handleKey(msg tea.KeyMsg) (vaultModel, tea.Cmd) {
	if m.busy {
		return m, nil // input is frozen while the write/keychain call is in flight
	}

	switch m.mode {
	case vaultModeInitChoice:
		switch msg.String() {
		case "1":
			m.mode = vaultModeConfirmKey
		case "2":
			m.mode, m.passBuf1, m.passBuf2, m.passErr = vaultModePass1, "", "", ""
		case "esc":
			m.mode = ""
		}
		return m, nil

	case vaultModeConfirmKey:
		switch msg.String() {
		case "y", "Y", "enter":
			m.busy, m.busyLabel, m.spin = true, "Generating vault key", 0
			return m, tea.Batch(generateKeypairCmd(m.memoryPath), vaultSpinTick())
		case "n", "N", "esc":
			m.mode = vaultModeInitChoice
		}
		return m, nil

	case vaultModePass1:
		switch msg.Type {
		case tea.KeyEsc:
			m.mode, m.passBuf1, m.passBuf2, m.passErr = "", "", "", ""
		case tea.KeyEnter:
			if len(m.passBuf1) < minVaultPassphraseLen {
				m.passErr = fmt.Sprintf("must be at least %d characters — try again", minVaultPassphraseLen)
				return m, nil
			}
			m.passErr = ""
			m.mode = vaultModePass2
		case tea.KeyBackspace, tea.KeyCtrlH:
			if r := []rune(m.passBuf1); len(r) > 0 {
				m.passBuf1 = string(r[:len(r)-1])
			}
		case tea.KeySpace:
			m.passBuf1 += " "
		case tea.KeyRunes:
			m.passBuf1 += string(msg.Runes)
		}
		return m, nil

	case vaultModePass2:
		switch msg.Type {
		case tea.KeyEsc:
			m.mode, m.passBuf1, m.passBuf2, m.passErr = "", "", "", ""
		case tea.KeyEnter:
			if m.passBuf1 != m.passBuf2 {
				m.passErr = "passwords did not match — try again"
				m.mode, m.passBuf1, m.passBuf2 = vaultModePass1, "", ""
				return m, nil
			}
			m.passErr = ""
			m.mode = vaultModeConfirmPass
		case tea.KeyBackspace, tea.KeyCtrlH:
			if r := []rune(m.passBuf2); len(r) > 0 {
				m.passBuf2 = string(r[:len(r)-1])
			}
		case tea.KeySpace:
			m.passBuf2 += " "
		case tea.KeyRunes:
			m.passBuf2 += string(msg.Runes)
		}
		return m, nil

	case vaultModeConfirmPass:
		switch msg.String() {
		case "y", "Y", "enter":
			pass := m.passBuf1
			m.passBuf1, m.passBuf2 = "", ""
			m.busy, m.busyLabel, m.spin = true, "Setting vault password", 0
			return m, tea.Batch(generatePassphraseCmd(m.memoryPath, pass), vaultSpinTick())
		case "n", "N", "esc":
			m.mode, m.passBuf1, m.passBuf2 = "", "", ""
		}
		return m, nil

	case vaultModeBackupKey:
		switch msg.String() {
		case "c":
			if err := copyVaultKey(m.backupKey); err != nil {
				m.status, m.statusErr = "(clipboard unavailable — copy the key above manually)", true
			} else {
				m.status, m.statusErr = "key copied ✓", false
			}
			return m, nil
		default:
			m.mode, m.backupKey = "", ""
			return m, m.refreshCmd()
		}

	case vaultModeConfirmDecry:
		switch msg.String() {
		case "y", "Y", "enter":
			target := m.decryptTarget
			m.mode = ""
			m.busy, m.busyLabel, m.spin = true, "Decrypting "+target, 0
			return m, tea.Batch(vaultDecryptFileCmd(m.memoryPath, target), vaultSpinTick())
		case "n", "N", "esc":
			m.mode, m.decryptTarget = "", ""
		}
		return m, nil
	}

	// idle: status view + file list.
	switch msg.String() {
	case "j", "down":
		if m.cursor < len(m.files)-1 {
			m.cursor++
		}
	case "k", "up":
		if m.cursor > 0 {
			m.cursor--
		}
	case "i":
		if !m.keyExists {
			m.mode, m.status = vaultModeInitChoice, ""
		}
	case "e":
		if f, ok := m.cursorFile(); ok && m.keyExists && !m.fileEnc[f] {
			m.status = ""
			m.busy, m.busyLabel, m.spin = true, "Encrypting "+f, 0
			return m, tea.Batch(vaultEncryptFileCmd(m.memoryPath, f), vaultSpinTick())
		}
	case "d":
		if f, ok := m.cursorFile(); ok && m.fileEnc[f] {
			m.mode, m.decryptTarget, m.status = vaultModeConfirmDecry, f, ""
		}
	case "r":
		if m.embedEnabled {
			m.status = ""
			m.busy, m.busyLabel, m.spin = true, "Rebuilding index", 0
			return m, tea.Batch(vaultRebuildIndexCmd(m.memoryPath), vaultSpinTick())
		}
	}
	return m, nil
}

// panel renders the Vault sub-tab body (the caller adds title + sub-tab bar).
func (m vaultModel) panel() string {
	cyan := lipgloss.NewStyle().Bold(true).Foreground(ColorPrimary)
	dim := StyleSubtitle
	accent := lipgloss.NewStyle().Bold(true).Foreground(ColorPrimary)
	bold := lipgloss.NewStyle().Bold(true)
	green := lipgloss.NewStyle().Foreground(ColorSuccess)
	warn := lipgloss.NewStyle().Foreground(ColorWarning)
	danger := lipgloss.NewStyle().Foreground(ColorDanger)

	w := m.width
	if w <= 0 {
		w = 80
	}
	padW := w - 10
	if padW < 44 {
		padW = 44
	}
	if padW > 70 {
		padW = 70
	}
	box := func(border lipgloss.Color, lines []string) string {
		var padded []string
		for _, l := range lines {
			padded = append(padded, padLine(l, padW))
		}
		return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(border).
			Padding(1, 2).Render(strings.Join(padded, "\n"))
	}

	if !m.loaded {
		return cyan.Render("Vault") + "\n\n" + dim.Render("Loading…")
	}

	// ── Modal-style overlays (mirror customizationsModel's confirm/applying) ──
	if m.busy {
		spin := lipgloss.NewStyle().Bold(true).Foreground(ColorAccent).Render(spinnerFrame(m.spin))
		return box(ColorWarning, []string{spin + "  " + warn.Render(m.busyLabel+"…")})
	}
	switch m.mode {
	case vaultModeInitChoice:
		return box(ColorPrimary, []string{
			bold.Render("Initialize vault encryption"),
			"",
			accent.Render("[1]") + " keypair " + dim.Render("— a generated key; back it up once, shown here"),
			accent.Render("[2]") + " passphrase " + dim.Render("— a password you choose; no backup key to lose"),
			"",
			dim.Render("esc cancel"),
		})
	case vaultModeConfirmKey:
		return box(ColorWarning, []string{
			warn.Render("⚠ SAVE THE KEY — shown only once."),
			dim.Render("Lose it and every file encrypted with it becomes permanently unreadable."),
			"",
			green.Render("[y] generate") + dim.Render("   [n]/esc cancel"),
		})
	case vaultModePass1, vaultModePass2:
		buf := m.passBuf1
		title := "New vault password:"
		if m.mode == vaultModePass2 {
			buf = m.passBuf2
			title = "Confirm vault password:"
		}
		lines := []string{
			bold.Render(title),
			"  " + strings.Repeat("•", len([]rune(buf))) + accent.Render("▌"),
			"",
			dim.Render("enter next · esc cancel"),
		}
		if m.passErr != "" {
			lines = append(lines, danger.Render(m.passErr))
		}
		return box(ColorPrimary, lines)
	case vaultModeConfirmPass:
		return box(ColorWarning, []string{
			warn.Render("⚠ THERE IS NO RECOVERY KEY IN THIS MODE."),
			dim.Render("Your password IS the key — forget it and the vault is gone, permanently."),
			"",
			green.Render("[y] set password") + dim.Render("   [n]/esc cancel"),
		})
	case vaultModeBackupKey:
		return box(ColorWarning, []string{
			warn.Render("🔐 Vault key generated — SAVE IT NOW, shown only this once:"),
			"",
			"  " + m.backupKey,
			"",
			accent.Render("[c]") + dim.Render(" copy to clipboard   ") + accent.Render("[any key]") + dim.Render(" dismiss"),
		})
	case vaultModeConfirmDecry:
		return box(ColorWarning, []string{
			warn.Render(fmt.Sprintf("Decrypt %q?  It will be stored as plaintext from now on.", m.decryptTarget)),
			"",
			green.Render("[y] decrypt") + dim.Render("   [n]/esc cancel"),
		})
	}

	// ── Idle: status + file list ──
	var lines []string
	lines = append(lines, bold.Render("Vault Encryption"))
	switch {
	case !m.keyExists:
		lines = append(lines, dim.Render("Not initialized — press ")+accent.Render("[i]")+dim.Render(" to generate a key or set a password."))
	case m.keySourceErr != "":
		lines = append(lines, danger.Render("⚠ key unreachable: "+m.keySourceErr))
	default:
		lines = append(lines, dim.Render("mode: ")+accent.Render(m.keyMode)+dim.Render("   source: ")+accent.Render(m.keySource))
	}
	lines = append(lines, dim.Render(fmt.Sprintf("%d of %d file(s) encrypted at rest", m.encCount, m.totalFiles)))
	lines = append(lines, "")

	if len(m.files) == 0 {
		lines = append(lines, dim.Render("No memory files yet."))
	}
	for i, f := range m.files {
		cursor := "  "
		if i == m.cursor {
			cursor = accent.Render("▸ ")
		}
		badge := dim.Render("○ plaintext")
		if m.fileEnc[f] {
			badge = green.Render("● encrypted")
		}
		row := fmt.Sprintf("%s%-28s %s", cursor, f, badge)
		if i == m.cursor {
			row = bold.Render(row)
		}
		lines = append(lines, row)
	}
	lines = append(lines, "")
	switch {
	case !m.keyExists:
		lines = append(lines, dim.Render("i init"))
	default:
		lines = append(lines, dim.Render("↑/↓ select · e encrypt selected · d decrypt selected (confirm)"))
	}

	lines = append(lines, "")
	lines = append(lines, bold.Render("Semantic Index"))
	if m.idx.Built {
		lines = append(lines, dim.Render(fmt.Sprintf("provider: %s   model: %s   chunks: %d", m.idx.Provider, m.idx.Model, m.idx.Chunks)))
	} else {
		lines = append(lines, dim.Render("not built yet"))
	}
	if m.embedEnabled {
		lines = append(lines, dim.Render("[r] rebuild"))
	} else {
		lines = append(lines, dim.Render("embedder unavailable — no local/allowed endpoint to rebuild with"))
	}

	if m.status != "" {
		style := green
		if m.statusErr {
			style = danger
		}
		lines = append(lines, "", style.Render(m.status))
	}

	var padded []string
	for _, l := range lines {
		padded = append(padded, padLine(l, padW))
	}
	panel := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(ColorPrimary).
		Padding(1, 2).Render(strings.Join(padded, "\n"))
	return cyan.Render("Vault") + "\n\n" + panel
}
