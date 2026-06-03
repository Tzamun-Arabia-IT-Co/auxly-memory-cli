package profiler

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/detect"
)

// Profile holds auto-detected user/system information.
type Profile struct {
	// Identity
	Name     string
	Email    string
	OS       string
	Arch     string
	Hostname string
	Timezone string
	Shell    string

	// Dev tools
	Languages  []string
	Frameworks []string
	Editors    []string
	Tools      []string

	// Infrastructure
	CloudCLIs   []string
	PackageMgr  string
	ContainerRT string

	// Agents
	Agents []detect.Agent

	// Projects (recent git repos)
	Projects []ProjectInfo
}

type ProjectInfo struct {
	Name   string
	Path   string
	Remote string
	Lang   string
}

// Detect auto-detects user profile from the system.
func Detect() *Profile {
	p := &Profile{}
	p.detectIdentity()
	p.detectDevTools()
	p.detectInfra()
	p.Agents = detect.InstalledAgents()
	p.detectProjects()
	return p
}

func (p *Profile) detectIdentity() {
	p.Name = gitConfig("user.name")
	p.Email = gitConfig("user.email")
	p.OS = fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH)
	p.Arch = runtime.GOARCH
	p.Hostname, _ = os.Hostname()

	tz, _ := time.Now().Zone()
	_, offset := time.Now().Zone()
	hours := offset / 3600
	if hours >= 0 {
		p.Timezone = fmt.Sprintf("%s (UTC+%d)", tz, hours)
	} else {
		p.Timezone = fmt.Sprintf("%s (UTC%d)", tz, hours)
	}

	p.Shell = os.Getenv("SHELL")
	if p.Shell == "" {
		p.Shell = "unknown"
	} else {
		p.Shell = filepath.Base(p.Shell)
	}
}

func (p *Profile) detectDevTools() {
	// Languages
	langChecks := []struct {
		binary string
		name   string
	}{
		{"go", "Go"},
		{"node", "Node.js"},
		{"python3", "Python"},
		{"python", "Python"},
		{"rustc", "Rust"},
		{"java", "Java"},
		{"ruby", "Ruby"},
		{"php", "PHP"},
		{"swift", "Swift"},
		{"dotnet", "C#/.NET"},
		{"dart", "Dart"},
		{"kotlin", "Kotlin"},
		{"zig", "Zig"},
		{"deno", "Deno"},
		{"bun", "Bun"},
	}

	seen := map[string]bool{}
	for _, c := range langChecks {
		if _, err := exec.LookPath(c.binary); err == nil {
			if !seen[c.name] {
				ver := getVersion(c.binary)
				if ver != "" {
					p.Languages = append(p.Languages, fmt.Sprintf("%s (%s)", c.name, ver))
				} else {
					p.Languages = append(p.Languages, c.name)
				}
				seen[c.name] = true
			}
		}
	}

	// Frameworks / package managers
	fwChecks := []struct {
		binary string
		name   string
	}{
		{"npm", "npm"},
		{"yarn", "Yarn"},
		{"pnpm", "pnpm"},
		{"cargo", "Cargo"},
		{"pip3", "pip"},
		{"pip", "pip"},
		{"composer", "Composer"},
		{"maven", "Maven"},
		{"gradle", "Gradle"},
		{"flutter", "Flutter"},
		{"next", "Next.js"},
		{"vite", "Vite"},
		{"turbo", "Turborepo"},
	}
	seen2 := map[string]bool{}
	for _, c := range fwChecks {
		if _, err := exec.LookPath(c.binary); err == nil {
			if !seen2[c.name] {
				p.Frameworks = append(p.Frameworks, c.name)
				seen2[c.name] = true
			}
		}
	}

	// Tools
	toolChecks := []struct {
		binary string
		name   string
	}{
		{"git", "Git"},
		{"docker", "Docker"},
		{"kubectl", "kubectl"},
		{"terraform", "Terraform"},
		{"ansible", "Ansible"},
		{"make", "Make"},
		{"cmake", "CMake"},
		{"tmux", "tmux"},
		{"gh", "GitHub CLI"},
		{"jq", "jq"},
		{"curl", "curl"},
		{"wget", "wget"},
		{"ffmpeg", "FFmpeg"},
		{"htop", "htop"},
		{"nvim", "Neovim"},
		{"vim", "Vim"},
		{"code", "VS Code"},
	}
	for _, c := range toolChecks {
		if _, err := exec.LookPath(c.binary); err == nil {
			p.Tools = append(p.Tools, c.name)
		}
	}
}

func (p *Profile) detectInfra() {
	// Cloud CLIs
	cloudChecks := []struct {
		binary string
		name   string
	}{
		{"aws", "AWS CLI"},
		{"gcloud", "Google Cloud"},
		{"az", "Azure CLI"},
		{"doctl", "DigitalOcean"},
		{"hcloud", "Hetzner Cloud"},
		{"flyctl", "Fly.io"},
		{"vercel", "Vercel"},
		{"netlify", "Netlify"},
		{"heroku", "Heroku"},
		{"railway", "Railway"},
		{"supabase", "Supabase"},
		{"firebase", "Firebase"},
		{"wrangler", "Cloudflare Workers"},
	}
	for _, c := range cloudChecks {
		if _, err := exec.LookPath(c.binary); err == nil {
			p.CloudCLIs = append(p.CloudCLIs, c.name)
		}
	}

	// Package manager
	if _, err := exec.LookPath("brew"); err == nil {
		p.PackageMgr = "Homebrew"
	} else if _, err := exec.LookPath("apt"); err == nil {
		p.PackageMgr = "apt"
	} else if _, err := exec.LookPath("dnf"); err == nil {
		p.PackageMgr = "dnf"
	} else if _, err := exec.LookPath("yum"); err == nil {
		p.PackageMgr = "yum"
	} else if _, err := exec.LookPath("pacman"); err == nil {
		p.PackageMgr = "pacman"
	}

	// Container runtime
	if _, err := exec.LookPath("docker"); err == nil {
		p.ContainerRT = "Docker"
	} else if _, err := exec.LookPath("podman"); err == nil {
		p.ContainerRT = "Podman"
	}
}

func (p *Profile) detectProjects() {
	home, _ := os.UserHomeDir()
	searchDirs := []string{
		filepath.Join(home, "projects"),
		filepath.Join(home, "Projects"),
		filepath.Join(home, "dev"),
		filepath.Join(home, "Developer"),
		filepath.Join(home, "src"),
		filepath.Join(home, "repos"),
		filepath.Join(home, "code"),
		filepath.Join(home, "Code"),
		filepath.Join(home, "workspace"),
		filepath.Join(home, "Workspace"),
	}

	seen := map[string]bool{}
	for _, dir := range searchDirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			projectPath := filepath.Join(dir, e.Name())
			gitDir := filepath.Join(projectPath, ".git")
			if _, err := os.Stat(gitDir); err != nil {
				continue
			}
			name := e.Name()
			if seen[name] {
				continue
			}
			seen[name] = true

			proj := ProjectInfo{
				Name: name,
				Path: projectPath,
			}

			// Get remote
			out, err := exec.Command("git", "-C", projectPath, "config", "--get", "remote.origin.url").Output()
			if err == nil {
				proj.Remote = strings.TrimSpace(string(out))
			}

			// Detect primary language
			proj.Lang = detectProjectLang(projectPath)

			p.Projects = append(p.Projects, proj)
			if len(p.Projects) >= 15 {
				return
			}
		}
	}
}

func detectProjectLang(path string) string {
	indicators := []struct {
		file string
		lang string
	}{
		{"go.mod", "Go"},
		{"Cargo.toml", "Rust"},
		{"package.json", "JavaScript/TypeScript"},
		{"requirements.txt", "Python"},
		{"pyproject.toml", "Python"},
		{"Gemfile", "Ruby"},
		{"pom.xml", "Java"},
		{"build.gradle", "Java/Kotlin"},
		{"composer.json", "PHP"},
		{"pubspec.yaml", "Dart/Flutter"},
		{"*.csproj", "C#"},
		{"Makefile", "C/C++"},
		{"CMakeLists.txt", "C/C++"},
	}
	for _, ind := range indicators {
		if _, err := os.Stat(filepath.Join(path, ind.file)); err == nil {
			return ind.lang
		}
	}
	return ""
}

// RenderIdentityMD generates the identity.md content.
func (p *Profile) RenderIdentityMD() string {
	var b strings.Builder
	b.WriteString("# Identity\n\n")
	b.WriteString("## Summary\n")
	b.WriteString("Core identity information — auto-detected by Auxly CLI.\n")
	b.WriteString("Shared across all connected AI agents via unified memory.\n\n")
	b.WriteString("## Details\n")
	b.WriteString(fmt.Sprintf("- Name: %s\n", p.Name))
	b.WriteString(fmt.Sprintf("- Email: %s\n", p.Email))
	b.WriteString(fmt.Sprintf("- Hostname: %s\n", p.Hostname))
	b.WriteString(fmt.Sprintf("- OS: %s\n", p.OS))
	b.WriteString(fmt.Sprintf("- Shell: %s\n", p.Shell))
	b.WriteString(fmt.Sprintf("- Timezone: %s\n", p.Timezone))
	b.WriteString(fmt.Sprintf("\n## Last Updated\n%s\n", time.Now().Format("02/01/2006 15:04:05")))
	return b.String()
}

// RenderPreferencesMD generates the preferences.md content.
func (p *Profile) RenderPreferencesMD() string {
	var b strings.Builder
	b.WriteString("# Preferences\n\n")
	b.WriteString("## Summary\n")
	b.WriteString("Development environment — auto-detected by Auxly CLI.\n")
	b.WriteString("Shared across all connected AI agents via unified memory.\n\n")

	b.WriteString("## Languages\n")
	if len(p.Languages) > 0 {
		for _, l := range p.Languages {
			b.WriteString(fmt.Sprintf("- %s\n", l))
		}
	} else {
		b.WriteString("- (none detected)\n")
	}

	b.WriteString("\n## Package Managers & Frameworks\n")
	if len(p.Frameworks) > 0 {
		for _, f := range p.Frameworks {
			b.WriteString(fmt.Sprintf("- %s\n", f))
		}
	} else {
		b.WriteString("- (none detected)\n")
	}

	b.WriteString("\n## Dev Tools\n")
	if len(p.Tools) > 0 {
		for _, t := range p.Tools {
			b.WriteString(fmt.Sprintf("- %s\n", t))
		}
	} else {
		b.WriteString("- (none detected)\n")
	}

	b.WriteString(fmt.Sprintf("\n## Last Updated\n%s\n", time.Now().Format("02/01/2006 15:04:05")))
	return b.String()
}

// RenderInfraMD generates the infra.md content.
func (p *Profile) RenderInfraMD() string {
	var b strings.Builder
	b.WriteString("# Infrastructure\n\n")
	b.WriteString("## Summary\n")
	b.WriteString("Infrastructure & cloud tools — auto-detected by Auxly CLI.\n")
	b.WriteString("Shared across all connected AI agents via unified memory.\n\n")

	b.WriteString("## System\n")
	b.WriteString(fmt.Sprintf("- OS: %s\n", p.OS))
	b.WriteString(fmt.Sprintf("- Local IP: %s\n", detect.LocalIP()))
	b.WriteString(fmt.Sprintf("- Public IP: %s\n", detect.PublicIP()))
	b.WriteString(fmt.Sprintf("- Environment: %s\n", detect.GetEnvironmentType()))
	if p.PackageMgr != "" {
		b.WriteString(fmt.Sprintf("- Package Manager: %s\n", p.PackageMgr))
	}
	if p.ContainerRT != "" {
		b.WriteString(fmt.Sprintf("- Container Runtime: %s\n", p.ContainerRT))
	}

	b.WriteString("\n## Cloud & Hosting CLIs\n")
	if len(p.CloudCLIs) > 0 {
		for _, c := range p.CloudCLIs {
			b.WriteString(fmt.Sprintf("- %s\n", c))
		}
	} else {
		b.WriteString("- (none detected)\n")
	}

	b.WriteString(fmt.Sprintf("\n## Last Updated\n%s\n", time.Now().Format("02/01/2006 15:04:05")))
	return b.String()
}

// RenderAgentsMD generates the agents.md content.
func (p *Profile) RenderAgentsMD() string {
	var b strings.Builder
	b.WriteString("# Agents\n\n")
	b.WriteString("## Summary\n")
	b.WriteString("AI agents connected to this unified memory — auto-detected by Auxly CLI.\n\n")

	b.WriteString("## Connected Agents\n\n")
	b.WriteString("| Agent | Provider | Connection | Path |\n")
	b.WriteString("|-------|----------|------------|------|\n")

	home, _ := os.UserHomeDir()
	for _, a := range p.Agents {
		shortPath := strings.Replace(a.Path, home, "~", 1)
		b.WriteString(fmt.Sprintf("| %s | %s | %s | %s |\n", a.Name, a.Provider, a.Connection, shortPath))
	}

	b.WriteString(fmt.Sprintf("\n## Last Updated\n%s\n", time.Now().Format("02/01/2006 15:04:05")))
	return b.String()
}

// RenderProductsMD generates the products.md content.
func (p *Profile) RenderProductsMD() string {
	var b strings.Builder
	b.WriteString("# Products\n\n")
	b.WriteString("## Summary\n")
	b.WriteString("Projects found on this machine — auto-detected by Auxly CLI.\n")
	b.WriteString("Shared across all connected AI agents via unified memory.\n\n")

	b.WriteString("## Projects\n\n")
	if len(p.Projects) > 0 {
		for _, proj := range p.Projects {
			b.WriteString(fmt.Sprintf("### %s\n", proj.Name))
			b.WriteString(fmt.Sprintf("- Path: %s\n", proj.Path))
			if proj.Lang != "" {
				b.WriteString(fmt.Sprintf("- Language: %s\n", proj.Lang))
			}
			if proj.Remote != "" {
				b.WriteString(fmt.Sprintf("- Remote: %s\n", proj.Remote))
			}
			b.WriteString("\n")
		}
	} else {
		b.WriteString("(no git repositories found in common project directories)\n")
	}

	b.WriteString(fmt.Sprintf("\n## Last Updated\n%s\n", time.Now().Format("02/01/2006 15:04:05")))
	return b.String()
}

func gitConfig(key string) string {
	out, err := exec.Command("git", "config", "--global", key).Output()
	if err != nil {
		return "(not set)"
	}
	return strings.TrimSpace(string(out))
}

func getVersion(binary string) string {
	out, err := exec.Command(binary, "--version").CombinedOutput()
	if err != nil {
		return ""
	}
	line := strings.TrimSpace(string(out))
	// Extract just version number from common formats
	line = strings.Split(line, "\n")[0]
	// "go version go1.22.0 darwin/arm64" -> "1.22.0"
	if strings.HasPrefix(line, "go version go") {
		parts := strings.Fields(line)
		if len(parts) >= 3 {
			return strings.TrimPrefix(parts[2], "go")
		}
	}
	// "node v20.10.0" -> "v20.10.0"
	if strings.Contains(line, " v") {
		parts := strings.Fields(line)
		for _, p := range parts {
			if strings.HasPrefix(p, "v") {
				return p
			}
		}
	}
	// "Python 3.12.0" -> "3.12.0"
	parts := strings.Fields(line)
	if len(parts) >= 2 {
		return parts[len(parts)-1]
	}
	return ""
}
