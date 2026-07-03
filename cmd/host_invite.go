package cmd

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/audit"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/clipboard"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/invite"
	"github.com/spf13/cobra"
)

// copyInvite is a package-level var (not a direct clipboard.Copy call) so
// tests can stub it — the real thing shells out to a platform tool that
// isn't guaranteed present on a CI box.
var copyInvite = clipboard.Copy

// ---------------------------------------------------------------------------
// `auxly host invite` / `auxly host consume` — one-command remote pairing over
// a DIRECT SSH connection (Sprint 21). This is deliberately separate from the
// relay/`host setup` flow above: the joiner already has SSH login to this box
// (the invite pins identity, it does not grant OS access), so there is no
// tunnel to provision — just a single-use token that (1) proves the joiner is
// talking to the right box, via the SSH host-key fingerprint pinned at mint
// time, and (2) is consumed exactly once so it can't be replayed.
// ---------------------------------------------------------------------------

var (
	hostInviteTTL    string
	hostInviteHost   string
	hostInvitePort   int
	hostInviteList   bool
	hostInviteRevoke string

	hostInviteNoCopy bool

	hostConsumeClient   string
	hostConsumeHostname string
)

var hostInviteCmd = &cobra.Command{
	Use:          "invite",
	Short:        "Mint a one-time token so a box can `auxly join` this machine's memory over SSH",
	SilenceUsage: true,
	Long: `invite mints a short-lived, single-use token that pairs a box to THIS
machine's memory over a DIRECT SSH connection (no relay).

The joiner runs "auxly join <token>" on their own machine. The token pins
THIS machine's SSH host-key fingerprint so the joiner can detect a MITM'd
host — but the token does NOT grant SSH/OS access: the joiner still needs
their own working SSH login to this box before the join can succeed.

  auxly host invite            mint a token (default TTL 15m)
  auxly host invite --list     show pending (unconsumed, unexpired) invites
  auxly host invite --revoke <id>   cancel a pending invite before it's used`,
	RunE: runHostInvite,
}

// hostConsumeCmd is plumbing: `auxly join` execs this ON the host over the
// joiner's own SSH connection. Hidden — it is never meant to be typed by a
// human directly.
var hostConsumeCmd = &cobra.Command{
	Use:          "consume <secret>",
	Short:        "Consume a pending invite (plumbing — invoked by `auxly join` over SSH)",
	Hidden:       true,
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE:         runHostConsume,
}

func runHostInvite(cmd *cobra.Command, args []string) error {
	dir, err := auxlyDir()
	if err != nil {
		return err
	}
	store := invite.NewStore(dir)

	if hostInviteRevoke != "" {
		if err := store.Revoke(hostInviteRevoke); err != nil {
			return fmt.Errorf("revoke invite %q: %w", hostInviteRevoke, err)
		}
		fmt.Printf("✓ Revoked invite %s\n", hostInviteRevoke)
		return nil
	}

	if hostInviteList {
		recs, err := store.List()
		if err != nil {
			return err
		}
		if len(recs) == 0 {
			fmt.Println("No pending invites.")
			return nil
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintf(w, "ID\tHOST\tEXPIRES\n")
		for _, r := range recs {
			fmt.Fprintf(w, "%s\t%s:%d\t%s\n", r.ID, r.Host, r.Port, r.Expires.Local().Format("2006-01-02 15:04"))
		}
		return w.Flush()
	}

	ttl, err := time.ParseDuration(hostInviteTTL)
	if err != nil || ttl <= 0 {
		return fmt.Errorf("invalid --ttl %q — use a duration like 15m, 1h, 24h", hostInviteTTL)
	}

	host := strings.TrimSpace(hostInviteHost)
	if host == "" {
		host = localHostname()
	}
	port := hostInvitePort
	if port == 0 {
		port = defaultSSHPort
	}

	// The pin: THIS machine's own SSH host-key fingerprint, exactly as a
	// joiner will independently re-derive it by scanning host:port. See
	// hostKeyFingerprint's doc comment for why ssh-keyscan (not reading
	// /etc/ssh/*.pub) — it's what makes the two sides' fingerprints agree.
	fp, err := hostKeyFingerprint("localhost", port)
	if err != nil {
		return fmt.Errorf("could not read this machine's own SSH host key (is sshd running on port %d?): %w", port, err)
	}

	token, err := invite.Mint(host, port, fp, ttl)
	if err != nil {
		return err
	}
	if err := store.Add(token); err != nil {
		return err
	}
	recordHostInviteAudit("invite_minted", token.ID())

	encoded := token.Encode()
	fmt.Println("🎫 Auxly invite minted — copy this to the joining machine:")
	fmt.Println()
	fmt.Println("   " + encoded)
	fmt.Println()
	fmt.Printf("   Expires : %s (in %s)\n", token.Expires.Local().Format("2006-01-02 15:04"), ttl)
	fmt.Printf("   Pairs   : %s:%d (this machine's SSH key: %s)\n", host, port, fp)
	fmt.Println()
	fmt.Println("👉 On the joining machine (it must already have SSH login to this box):")
	fmt.Println("   auxly join " + encoded)
	fmt.Println()
	fmt.Println("   Single-use — consumed automatically on the first successful join.")

	if line := inviteCopyLine(hostInviteNoCopy, copyInvite, encoded); line != "" {
		fmt.Println(line)
	}
	return nil
}

// inviteCopyLine is the pure(ish) core of the post-mint auto-copy step:
// given --no-copy (skip) and the clipboard func to use (copyFn — injected so
// tests never touch a real clipboard tool), it copies token and returns the
// line to print, or "" when skip is set. Kept separate from runHostInvite
// (which needs a live sshd to reach at all) so this one decision is
// independently testable.
func inviteCopyLine(skip bool, copyFn func(string) error, token string) string {
	if skip {
		return ""
	}
	if err := copyFn(token); err != nil {
		return "\033[38;5;240m(clipboard unavailable — copy the token above manually)\033[0m"
	}
	return "📋 copied to clipboard"
}

// consumeInvite is the pure(ish) core of `host consume`: given a store, the
// presented secret, and THIS host's own current fingerprint (nowFingerprint —
// injected so tests never need a live sshd), it consumes the invite and
// returns the record, or one of three friendly, greppable error strings
// mirroring the Store's Err* sentinels. `auxly join` classifies its SSH exec
// of this command by matching those exact strings (see
// classifyJoinConsumeError in join.go) — keep them in sync if reworded.
// Side effects (client registry write, audit log) stay in runHostConsume so
// this stays testable against a plain temp Store.
func consumeInvite(store *invite.Store, secret, nowFingerprint string) (invite.Rec, error) {
	rec, err := store.Consume(secret, nowFingerprint)
	if err != nil {
		switch {
		case errors.Is(err, invite.ErrUnknown):
			return invite.Rec{}, errors.New("invite already used or unknown")
		case errors.Is(err, invite.ErrExpired):
			return invite.Rec{}, errors.New("invite expired")
		case errors.Is(err, invite.ErrFingerprint):
			return invite.Rec{}, errors.New("invite fingerprint mismatch — this host's current SSH key does not match what was pinned at mint time")
		default:
			return invite.Rec{}, err
		}
	}
	return rec, nil
}

func runHostConsume(cmd *cobra.Command, args []string) error {
	secret := args[0]
	dir, err := auxlyDir()
	if err != nil {
		return err
	}
	store := invite.NewStore(dir)

	// Recompute THIS host's own current fingerprint the same way `host
	// invite` did, against the PORT that invite actually pinned (not a
	// hardcoded 22 — a token minted with `--port 2222` must be verified
	// against 2222, or consume always mismatches on a nonstandard sshd
	// port). Store.Consume compares it against the fingerprint pinned at
	// mint time, so a token minted against a since-rotated host key is
	// refused rather than silently honored.
	fp, ferr := hostKeyFingerprint("localhost", consumeFingerprintPort(store, secret))
	if ferr != nil {
		fp = "" // best-effort: Consume just won't match a non-empty pin
	}

	rec, err := consumeInvite(store, secret, fp)
	if err != nil {
		return err
	}

	clientName := strings.TrimSpace(hostConsumeClient)
	if clientName == "" {
		clientName = "invited-" + rec.ID
	}
	// Best-effort bookkeeping — same registry `provisionRemote`'s manual flow
	// writes to (host_clients.go), so an invited box shows up in `auxly host
	// clients` exactly like a manually-connected one. Target comes from
	// sshd's own view of who's calling (SSH_CONNECTION), since the host never
	// dials the joiner — see clientAddrFromSSHConnection.
	target := clientAddrFromSSHConnection(os.Getenv("SSH_CONNECTION"))
	if err := registerConsumedClient(clientName, strings.TrimSpace(hostConsumeHostname), target); err != nil {
		fmt.Printf("⚠ consumed the invite but could not register the client locally: %v\n", err)
	} else {
		fmt.Printf("✓ Registered %q (manage with `auxly host clients`)\n", clientName)
	}

	// Audit the invite's ID, never the raw secret (it's already spent by now
	// anyway, but this keeps the log consistent with the Store's own guard).
	recordHostInviteAudit("invite_consumed", rec.ID)
	fmt.Println("OK")
	return nil
}

// consumeFingerprintPort returns the port whose SSH host key should be
// recomputed to verify secret's pending invite: the invite's own pinned
// port when it can be looked up, defaultSSHPort as a harmless fallback
// otherwise. Store.Consume (called right after) is what actually enforces
// correctness atomically — this only decides which port THIS process
// re-probes before that call.
func consumeFingerprintPort(store *invite.Store, secret string) int {
	if rec, err := store.Lookup(secret); err == nil && rec.Port != 0 {
		return rec.Port
	}
	return defaultSSHPort
}

// clientNameRe bounds --client/--hostname, both joiner-controlled and both
// fed into clients.yaml and the `auxly host clients` tabwriter table: without
// this, a crafted value could inject a terminal escape sequence into that
// table, or (via YAML) corrupt clients.yaml. The charset excludes anything a
// legitimate friendly name or hostname would never need.
var clientNameRe = regexp.MustCompile(`^[a-zA-Z0-9._-]{1,64}$`)

func validateClientName(label, name string) error {
	if !clientNameRe.MatchString(name) {
		return fmt.Errorf("%s %q is invalid — must match ^[a-zA-Z0-9._-]{1,64}$", label, name)
	}
	return nil
}

// registerConsumedClient validates the joiner-supplied client/hostname and
// registers the box, refusing rather than silently clobbering an existing,
// DIFFERENT box already registered under the same name (a name collision
// with the SAME target is a legitimate re-join and still upserts in place).
func registerConsumedClient(clientName, hostname, target string) error {
	if err := validateClientName("--client", clientName); err != nil {
		return err
	}
	if hostname != "" {
		if err := validateClientName("--hostname", hostname); err != nil {
			return err
		}
	}
	if existing, ok := findClient(clientName); ok && existing.Target != target {
		return fmt.Errorf("client %q already registered — revoke/rename first", clientName)
	}
	return upsertClient(clientEntry{Name: clientName, Target: target, Method: "invite", Hostname: hostname})
}

// clientAddrFromSSHConnection extracts the connecting client's IP from
// sshd's SSH_CONNECTION env var ("client_ip client_port server_ip
// server_port"), best-effort ("" if unset/unparseable). Pure — the raw env
// value is passed in so this is unit-testable without a real SSH session.
func clientAddrFromSSHConnection(v string) string {
	fields := strings.Fields(v)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

func recordHostInviteAudit(action, inviteID string) {
	logger, err := audit.NewLogger(getMemoryPath())
	if err != nil {
		return
	}
	defer logger.Close()
	_, _ = logger.Log("auxly-invite", "system", action, inviteID, "", "SSH-pairing invite lifecycle", "auto")
}
