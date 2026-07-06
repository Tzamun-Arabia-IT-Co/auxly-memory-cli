package cmd

import (
	"context"
	"fmt"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/config"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/embed"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/memory"
	"github.com/spf13/cobra"
)

var (
	indexEndpoint   string
	indexEmbedModel string
	indexAllowCloud bool
)

var indexCmd = &cobra.Command{
	Use:   "index",
	Short: "Manage the semantic recall index",
}

var indexRebuildCmd = &cobra.Command{
	Use:   "rebuild",
	Short: "Wipe and rebuild the semantic index from the vault",
	Long: "Wipe and rebuild the semantic index from the vault.\n\n" +
		"The --endpoint/--embed-model/--allow-cloud flags PERSIST to\n" +
		"~/.auxly/settings.json, so the auxly MCP server — which an agent spawns\n" +
		"without your interactive-shell env — uses the same embedder for recall.\n\n" +
		"Remote Ollama example:\n" +
		"  auxly index rebuild --endpoint http://192.168.1.141:11434/v1/embeddings",
	RunE: runIndexRebuild,
}

var indexStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show semantic index status (provider, model, chunk count)",
	RunE:  runIndexStatus,
}

func init() {
	indexRebuildCmd.Flags().StringVar(&indexEndpoint, "endpoint", "", "embeddings API URL (OpenAI-compatible /v1/embeddings) — persisted to config")
	indexRebuildCmd.Flags().StringVar(&indexEmbedModel, "embed-model", "", "embedding model name (e.g. nomic-embed-text) — persisted to config")
	indexRebuildCmd.Flags().BoolVar(&indexAllowCloud, "allow-cloud", false, "permit a non-local (public) embeddings host — persisted to config")
	indexCmd.AddCommand(indexRebuildCmd, indexStatusCmd)
	rootCmd.AddCommand(indexCmd)
}

// persistEmbedFlags writes any embed flags the user passed into
// ~/.auxly/settings.json, so embed.New() (here AND in the MCP server) resolves
// them from config without needing the env vars. Returns whether it saved.
func persistEmbedFlags(cmd *cobra.Command) error {
	if !cmd.Flags().Changed("endpoint") && !cmd.Flags().Changed("embed-model") && !cmd.Flags().Changed("allow-cloud") {
		return nil
	}
	s := config.LoadSettings()
	if cmd.Flags().Changed("endpoint") {
		s.EmbedEndpoint = indexEndpoint
	}
	if cmd.Flags().Changed("embed-model") {
		s.EmbedModel = indexEmbedModel
	}
	if cmd.Flags().Changed("allow-cloud") {
		s.EmbedAllowCloud = indexAllowCloud
	}
	if err := config.SaveSettings(s); err != nil {
		return fmt.Errorf("save embed settings: %w", err)
	}
	fmt.Println("💾 Saved embed settings to ~/.auxly/settings.json (agent recall uses these too).")
	return nil
}

func runIndexRebuild(cmd *cobra.Command, args []string) error {
	// Persist provided flags FIRST so the embed client below — and every later
	// MCP-server recall — resolves the same endpoint/model from config.
	if err := persistEmbedFlags(cmd); err != nil {
		return err
	}

	store := memory.NewStore(getMemoryPath())
	emb := embed.New()
	if !emb.Enabled() {
		fmt.Println("⚠️  Semantic index unavailable: no embedding endpoint configured.")
		fmt.Println("    Run a local embedder (Ollama + nomic-embed-text), or point at one:")
		fmt.Println("      auxly index rebuild --endpoint http://<host>:11434/v1/embeddings")
		fmt.Println("    A public host also needs --allow-cloud (or AUXLY_EMBED_ALLOW_CLOUD=1).")
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

	// Surface the persisted endpoint regardless of build state — it's the first
	// thing to check when a remote embedder "doesn't work".
	cfgEndpoint := config.LoadSettings().EmbedEndpoint

	if !st.Built {
		fmt.Println("Semantic index: not built yet (run 'auxly index rebuild' or just use recall)")
		if cfgEndpoint != "" {
			fmt.Printf("Configured endpoint: %s\n", cfgEndpoint)
		}
		return nil
	}

	fmt.Println("🔎 Semantic Index Status")
	fmt.Println("────────────────────────")
	fmt.Printf("Path:      %s\n", st.Path)
	fmt.Printf("Provider:  %s\n", st.Provider)
	fmt.Printf("Model:     %s\n", st.Model)
	fmt.Printf("Dim:       %d\n", st.Dim)
	fmt.Printf("Chunks:    %d\n", st.Chunks)
	if cfgEndpoint != "" {
		fmt.Printf("Endpoint:  %s (config)\n", cfgEndpoint)
	}
	return nil
}
