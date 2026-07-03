package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/invite"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/statusline"
	"github.com/spf13/cobra"
)

// ---------------------------------------------------------------------------
// `auxly join <token>` — the client side of Sprint 21's one-command pairing.
// Deliberately built on the CONSUMER-direction machinery (`connect use` /
// createConsumerProfile: local key auth, local remotes.yaml entry, local
// launcher+skills injection), not provisionRemote — provisionRemote pushes
// config onto whatever it SSHes into, which here would wire the HOST's agents
// to read the JOINER's memory: backwards from the intent (the joiner reads
// the HOST's memory). See the Sprint 21 report for the full reasoning; the
// "existing provisionRemote/selftest wiring" reused here is the same building
// blocks provisionRemote itself uses (upsertRemote/injectRemoteConfigs/
// installAuxlySkills + the connect-mcp --selftest proof), applied in the
// consumer direction join actually needs.
// ---------------------------------------------------------------------------

var (
	joinName string
	joinUser string
)

var joinCmd = &cobra.Command{
	Use:          "join <token>",
	Short:        "Pair with a host's memory using a token from `auxly host invite`",
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	Long: `join consumes a one-time invite token minted by "auxly host invite" on
another machine, and wires THIS machine's agents to read that host's memory
over SSH.

The token pins the host's SSH host-key fingerprint — join verifies it BEFORE
doing anything else, and refuses to proceed if the host it reaches doesn't
match (blocks a MITM'd host). The token does NOT grant SSH/OS access on its
own: this machine must already have a working SSH login to the host (the same
login "ssh <host>" would use) before join can succeed.`,
	RunE: runJoin,
}

func init() {
	joinCmd.Flags().StringVar(&joinName, "name", "", "profile name for the host (default: the host's address)")
	joinCmd.Flags().StringVar(&joinUser, "user", "", "SSH login on the host (default: your normal SSH default)")
	rootCmd.AddCommand(joinCmd)
}

func runJoin(cmd *cobra.Command, args []string) error {
	tok, err := invite.Decode(args[0])
	if err != nil {
		return fmt.Errorf("that doesn't look like an auxly invite token: %w", err)
	}
	if err := validateInviteToken(tok); err != nil {
		return err
	}

	if err := joinPreflight(tok, time.Now(), hostKeyFingerprint); err != nil {
		return err
	}
	fmt.Printf("✓ Host identity verified (%s:%d matches the invite's pinned SSH key)\n", tok.Host, tok.Port)

	name := strings.TrimSpace(joinName)
	if name == "" {
		name = tok.Host
	}
	p := remoteProfile{
		Name: name, Method: "public", User: strings.TrimSpace(joinUser), Host: tok.Host, Port: tok.Port,
		// joinPreflight (above) already independently verified this exact
		// host's live SSH key against the invite's pinned fingerprint before
		// we ever got here — ssh's own interactive TOFU prompt would just
		// re-ask what we've already cryptographically proven, and BatchMode
		// turns that prompt into a hard "Host key verification failed" for
		// any host the joiner hasn't `ssh`'d into manually before. A key
		// change AFTER today still hard-fails on a later connection (normal
		// known_hosts behavior) — this only accepts the first sighting.
		//
		// This accept-new profile is what gets SAVED (upsertRemote, below)
		// for ordinary future use. The ONE exec that actually carries
		// tok.Secret does NOT use it — see consumeProfile below, which pins
		// StrictHostKeyChecking=yes against the exact key joinPreflight just
		// verified, closing the TOCTOU window a racing MITM would otherwise
		// get between the preflight probe and this connection.
		SSHArgs: []string{"-o", "StrictHostKeyChecking=accept-new"},
	}

	knownHosts, algo, cleanupKnownHosts, kerr := pinnedKnownHostsFile(tok.Host, tok.Port, tok.Fingerprint)
	if kerr != nil {
		return fmt.Errorf("could not pin the host key for the invite exchange: %w", kerr)
	}
	defer cleanupKnownHosts()
	consumeProfile := withoutMux(p) // never reuse a pre-existing, unpinned ControlMaster
	consumeProfile.SSHArgs = pinnedSSHArgs(knownHosts, algo)

	clientName := localHostname()
	fmt.Println("🤝 Consuming the invite on the host...")
	// ponytail: the secret rides the SSH exec argv (same-machine argv exposure
	// on the HOST for the exec's brief duration — visible to other local users
	// via `ps` there, not to anyone on the network). runSSH/runSSHCtx have no
	// stdin plumbing and are shared by every SSH call site in this package;
	// adding one just for this call is a bigger, riskier diff than the
	// exposure it would close. Upgrade path: add optional Stdin to runSSHCtx
	// if a lower-trust host operator ever needs it.
	//
	// The remote command below is built via shellQuote/remoteShellArgv (the
	// same pattern every other remote-script call site in this package uses,
	// see remoteshell.go) rather than raw argv: OpenSSH joins a multi-element
	// remote command into ONE string for the remote shell to re-parse, so an
	// unquoted tok.Secret/clientName would let a crafted invite (or hostname)
	// inject shell syntax. validateInviteToken (above) also defensively
	// rejects a Secret/Host outside their expected charset before we ever get
	// here — belt and suspenders.
	consumeArgv, aerr := buildConsumeArgv(hostAuxlyBin(p), tok, clientName)
	if aerr != nil {
		return aerr
	}
	out, cerr := runSSH(consumeProfile, consumeArgv...)
	if cerr != nil {
		return classifyJoinConsumeError(out, cerr)
	}
	fmt.Println("   ✓ Invite consumed — it cannot be reused")

	if err := upsertRemote(p); err != nil {
		return err
	}
	fmt.Printf("💾 Saved remote profile %q (%s)\n", p.Name, connTarget(p))

	injectRemoteConfigs(p.Name)
	installAuxlySkills(remoteBanner())
	if wired := statusline.AutoInstallMissing(); len(wired) > 0 {
		fmt.Printf("   ✓ Installed the Auxly statusline for: %s\n", strings.Join(wired, ", "))
	}

	fmt.Println("🔎 Verifying the memory link end-to-end...")
	verdict, selftestOK := runLocalSelftest(p.Name)
	fmt.Printf("   %s\n", verdict)

	fmt.Println()
	msg, joined := joinCompletionMessage(connTarget(p), p.Name, verdict, selftestOK)
	fmt.Println(msg)
	if !joined {
		return errors.New("join completed but the local selftest failed — see above")
	}
	return nil
}

// inviteSecretRe / inviteHostRe bound the two decoded token fields that ride
// the remote shell exec (see buildConsumeArgv): Mint always produces a
// Secret in exactly the base32 alphabet tokenEncoding uses, and a Host with
// no shell metacharacters. Anything else means a hand-crafted token — e.g.
// one pasted from a chat message with a malicious Secret — not a real invite
// minted by `auxly host invite`. Checked BEFORE any SSH contact; shellQuote
// in buildConsumeArgv is the second, independent layer (defense in depth).
var (
	inviteSecretRe = regexp.MustCompile(`^[a-z2-7]+$`)
	inviteHostRe   = regexp.MustCompile(`^[a-zA-Z0-9.:_-]+$`)
)

// validateInviteToken rejects a decoded token whose fields fall outside
// their expected shape, with one deliberately generic message ("malformed
// invite") so it never hints at which check failed to a caller who may be
// probing a crafted token.
func validateInviteToken(tok invite.Token) error {
	if !inviteSecretRe.MatchString(tok.Secret) {
		return errors.New("malformed invite")
	}
	if tok.Host == "" || strings.HasPrefix(tok.Host, "-") || !inviteHostRe.MatchString(tok.Host) {
		return errors.New("malformed invite")
	}
	if tok.Port < 1 || tok.Port > 65535 {
		return errors.New("malformed invite")
	}
	return nil
}

// buildConsumeArgv is the pure command-construction path for the SSH exec
// that carries tok.Secret: every dynamic field is shellQuote'd into its own
// single-quoted token before assembly (remoteShellArgv's POSIX branch), so
// OpenSSH's remote-shell join can never see Secret/client as anything but
// inert literal text — never shell syntax, no matter what characters they
// contain.
func buildConsumeArgv(hostBin string, tok invite.Token, client string) ([]string, error) {
	posix := hostBin + " host consume " + shellQuote(tok.Secret) +
		" --client " + shellQuote(client) + " --hostname " + shellQuote(client)
	return remoteShellArgv(classifyOS(""), posix, "")
}

// joinCompletionMessage renders the final join outcome. The invite is
// ALREADY consumed (single-use, burned on the host the moment `host
// consume` returned OK) by the time this runs — if the local selftest then
// fails, silently exiting 0 would leave the user believing they're fully
// joined when the read path is unproven and the invite can't be replayed to
// retry. joined=false means the caller must exit nonzero.
func joinCompletionMessage(target, name, selftestVerdict string, selftestOK bool) (string, bool) {
	if !selftestOK {
		return fmt.Sprintf(
			"⚠ Joined %s, but the local link isn't verified working (%s).\n"+
				"   The invite was already consumed (single-use) — if you need to retry,\n"+
				"   the host must run `auxly host invite` again.\n"+
				"   👉 Retry the link check with `auxly connect-mcp %s --selftest`.",
			target, selftestVerdict, name,
		), false
	}
	return fmt.Sprintf(
		"🎉 Joined %s's memory.\n"+
			"   • connect-mcp launcher injected into your IDEs/agents\n"+
			"   • /auxly-* skills installed (shared-vault banner)\n"+
			"👉 Restart your IDE / agent to load the link.",
		target,
	), true
}

// joinPreflight validates a decoded invite token BEFORE any contact with the
// host beyond the identity probe itself: it must not be expired (checked
// locally, from the token payload — no host contact needed), and the host's
// live SSH identity (from probe) must match the fingerprint pinned at mint
// time. probe is injected (real: hostKeyFingerprint; tests: a fake) so the
// decode+expiry+pin-compare policy is unit-testable without real SSH/
// ssh-keyscan — mirrors the fake-prober seams used by the existing remote
// tests (see connect_probe_retry_test.go, connect_install_verify_test.go).
func joinPreflight(tok invite.Token, now time.Time, probe func(host string, port int) (string, error)) error {
	// Same fail-closed treatment as invite.Store's own isExpired: a zero
	// Expires can only come from hand-built/corrupt data, never a real Mint.
	if tok.Expires.IsZero() || !tok.Expires.After(now) {
		return errors.New("invite expired — ask the host to run `auxly host invite` again")
	}
	fp, err := probe(tok.Host, tok.Port)
	if err != nil {
		return fmt.Errorf("could not verify %s:%d's SSH identity (%w) — this looks like a connectivity problem, not necessarily a bad invite", tok.Host, tok.Port, err)
	}
	if fp != tok.Fingerprint {
		return fmt.Errorf("SSH host-key fingerprint mismatch for %s:%d (got %s, invite pins %s) — refusing to connect: this could be a MITM. If the host's SSH key legitimately changed, get a fresh invite", tok.Host, tok.Port, fp, tok.Fingerprint)
	}
	return nil
}

// classifyJoinConsumeError tells apart the two ways `host consume` over SSH
// can fail: the invite itself is bad (unknown/expired/fingerprint-mismatched
// — `runHostConsume` returns exactly one of these three strings and nothing
// else), versus everything else (SSH couldn't connect/authenticate, auxly
// isn't on the host's PATH, etc.) — never conflate the two, per the honesty
// requirement: no SSH connectivity is not the same failure as a bad invite.
func classifyJoinConsumeError(out string, sshErr error) error {
	line := strings.ToLower(firstLine(out))
	for _, bad := range []string{"invite already used or unknown", "invite expired", "invite fingerprint mismatch"} {
		if strings.Contains(line, bad) {
			return errors.New(firstLine(out))
		}
	}
	return fmt.Errorf("could not consume the invite on the host (not necessarily a bad invite — check SSH connectivity/auth and that auxly is installed there): %w", sshErr)
}

// runLocalSelftest proves the freshly-wired link with a real read, the same
// way doctor.go's probeLinks and provisionRemote do: exec the exact launcher
// the agents use and report its verdict, rather than trusting the config
// writes above actually work. The bool return is the honest pass/fail signal
// runJoin needs to decide its own exit code (see joinCompletionMessage) —
// previously only the human-readable string was returned, so a failure here
// never changed runJoin's (always nil) result.
func runLocalSelftest(name string) (string, bool) {
	exe, err := os.Executable()
	if err != nil || exe == "" {
		exe = "auxly"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, exe, "connect-mcp", name, "--selftest").Output()
	verdict := strings.TrimSpace(firstLine(string(out)))
	if err == nil && strings.HasPrefix(verdict, "OK") {
		return "✅ " + verdict, true
	}
	if verdict == "" {
		verdict = "probe failed"
	}
	return "⚠ selftest: " + verdict + " — re-run with `auxly connect test " + name + "`", false
}
