package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// ---------------------------------------------------------------------------
// Clients registry — the remote boxes THIS Mac (as a host) has wired to use its
// memory. Lets the user see + manage (disconnect / reconnect / rename / remove)
// every connection from one place. Stored at ~/.auxly/clients.yaml.
// ---------------------------------------------------------------------------

type clientEntry struct {
	Name   string `yaml:"name"`             // friendly label
	Target string `yaml:"target"`           // [user@]host[:port] of the box
	Method string `yaml:"method,omitempty"` // relay
}

type clientsFile struct {
	Clients []clientEntry `yaml:"clients"`
}

func clientsPath() (string, error) {
	dir, err := auxlyDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "clients.yaml"), nil
}

func loadClients() ([]clientEntry, error) {
	path, err := clientsPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var f clientsFile
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, err
	}
	return f.Clients, nil
}

func saveClients(cs []clientEntry) error {
	dir, err := auxlyDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	data, err := yaml.Marshal(clientsFile{Clients: cs})
	if err != nil {
		return err
	}
	path := filepath.Join(dir, "clients.yaml")
	return os.WriteFile(path, data, 0644)
}

func upsertClient(c clientEntry) error {
	cs, _ := loadClients()
	out := make([]clientEntry, 0, len(cs)+1)
	replaced := false
	for _, e := range cs {
		if e.Name == c.Name {
			out = append(out, c)
			replaced = true
		} else {
			out = append(out, e)
		}
	}
	if !replaced {
		out = append(out, c)
	}
	return saveClients(out)
}

func findClient(name string) (clientEntry, bool) {
	cs, _ := loadClients()
	for _, e := range cs {
		if e.Name == name {
			return e, true
		}
	}
	return clientEntry{}, false
}

func removeClientEntry(name string) error {
	cs, _ := loadClients()
	out := make([]clientEntry, 0, len(cs))
	for _, e := range cs {
		if e.Name != name {
			out = append(out, e)
		}
	}
	return saveClients(out)
}

// clientProfile builds an SSH profile for reaching a client box.
func clientProfile(c clientEntry) (remoteProfile, error) {
	u, h, p, err := parseHostSpec(c.Target)
	if err != nil {
		return remoteProfile{}, err
	}
	return remoteProfile{Name: c.Name, Method: "public", User: u, Host: h, Port: p}, nil
}

// ---------------------------------------------------------------------------
// Commands (also driven by the TUI Remote tab)
// ---------------------------------------------------------------------------

var hostClientsCmd = &cobra.Command{
	Use:          "clients",
	Short:        "List the remote boxes using this Mac's memory",
	SilenceUsage: true,
	RunE:         runHostClients,
}

var hostDisconnectCmd = &cobra.Command{
	Use:          "disconnect <name>",
	Short:        "Remove this Mac's memory wiring from a connected box (leave no trace)",
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE:         runHostDisconnect,
}

var hostReconnectCmd = &cobra.Command{
	Use:          "reconnect <name>",
	Short:        "Re-wire a box to this Mac's memory",
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE:         runHostReconnect,
}

var hostForgetCmd = &cobra.Command{
	Use:          "forget <name>",
	Short:        "Disconnect a box and drop it from the connections list",
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE:         runHostForget,
}

func runHostClients(cmd *cobra.Command, args []string) error {
	cs, err := loadClients()
	if err != nil {
		return err
	}
	if len(cs) == 0 {
		fmt.Println("No connected boxes yet. Set one up with `auxly host provision`.")
		return nil
	}
	fmt.Println("Remote boxes using this Mac's memory:")
	for _, c := range cs {
		fmt.Printf("  • %-20s %s [%s]\n", c.Name, c.Target, c.Method)
	}
	return nil
}

func runHostDisconnect(cmd *cobra.Command, args []string) error {
	c, ok := findClient(args[0])
	if !ok {
		return fmt.Errorf("no connection named %q", args[0])
	}
	p, err := clientProfile(c)
	if err != nil {
		return err
	}
	fmt.Printf("🔌 Disconnecting %s (%s)...\n", c.Name, c.Target)
	out, err := runSSH(p, "auxly", "connect", "disconnect", offerName(), "--purge")
	if err != nil {
		fmt.Printf("   ⚠ remote disconnect failed: %v\n   %s\n", err, firstLine(out))
		return err
	}
	fmt.Printf("   ✓ Removed the launcher + skills on %s (no trace left)\n", c.Name)
	fmt.Println("   👉 Restart the agent on that box to drop the link.")
	return nil
}

func runHostReconnect(cmd *cobra.Command, args []string) error {
	c, ok := findClient(args[0])
	if !ok {
		return fmt.Errorf("no connection named %q", args[0])
	}
	p, err := clientProfile(c)
	if err != nil {
		return err
	}
	fmt.Printf("🔗 Reconnecting %s (%s)...\n", c.Name, c.Target)
	out, err := runSSH(p, "auxly", "connect", "auto")
	if err != nil {
		fmt.Printf("   ⚠ remote reconnect failed: %v\n   %s\n", err, firstLine(out))
		return err
	}
	fmt.Printf("   ✓ Re-wired %s to this Mac's memory\n", c.Name)
	fmt.Println("   👉 Restart the agent on that box to load the link.")
	return nil
}

func runHostForget(cmd *cobra.Command, args []string) error {
	c, ok := findClient(args[0])
	if !ok {
		return fmt.Errorf("no connection named %q", args[0])
	}
	// Best-effort disconnect first so we don't leave the box wired.
	if p, err := clientProfile(c); err == nil {
		if _, derr := runSSH(p, "auxly", "connect", "disconnect", offerName(), "--purge"); derr == nil {
			fmt.Printf("   ✓ Disconnected %s\n", c.Name)
		} else {
			fmt.Printf("   ⚠ Could not reach %s to disconnect (removing locally anyway)\n", c.Name)
		}
	}
	if err := removeClientEntry(c.Name); err != nil {
		return err
	}
	fmt.Printf("🗑️  Removed %q from the connections list.\n", c.Name)
	return nil
}
