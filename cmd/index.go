package cmd

import (
	"context"
	"fmt"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/embed"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/memory"
	"github.com/spf13/cobra"
)

var indexCmd = &cobra.Command{
	Use:   "index",
	Short: "Manage the semantic recall index",
}

var indexRebuildCmd = &cobra.Command{
	Use:   "rebuild",
	Short: "Wipe and rebuild the semantic index from the vault",
	RunE:  runIndexRebuild,
}

var indexStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show semantic index status (provider, model, chunk count)",
	RunE:  runIndexStatus,
}

func init() {
	indexCmd.AddCommand(indexRebuildCmd, indexStatusCmd)
	rootCmd.AddCommand(indexCmd)
}

func runIndexRebuild(cmd *cobra.Command, args []string) error {
	store := memory.NewStore(getMemoryPath())
	emb := embed.New()
	if !emb.Enabled() {
		fmt.Println("⚠️  Semantic index unavailable: no local embedding model configured.")
		fmt.Println("    Run a local embedder (e.g. Ollama with nomic-embed-text), or set")
		fmt.Println("    AUXLY_EMBED_ALLOW_CLOUD=1 to allow a cloud endpoint.")
		return nil
	}

	fmt.Println("⏳ Rebuilding semantic index…")
	n, err := store.RebuildIndex(context.Background(), emb)
	if err != nil {
		return err
	}
	fmt.Printf("✅ Indexed %d chunks (%s/%s)\n", n, emb.Provider(), emb.Model())
	return nil
}

func runIndexStatus(cmd *cobra.Command, args []string) error {
	store := memory.NewStore(getMemoryPath())
	st, err := store.IndexStatus()
	if err != nil {
		return err
	}

	if !st.Built {
		fmt.Println("Semantic index: not built yet (run 'auxly index rebuild' or just use recall)")
		return nil
	}

	fmt.Println("🔎 Semantic Index Status")
	fmt.Println("────────────────────────")
	fmt.Printf("Path:      %s\n", st.Path)
	fmt.Printf("Provider:  %s\n", st.Provider)
	fmt.Printf("Model:     %s\n", st.Model)
	fmt.Printf("Dim:       %d\n", st.Dim)
	fmt.Printf("Chunks:    %d\n", st.Chunks)
	return nil
}
