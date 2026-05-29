package cmd

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/Tzamun-Arabia-IT-Co/auxly-cli/internal/memory"
	"github.com/spf13/cobra"
)

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List all memory files",
	RunE:  runList,
}

func init() {
	rootCmd.AddCommand(listCmd)
}

func runList(cmd *cobra.Command, args []string) error {
	store := memory.NewStore(getMemoryPath())
	files, err := store.List()
	if err != nil {
		return err
	}

	if len(files) == 0 {
		fmt.Println("No memory files found. Run 'auxly init' to create the default structure.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "FILE\tSIZE\tMODIFIED\n")
	fmt.Fprintf(w, "────\t────\t────────\n")
	for _, f := range files {
		fmt.Fprintf(w, "%s\t%d B\t%s\n", f.Name, f.Size, f.ModTime.Format("2006-01-02 15:04"))
	}
	w.Flush()
	return nil
}
