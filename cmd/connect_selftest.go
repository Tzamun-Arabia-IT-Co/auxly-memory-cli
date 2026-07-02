package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

var connectMCPSelftest bool

type selftestRPCResponse struct {
	JSONRPC string             `json:"jsonrpc"`
	ID      int                `json:"id"`
	Result  selftestToolResult `json:"result,omitempty"`
	Error   *selftestRPCError  `json:"error,omitempty"`
}

type selftestRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type selftestToolResult struct {
	Content []selftestToolContent `json:"content"`
	IsError bool                  `json:"isError,omitempty"`
}

type selftestToolContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// runConnectSelftest probes the memory link end to end. It walks the SAME
// host_bin fallback chain as the real launcher (runConnectMCP) — a probe that
// only tries the configured host_bin would report FAIL hostbin for a link the
// agents' launcher would transparently recover, poisoning every health surface
// built on this signal (doctor, host clients, provisioning).
func runConnectSelftest(p remoteProfile) error {
	var lastErr error
	for _, bin := range hostBinCandidates(p) {
		err := runConnectSelftestWith(p, bin)
		if err == nil {
			return nil
		}
		lastErr = err
		if !strings.Contains(err.Error(), "selftest hostbin") {
			return err // real failure — don't mask it behind candidate walking
		}
	}
	return lastErr
}

func runConnectSelftestWith(p remoteProfile, bin string) error {
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	provider := connectMCPProvider
	if provider == "" {
		provider = defaultProviderID
	}

	sshArgs := []string{
		"-T",
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=10",
		"-C",
	}
	if p.Method == "rendezvous" || p.Jump != "" {
		sshArgs = append(sshArgs, "-o", "StrictHostKeyChecking=accept-new")
	}
	if p.Jump != "" {
		sshArgs = append(sshArgs, "-J", p.Jump)
	}
	if p.Port != 0 && p.Port != defaultSSHPort {
		sshArgs = append(sshArgs, "-p", strconv.Itoa(p.Port))
	}
	sshArgs = append(sshArgs, p.SSHArgs...)
	sshArgs = append(sshArgs, "--", connTarget(p))
	serverArgs := []string{
		bin,
		"mcp-server",
		"--provider", provider,
		"--source", "ssh-remote",
		"--remote-os", runtime.GOOS,
		"--remote-host", localHostname(),
	}
	if p.MemPath != "" {
		serverArgs = append(serverArgs, "--path", p.MemPath)
	}
	sshArgs = append(sshArgs, serverArgs...)

	cmd := exec.CommandContext(ctx, "ssh", sshArgs...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return printSelftestFailure("ssh", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return printSelftestFailure("ssh", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return printSelftestFailure("ssh", err)
	}

	if err := cmd.Start(); err != nil {
		return printSelftestFailure("ssh", err)
	}
	defer func() {
		_ = stdin.Close()
	}()

	stderrCh := make(chan string, 1)
	go func() {
		data, _ := io.ReadAll(io.LimitReader(stderr, 64*1024))
		stderrCh <- strings.TrimSpace(string(data))
	}()

	responses := make(chan selftestLine, 8)
	go scanSelftestLines(stdout, responses)

	waitCh := make(chan error, 1)
	go func() {
		waitCh <- cmd.Wait()
	}()

	if err := writeSelftestRequest(stdin, selftestInitializeRequest()); err != nil {
		return finishSelftestFailure(ctx, waitCh, stderrCh, false, "handshake", err)
	}
	initResp, err := waitSelftestResponse(ctx, responses, waitCh, stderrCh, 1, false, "handshake")
	if err != nil {
		return err
	}
	if initResp.Error != nil {
		return finishSelftestFailure(ctx, waitCh, stderrCh, true, "handshake", errors.New(initResp.Error.Message))
	}

	if err := writeSelftestRequest(stdin, selftestListRequest()); err != nil {
		return finishSelftestFailure(ctx, waitCh, stderrCh, true, "read", err)
	}
	listResp, err := waitSelftestResponse(ctx, responses, waitCh, stderrCh, 2, true, "read")
	if err != nil {
		return err
	}
	if listResp.Error != nil {
		return finishSelftestFailure(ctx, waitCh, stderrCh, true, "read", errors.New(listResp.Error.Message))
	}
	if listResp.Result.IsError {
		return finishSelftestFailure(ctx, waitCh, stderrCh, true, "read", errors.New("auxly_memory_list returned isError"))
	}

	elapsed := time.Since(start)
	fmt.Println(formatSelftestResult(countSelftestMemoryFiles(listResp.Result), elapsed))
	return nil
}

type selftestLine struct {
	resp selftestRPCResponse
	ok   bool
	text string
}

func scanSelftestLines(stdout io.Reader, out chan<- selftestLine) {
	defer close(out)
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		text := strings.TrimSpace(scanner.Text())
		if text == "" {
			continue
		}
		var resp selftestRPCResponse
		out <- selftestLine{resp: resp, ok: json.Unmarshal([]byte(text), &resp) == nil, text: text}
	}
}

func waitSelftestResponse(ctx context.Context, lines <-chan selftestLine, waitCh <-chan error, stderrCh <-chan string, id int, sawJSON bool, phase string) (selftestRPCResponse, error) {
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				return selftestRPCResponse{}, finishSelftestFailure(ctx, waitCh, stderrCh, sawJSON, phase, errors.New("stdout closed before response"))
			}
			if !line.ok {
				sawJSON = true
				continue
			}
			sawJSON = true
			if line.resp.ID == id {
				return line.resp, nil
			}
		case err := <-waitCh:
			return selftestRPCResponse{}, printClassifiedSelftestFailure(err, stderrCh, sawJSON, phase, errors.New("ssh exited before response"))
		case <-ctx.Done():
			return selftestRPCResponse{}, finishSelftestFailure(ctx, waitCh, stderrCh, sawJSON, phase, ctx.Err())
		}
	}
}

func finishSelftestFailure(ctx context.Context, waitCh <-chan error, stderrCh <-chan string, sawJSON bool, phase string, cause error) error {
	select {
	case err := <-waitCh:
		return printClassifiedSelftestFailure(err, stderrCh, sawJSON, phase, cause)
	case <-ctx.Done():
		if phase == "read" {
			return printSelftestFailure("read", cause)
		}
		return printSelftestFailure("server", cause)
	default:
		if phase == "read" {
			return printSelftestFailure("read", cause)
		}
		return printSelftestFailure("server", cause)
	}
}

func printClassifiedSelftestFailure(err error, stderrCh <-chan string, sawJSON bool, phase string, cause error) error {
	exitCode, isExit := selftestExit(err)
	class := classifySelftestFailure(exitCode, isExit, sawJSON, phase)
	detail := firstSelftestDetail(cause, stderrCh)
	return printSelftestFailure(class, errors.New(detail))
}

func firstSelftestDetail(cause error, stderrCh <-chan string) string {
	if cause != nil && strings.TrimSpace(cause.Error()) != "" {
		return oneLineSelftestDetail(cause.Error())
	}
	select {
	case s := <-stderrCh:
		if s != "" {
			return oneLineSelftestDetail(s)
		}
	default:
	}
	return "probe failed"
}

func printSelftestFailure(class string, err error) error {
	detail := "probe failed"
	if err != nil && strings.TrimSpace(err.Error()) != "" {
		detail = oneLineSelftestDetail(err.Error())
	}
	fmt.Printf("FAIL %s: %s\n", class, detail)
	return fmt.Errorf("selftest %s: %s", class, detail)
}

func selftestExit(err error) (int, bool) {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), true
	}
	return 0, false
}

func writeSelftestRequest(w io.Writer, req map[string]interface{}) error {
	data, err := json.Marshal(req)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "%s\n", data)
	return err
}

func selftestInitializeRequest() map[string]interface{} {
	return map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]interface{}{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]interface{}{},
			"clientInfo": map[string]interface{}{
				"name":    "auxly-selftest",
				"version": "1",
			},
		},
	}
}

func selftestListRequest() map[string]interface{} {
	return map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/call",
		"params": map[string]interface{}{
			"name":      "auxly_memory_list",
			"arguments": map[string]interface{}{},
		},
	}
}

func countSelftestMemoryFiles(result selftestToolResult) int {
	if len(result.Content) == 0 {
		return 0
	}
	n := 0
	for _, line := range strings.Split(result.Content[0].Text, "\n") {
		if strings.Contains(line, ".md") {
			n++
		}
	}
	return n
}

func classifySelftestFailure(exitCode int, isExit bool, sawJSON bool, phase string) string {
	if phase == "read" {
		return "read"
	}
	if !sawJSON && isExit {
		switch exitCode {
		case 255:
			return "ssh"
		case 127:
			return "hostbin"
		}
	}
	return "server"
}

func formatSelftestResult(n int, elapsed time.Duration) string {
	if elapsed < 5*time.Second {
		return fmt.Sprintf("OK (%d files)", n)
	}
	return fmt.Sprintf("SLOW (%d files, took %.1fs)", n, elapsed.Seconds())
}

func oneLineSelftestDetail(s string) string {
	fields := strings.Fields(strings.TrimSpace(s))
	if len(fields) == 0 {
		return "probe failed"
	}
	return strings.Join(fields, " ")
}
