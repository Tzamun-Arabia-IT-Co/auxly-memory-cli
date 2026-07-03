package cmd

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/memory"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/vaultcrypt"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var encryptCmd = &cobra.Command{
	Use:   "encrypt",
	Short: "Manage vault encryption-at-rest",
}

var encryptInitPassphraseFlag bool

var encryptInitCmd = &cobra.Command{
	Use:   "init",
	Short: "Generate a vault encryption key and encrypt personal.md",
	Long: `Generates a new vault encryption key (age X25519) and stores it in the macOS
keychain (or a 0600 file on other platforms). Prints the key ONCE for backup —
losing it means losing every file encrypted with it, permanently.

With --passphrase, encrypts the vault with a password YOU choose instead:
no keypair, no 60-character backup key to store — just a password, typed
once and remembered by the keychain (or a 0600 file) afterward. There is no
recovery in this mode: your password IS the key, and losing it means losing
the vault, permanently.

If personal.md exists and is plaintext, it is migrated to encrypted-at-rest.`,
	RunE: runEncryptInit,
}

var encryptFileCmd = &cobra.Command{
	Use:   "file <name>",
	Short: "Encrypt one memory file at rest (e.g. business.md)",
	Args:  cobra.ExactArgs(1),
	RunE:  runEncryptFile,
}

var encryptStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show vault key reachability and which files are encrypted",
	RunE:  runEncryptStatus,
}

var decryptCmd = &cobra.Command{
	Use:   "decrypt",
	Short: "Escape hatch: remove vault encryption-at-rest",
}

var decryptFileCmd = &cobra.Command{
	Use:   "file <name>",
	Short: "Decrypt one memory file back to plaintext (asks for confirmation)",
	Args:  cobra.ExactArgs(1),
	RunE:  runDecryptFile,
}

func init() {
	encryptInitCmd.Flags().BoolVar(&encryptInitPassphraseFlag, "passphrase", false, "Encrypt with a password you choose instead of a generated keypair")
	encryptCmd.AddCommand(encryptInitCmd, encryptFileCmd, encryptStatusCmd)
	decryptCmd.AddCommand(decryptFileCmd)
	rootCmd.AddCommand(encryptCmd)
	rootCmd.AddCommand(decryptCmd)
}

func runEncryptInit(cmd *cobra.Command, args []string) error {
	if err := requireInit(); err != nil {
		return err
	}
	if encryptInitPassphraseFlag {
		return runEncryptInitPassphrase()
	}

	memPath := getMemoryPath()
	ks := vaultcrypt.NewKeystore(filepath.Dir(memPath))

	if err := ks.Generate(); err != nil {
		if errors.Is(err, vaultcrypt.ErrKeyExists) {
			fmt.Println("🔑 A vault key already exists — nothing generated.")
			fmt.Println("   Run `auxly encrypt status` to see where it lives, or `auxly encrypt file <name>` to encrypt more files with it.")
			return nil
		}
		return fmt.Errorf("generate vault key: %w", err)
	}

	key, err := ks.ExportKey()
	if err != nil {
		return fmt.Errorf("export vault key for backup: %w", err)
	}

	fmt.Println("🔐 Vault encryption key generated.")
	fmt.Println()
	fmt.Println("⚠️  ⚠️  ⚠️   SAVE THIS KEY NOW — IT IS SHOWN ONLY THIS ONCE   ⚠️  ⚠️  ⚠️")
	fmt.Println("If you lose it, every file encrypted with it becomes permanently unreadable.")
	fmt.Println("Copy it into a password manager before you do anything else.")
	fmt.Println()
	fmt.Println("  " + key)
	fmt.Println()

	migratePersonalMD(memory.NewStore(memPath), memPath)
	return nil
}

// runEncryptInitPassphrase is `auxly encrypt init --passphrase`: the
// password-based alternative to the keypair flow above. Same end state
// (personal.md migrated/seeded encrypted-at-rest), different key material —
// a password the user chose, stored the same transparent way (keychain, else
// a 0600 file) instead of a generated X25519 keypair with a backup key to
// stash somewhere.
func runEncryptInitPassphrase() error {
	memPath := getMemoryPath()
	ks := vaultcrypt.NewKeystore(filepath.Dir(memPath))

	pass, err := promptNewPassphrase(bufio.NewReader(os.Stdin))
	if err != nil {
		return err
	}

	fmt.Println()
	fmt.Println("⚠️  ⚠️  ⚠️   THERE IS NO RECOVERY KEY IN THIS MODE   ⚠️  ⚠️  ⚠️")
	fmt.Println("Your vault's security is exactly your password's strength. Anyone who has")
	fmt.Println("it can read your vault, and if you forget it, everything encrypted with it")
	fmt.Println("becomes permanently unreadable — there is no backup key to fall back on.")
	fmt.Println("Store the password itself in a password manager.")
	fmt.Println()

	if err := ks.GeneratePassphrase(pass); err != nil {
		if errors.Is(err, vaultcrypt.ErrKeyExists) {
			fmt.Println("🔑 A vault key already exists — nothing generated.")
			fmt.Println("   Run `auxly encrypt status` to see where it lives.")
			return nil
		}
		return fmt.Errorf("generate vault passphrase key: %w", err)
	}

	fmt.Println("🔐 Vault password set.")
	migratePersonalMD(memory.NewStore(memPath), memPath)
	return nil
}

const minPassphraseLen = 8

// promptNewPassphrase prompts twice (confirming the two match) and rejects
// anything shorter than minPassphraseLen. r backs the non-terminal fallback
// path so callers can inject a fake stdin in tests; the real terminal check
// always inspects the process's actual stdin regardless of r.
func promptNewPassphrase(r *bufio.Reader) (string, error) {
	for {
		pass, err := readPassphrase("New vault password: ", r)
		if err != nil {
			return "", err
		}
		if len(pass) < minPassphraseLen {
			fmt.Printf("Password must be at least %d characters — try again.\n", minPassphraseLen)
			continue
		}
		confirm, err := readPassphrase("Confirm vault password: ", r)
		if err != nil {
			return "", err
		}
		if pass != confirm {
			fmt.Println("Passwords did not match — try again.")
			continue
		}
		return pass, nil
	}
}

// isTerminalStdin reports whether stdin is a real terminal. A var, not a
// direct call, so tests can force the piped-stdin fallback path
// deterministically instead of depending on whether the test process itself
// happens to have a terminal attached (varies by environment/CI).
var isTerminalStdin = func() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}

// readPassphrase prints prompt and reads one line without echoing it back,
// when stdin is a real terminal (golang.org/x/term.ReadPassword). Piped
// stdin (CI, tests) has no terminal to suppress echo on, so it falls back to
// reading one line from r — the SAME *bufio.Reader across an entire prompt
// sequence, never a fresh one per call: a fresh bufio.Reader re-wrapping
// os.Stdin on every call would silently drop whatever it over-read into its
// own internal buffer on the previous call.
func readPassphrase(prompt string, r *bufio.Reader) (string, error) {
	fmt.Print(prompt)
	if isTerminalStdin() {
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Println()
		if err != nil {
			return "", fmt.Errorf("read passphrase: %w", err)
		}
		return string(b), nil
	}
	line, err := r.ReadString('\n')
	line = strings.TrimRight(line, "\r\n")
	if err != nil && line == "" {
		if err == io.EOF {
			return "", fmt.Errorf("read passphrase: no input (stdin closed)")
		}
		return "", fmt.Errorf("read passphrase: %w", err)
	}
	return line, nil
}

// migratePersonalMD encrypts an existing plaintext personal.md, or seeds a
// fresh encrypted empty one when none exists yet — shared by both init paths
// (keypair and passphrase) so a vault ends up with personal.md
// encrypted-at-rest from day one regardless of which mode generated the key.
func migratePersonalMD(store *memory.Store, memPath string) {
	if store.Exists("personal.md") {
		if err := store.EncryptFile("personal.md"); err != nil {
			fmt.Printf("⚠️  key generated, but encrypting personal.md failed: %v\n", err)
			fmt.Println("   personal.md is untouched (still plaintext) — retry with `auxly encrypt file personal.md`.")
			return
		}
		fmt.Println("✅ personal.md is now encrypted at rest.")
		return
	}

	// MAJOR 8: no personal.md yet on this fresh vault. Encryption state lives
	// in the file itself (its age header), not in config — so a personal.md
	// created LATER by an agent's first write would default to plaintext and
	// stay that way forever, since nothing would ever migrate it. Seed an
	// empty encrypted file now so writes to it stay encrypted from day one.
	if err := seedEncryptedPersonalMD(store, memPath); err != nil {
		fmt.Printf("⚠️  key generated, but creating an encrypted personal.md failed: %v\n", err)
		return
	}
	fmt.Println("✅ personal.md created and encrypted at rest — writes to it will stay encrypted from day one.")
}

// seedEncryptedPersonalMD creates an empty ENCRYPTED personal.md under lock
// (state-lives-in-file trick — see runEncryptInit). Split out so the seeding
// itself is directly testable without going through the cobra command.
func seedEncryptedPersonalMD(store *memory.Store, memPath string) error {
	unlock, err := memory.LockVault(memPath)
	if err != nil {
		return err
	}
	defer unlock()
	// Re-check inside the lock: the caller's Exists() ran unlocked, and a
	// concurrent agent write creating personal.md in that window must not be
	// clobbered by the empty seed.
	if store.Exists("personal.md") {
		return nil
	}
	return store.WriteVaultFile(filepath.Join(memPath, "personal.md"), []byte("# Personal\n"), 0o644, true)
}

func runEncryptFile(cmd *cobra.Command, args []string) error {
	if err := requireInit(); err != nil {
		return err
	}
	memPath := getMemoryPath()
	store := memory.NewStore(memPath)
	name := args[0]
	if !store.Exists(name) {
		return fmt.Errorf("%s not found", name)
	}
	if err := store.EncryptFile(name); err != nil {
		return err
	}
	fmt.Printf("✅ %s is now encrypted at rest.\n", name)
	return nil
}

func runDecryptFile(cmd *cobra.Command, args []string) error {
	if err := requireInit(); err != nil {
		return err
	}
	memPath := getMemoryPath()
	store := memory.NewStore(memPath)
	name := args[0]

	fmt.Printf("⚠️  This removes encryption-at-rest from %s — it will be stored as plaintext from now on.\n", name)
	fmt.Print("Type 'yes' to confirm: ")
	resp, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	if strings.TrimSpace(resp) != "yes" {
		fmt.Println("Aborted — nothing changed.")
		return nil
	}

	if err := store.DecryptFile(name); err != nil {
		return err
	}
	fmt.Printf("🔓 %s is now plaintext.\n", name)
	return nil
}

func runEncryptStatus(cmd *cobra.Command, args []string) error {
	memPath := getMemoryPath()
	ks := vaultcrypt.NewKeystore(filepath.Dir(memPath))
	store := memory.NewStore(memPath)

	fmt.Printf("🗝️  mode: %s\n", ks.Mode())

	source, keyErr := ks.Source()
	switch {
	case keyErr == nil:
		fmt.Printf("🔑 vault key reachable — source: %s\n", source)
	case errors.Is(keyErr, vaultcrypt.ErrKeychainUnavailable):
		fmt.Println("🔒 vault keychain unavailable (locked, access denied, or timed out)")
	case errors.Is(keyErr, vaultcrypt.ErrNoKey):
		fmt.Println("🔓 no vault key found — run `auxly encrypt init`")
	default:
		fmt.Printf("⚠️  key lookup error: %v\n", keyErr)
	}

	encFiles, err := encryptedFileNames(store)
	if err != nil {
		return fmt.Errorf("list vault files: %w", err)
	}
	if len(encFiles) == 0 {
		fmt.Println("📄 no encrypted files")
		return nil
	}
	fmt.Printf("📄 %d encrypted file(s):\n", len(encFiles))
	for _, name := range encFiles {
		fmt.Println("   - " + name)
	}
	if keyErr != nil {
		fmt.Println("⚠️  encrypted files exist but the vault key is not reachable right now — they cannot be read until it is.")
	}
	return nil
}

// encryptedFileNames scans every vault file's on-disk header (no decryption,
// no key required) and returns the names that are encrypted at rest.
func encryptedFileNames(store *memory.Store) ([]string, error) {
	files, err := store.List()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, f := range files {
		raw, rerr := os.ReadFile(f.Path)
		if rerr != nil {
			continue
		}
		if vaultcrypt.IsEncrypted(raw) {
			out = append(out, f.Name)
		}
	}
	return out, nil
}
