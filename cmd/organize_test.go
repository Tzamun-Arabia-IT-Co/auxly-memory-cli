package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/memory"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/pending"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/vaultcrypt"
)

func TestResolveHeadlessAgent_EmptyMeansDirectLLM(t *testing.T) {
	name, path, err := resolveHeadlessAgent("")
	if err != nil || name != "" || path != "" {
		t.Fatalf("resolveHeadlessAgent(\"\") = (%q, %q, %v), want empty/no error", name, path, err)
	}
}

func TestResolveHeadlessAgent_UnknownRefuses(t *testing.T) {
	_, _, err := resolveHeadlessAgent("definitely-not-an-installed-agent-xyz")
	if err == nil {
		t.Fatal("resolveHeadlessAgent should refuse a name matching no installed CLI agent")
	}
	if !strings.Contains(err.Error(), "--agent") {
		t.Fatalf("error = %v, want it to mention --agent", err)
	}
}

// TestRunOrganizeDecryptTemporarily_NonTTYWithoutYesRefuses proves
// --decrypt-temporarily never blocks forever on a stdin read it can't get:
// without --yes, in the non-interactive environment `go test` runs under, it
// must refuse up front and leave the vault file untouched — never decrypt
// then hang waiting for a confirmation that will never arrive.
func TestRunOrganizeDecryptTemporarily_NonTTYWithoutYesRefuses(t *testing.T) {
	if isStdinTTY() {
		t.Skip("stdin is a terminal in this environment — the non-TTY refusal path isn't reachable")
	}
	organizeAssumeYes = false
	t.Cleanup(func() { organizeAssumeYes = false })

	memPath := t.TempDir()
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AUXLY_VAULT_KEY", identity.String())
	store := memory.NewStore(memPath)
	if err := store.Write("personal.md", "- secret\n"); err != nil {
		t.Fatal(err)
	}
	if err := store.EncryptFile("personal.md"); err != nil {
		t.Fatal(err)
	}

	err = runOrganizeDecryptTemporarily(store, "Claude Code / CLI", "/bin/echo", []string{"personal.md"})
	if err == nil {
		t.Fatal("expected a refusal without --yes on non-interactive stdin")
	}
	if !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("error = %v, want it to mention --yes", err)
	}

	raw, err := os.ReadFile(filepath.Join(memPath, "personal.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !vaultcrypt.IsEncrypted(raw) {
		t.Fatal("personal.md was decrypted despite the refusal — nothing should have been touched")
	}
}

// TestRunOrganizeDecryptTemporarily_YesFlagRunsAndRestores proves --yes skips
// the prompt, the CLI-agent stub runs against decrypted content, and the file
// is re-encrypted afterward via the defer.
func TestRunOrganizeDecryptTemporarily_YesFlagRunsAndRestores(t *testing.T) {
	organizeAssumeYes = true
	t.Cleanup(func() { organizeAssumeYes = false })

	memPath := t.TempDir()
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AUXLY_VAULT_KEY", identity.String())
	store := memory.NewStore(memPath)
	if err := store.Write("identity.md", "# Identity\n- Name: Test\n"); err != nil {
		t.Fatal(err)
	}
	if err := store.Write("personal.md", "- secret\n"); err != nil {
		t.Fatal(err)
	}
	if err := store.EncryptFile("personal.md"); err != nil {
		t.Fatal(err)
	}

	// /bin/echo stands in for the CLI agent: it just echoes its args, so the
	// organize model call fails to parse as JSON — that's fine, this test is
	// only checking the decrypt/restore bracket, not a real organize result.
	_ = runOrganizeDecryptTemporarily(store, "Claude Code / CLI", "/bin/echo", []string{"personal.md"})

	raw, err := os.ReadFile(filepath.Join(memPath, "personal.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !vaultcrypt.IsEncrypted(raw) {
		t.Fatal("personal.md was not re-encrypted after runOrganizeDecryptTemporarily returned")
	}
}

// TestDecryptTemporarilyPromptText_MentionsCommandLine is MAJOR 3's
// regression: the CLI consent prompt must name the ps/argv exposure — the
// same warning the TUI's encChoiceView() already shows — so a user typing
// [y] isn't consenting blind.
func TestDecryptTemporarilyPromptText_MentionsCommandLine(t *testing.T) {
	prompt := decryptTemporarilyPromptText([]string{"personal.md"})
	if !strings.Contains(prompt, "command line") {
		t.Fatalf("prompt = %q, want it to mention the command line", prompt)
	}
}

// TestDecryptTemporarilyFlagHelp_MentionsCommandLine covers the other half
// of MAJOR 3: --help must carry the same warning for a user who never hits
// the interactive prompt (e.g. reads --help before scripting --yes).
func TestDecryptTemporarilyFlagHelp_MentionsCommandLine(t *testing.T) {
	f := organizeCmd.Flags().Lookup("decrypt-temporarily")
	if f == nil {
		t.Fatal("--decrypt-temporarily flag not registered")
	}
	if !strings.Contains(f.Usage, "command line") {
		t.Fatalf("flag help = %q, want it to mention the command line", f.Usage)
	}
}

// TestRunOrganizeWithRestore_RestoreFailureReturnsNonNilError is MAJOR 4's
// regression: a restore (re-encrypt) failure must make the command return a
// non-nil error even when the organize run itself succeeded — otherwise the
// process exits 0 while a vault file is left plaintext on disk.
func TestRunOrganizeWithRestore_RestoreFailureReturnsNonNilError(t *testing.T) {
	run := func() memory.OrganizeResult { return memory.OrganizeResult{Success: true, Message: "ok"} }
	restore := func() error { return fmt.Errorf("boom") }

	err := runOrganizeWithRestore(run, restore, []string{"personal.md"})
	if err == nil {
		t.Fatal("a restore failure must make runOrganizeWithRestore return a non-nil error")
	}
}

// MAJOR 9 regression: splitting an ENCRYPTED projects.md must pre-create each
// missing projects/<slug>.md as an empty ENCRYPTED file before queueing its
// first pending addition — otherwise approving that addition would create
// the sub-file as plaintext (state lives in the file, not config).
func TestSeedEncryptedProjectSubFile_ApprovedSplitStaysEncrypted(t *testing.T) {
	memPath := t.TempDir()
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AUXLY_VAULT_KEY", identity.String())

	store := memory.NewStore(memPath)
	subFile := "projects/widget.md"
	if store.Exists(subFile) {
		t.Fatal("test premise broken: sub-file already exists")
	}

	created, err := seedEncryptedProjectSubFile(store, memPath, subFile, true)
	if err != nil {
		t.Fatalf("seedEncryptedProjectSubFile: %v", err)
	}
	if !created {
		t.Fatal("expected the sub-file to be created")
	}

	mgr := pending.NewManager(memPath)
	name, err := mgr.WriteFrom(subFile, "+- first fact about widget\n", "organize-split")
	if err != nil {
		t.Fatalf("WriteFrom: %v", err)
	}
	if err := mgr.Approve(name); err != nil {
		t.Fatalf("Approve: %v", err)
	}

	raw, err := os.ReadFile(memPath + "/" + subFile)
	if err != nil {
		t.Fatal(err)
	}
	if !vaultcrypt.IsEncrypted(raw) {
		t.Fatalf("%s is not encrypted at rest after approval: %q", subFile, raw)
	}

	created2, err := seedEncryptedProjectSubFile(store, memPath, subFile, true)
	if err != nil {
		t.Fatal(err)
	}
	if created2 {
		t.Fatal("seedEncryptedProjectSubFile re-created an already-existing sub-file")
	}
}
