package cmd

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"filippo.io/age"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/memory"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/pending"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/vaultcrypt"
)

func TestResolveHeadlessAgent_EmptyWithEnvMeansDirectLLM(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-key")
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

	err = runOrganizeDecryptTemporarily(store, "Claude Code / CLI", "/bin/echo", []string{"personal.md"}, memory.OrganizeRunOpts{})
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
	_ = runOrganizeDecryptTemporarily(store, "Claude Code / CLI", "/bin/echo", []string{"personal.md"}, memory.OrganizeRunOpts{})

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

	created, err := store.SeedEncryptedProjectSubFile(memPath, subFile, true)
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

	created2, err := store.SeedEncryptedProjectSubFile(memPath, subFile, true)
	if err != nil {
		t.Fatal(err)
	}
	if created2 {
		t.Fatal("seedEncryptedProjectSubFile re-created an already-existing sub-file")
	}
}

// TestRunSweepPendingRoundtrip locks the sweep's full two-phase story against
// the REAL pending queue and the REAL Direct-LLM transport (this test lives
// in cmd, not internal/memory, because pending imports memory — the same
// reason PendingWrite queueing is caller-side; the LLM is a local httptest
// endpoint via AUXLY_LLM_BASE, so no network and no model): the addition
// queues into a taxonomy target; approving lands the fact; only then does
// the next sweep plan see the orphan bullet as provably re-homed and emit
// its cleanup deletion. Finally, once the orphan is drained, the guarded
// empty-removal path removes it outright.
func TestRunSweepPendingRoundtrip(t *testing.T) {
	sweepJSON := `{"moves":[{"target":"infra.md","bullets":["- the cluster runs in riyadh"]}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			fmt.Fprint(w, `{"data":[{"id":"stub-model"}]}`)
			return
		}
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":`+strconv.Quote(sweepJSON)+`}}],"usage":{"total_tokens":10}}`)
	}))
	defer srv.Close()
	t.Setenv("AUXLY_LLM_BASE", srv.URL)

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "infra.md"), []byte("# Infrastructure\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "stray.md"), []byte("# Stray\n- the cluster runs in riyadh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := memory.NewStore(root)
	mgr := pending.NewManager(root)

	// Before approval, the planner must NOT queue a deletion for the orphan —
	// presence is only proven once an addition has actually landed.
	pre, err := store.PlanSweepRun(context.Background(), root, memory.SweepOpts{}, nil)
	if err != nil {
		t.Fatalf("pre-approve PlanSweepRun: %v", err)
	}
	if len(pre.CleanupWrites) != 0 {
		t.Fatalf("unapproved addition must not enable deletion, got %+v", pre.CleanupWrites)
	}
	if len(pre.Writes) != 1 || pre.Writes[0].TargetFile != "infra.md" {
		t.Fatalf("pre-approve writes = %+v, want one infra.md addition", pre.Writes)
	}

	name, err := mgr.WriteFrom(pre.Writes[0].TargetFile, pre.Writes[0].Diff, "organize-sweep")
	if err != nil {
		t.Fatalf("WriteFrom: %v", err)
	}
	if err := mgr.Approve(name); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	infra, rerr := os.ReadFile(filepath.Join(root, "infra.md"))
	if rerr != nil || !strings.Contains(string(infra), "- the cluster runs in riyadh") {
		t.Fatalf("approved fact did not land in infra.md: %q err=%v", infra, rerr)
	}

	// Post-approval: the planner must now emit the stray.md cleanup deletion.
	post, err := store.PlanSweepRun(context.Background(), root, memory.SweepOpts{}, nil)
	if err != nil {
		t.Fatalf("post-approve PlanSweepRun: %v", err)
	}
	found := false
	for _, w := range post.CleanupWrites {
		if w.TargetFile == "stray.md" && strings.Contains(w.Diff, "- the cluster runs in riyadh") {
			found = true
		}
	}
	if !found {
		t.Fatalf("post-approval run must queue the stray.md cleanup, got %+v", post.CleanupWrites)
	}

	// Drain the orphan by approving the cleanup, then the guarded empty
	// removal takes the file itself.
	cname, cerr := mgr.WriteFrom("stray.md", "-- the cluster runs in riyadh\n", "organize-sweep")
	if cerr != nil {
		t.Fatalf("WriteFrom cleanup: %v", cerr)
	}
	if err := mgr.Approve(cname); err != nil {
		t.Fatalf("Approve cleanup: %v", err)
	}
	if removed := store.RemoveEmptyVaultFiles([]string{"stray.md"}); len(removed) != 1 {
		stray, _ := os.ReadFile(filepath.Join(root, "stray.md"))
		t.Fatalf("drained orphan not removed: %v (content %q)", removed, stray)
	}
}

// TestRunOrganizeSweepModeExclusion locks the one-mode-at-a-time guard for
// the new --sweep flag.
func TestRunOrganizeSweepModeExclusion(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".initialized"), []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AUXLY_MEMORY_PATH", dir)
	restore := func() { organizeSplitProjects, organizeContradictions, organizeSweep = false, false, false }
	restore()
	t.Cleanup(restore)

	organizeSplitProjects, organizeSweep = true, true
	err := runOrganize(nil, nil)
	if err == nil || !strings.Contains(err.Error(), "one mode at a time") {
		t.Fatalf("split+sweep must refuse, got %v", err)
	}
	organizeSplitProjects, organizeContradictions = false, true
	err = runOrganize(nil, nil)
	if err == nil || !strings.Contains(err.Error(), "one mode at a time") {
		t.Fatalf("contradictions+sweep must refuse, got %v", err)
	}
}
