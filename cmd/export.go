package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/memory"
	"github.com/spf13/cobra"
)

var exportDest string

var exportCmd = &cobra.Command{
	Use:   "export",
	Short: "Export all memory files to a timestamped folder (default: ~/Downloads)",
	Long: `Export every memory .md file to a fresh timestamped folder so you can keep or
share a snapshot. Each file is tagged with its name and the export time — in the folder
name, the file name, and a header comment — and a MANIFEST.txt records the set.

  auxly export                 export to ~/Downloads/auxly-memory-export-<timestamp>/
  auxly export --dest /tmp     export under a different folder`,
	SilenceUsage: true,
	RunE:         runExport,
}

func init() {
	exportCmd.Flags().StringVar(&exportDest, "dest", "", "destination folder (default: ~/Downloads)")
	rootCmd.AddCommand(exportCmd)
}

func runExport(cmd *cobra.Command, args []string) error {
	dest := exportDest
	if dest == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("cannot resolve home directory: %w", err)
		}
		dest = filepath.Join(home, "Downloads")
	}

	store := memory.NewStore(getMemoryPath())
	res, err := store.Export(dest, time.Now())
	if err != nil {
		return err
	}
	fmt.Printf("✓ Exported %d memory file(s) to:\n   %s\n", len(res.Files), res.Dir)
	return nil
}
