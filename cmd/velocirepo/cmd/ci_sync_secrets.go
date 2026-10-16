package cmd

import (
	"bufio"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/posit-dev/velocirepo/internal/auth"
	"github.com/posit-dev/velocirepo/internal/config"
	"github.com/spf13/cobra"
	"golang.org/x/crypto/nacl/box"
)

func syncSecretsCmd() *cobra.Command {
	var repo string
	var dryRun bool
	var force bool
	var envFile string

	cmd := &cobra.Command{
		Use:     "sync-secrets",
		Short:   "Sync .env secrets to GitHub Actions repository secrets",
		Long:    "Reads secrets from the .env file and sets them as GitHub Actions secrets using the GitHub API.",
		GroupID: "ci",
		RunE: func(cmd *cobra.Command, args []string) error {
			if repo == "" {
				owner, name := config.DetectGitHub()
				if owner == "" || name == "" {
					return fmt.Errorf("could not detect repository from git remote; use --repo owner/name")
				}
				repo = owner + "/" + name
			}
			if !strings.Contains(repo, "/") {
				return fmt.Errorf("repo must be in owner/name format")
			}

			envPath := envFile
			if envPath == "" {
				envPath = filepath.Join(cfg.Dir, ".env")
			}

			secrets, err := parseEnvFile(envPath)
			if err != nil {
				return fmt.Errorf("read .env: %w", err)
			}
			if len(secrets) == 0 {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "No secrets found in .env file")
				return nil
			}

			// Rename GITHUB_* to GH_* (GITHUB_ prefix is reserved on GitHub Actions)
			for name, value := range secrets {
				if strings.HasPrefix(name, "GITHUB_") {
					ghName := "GH_" + strings.TrimPrefix(name, "GITHUB_")
					secrets[ghName] = value
					delete(secrets, name)
				}
			}

			if len(secrets) == 0 {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "No secrets to sync")
				return nil
			}

			if dryRun {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Would sync %d secret(s) to %s:\n", len(secrets), repo)
				for name := range secrets {
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  %s\n", name)
				}
				return nil
			}

			if !force && !isInteractive() {
				return fmt.Errorf("cannot prompt for confirmation (use --force to skip prompts)")
			}

			token := os.Getenv("GITHUB_TOKEN")
			if token == "" {
				token = os.Getenv("GH_TOKEN")
			}
			if token == "" {
				return fmt.Errorf("GITHUB_TOKEN or GH_TOKEN environment variable is required")
			}

			client := &http.Client{}
			pubKey, keyID, err := getRepoPublicKey(client, token, repo)
			if err != nil {
				return fmt.Errorf("get repository public key: %w", err)
			}

			var reader *bufio.Reader
			if !force {
				reader = bufio.NewReader(os.Stdin)
			}

			synced := 0
			for name, value := range secrets {
				if !force {
					ok, err := confirm(cmd.OutOrStdout(), reader, fmt.Sprintf("Sync %s to %s?", name, repo))
					if err != nil {
						return err
					}
					if !ok {
						_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  %s (skipped)\n", name)
						continue
					}
				}

				encrypted, err := encryptSecret(pubKey, value)
				if err != nil {
					return fmt.Errorf("encrypt secret %s: %w", name, err)
				}
				if err := putSecret(client, token, repo, name, encrypted, keyID); err != nil {
					return fmt.Errorf("set secret %s: %w", name, err)
				}
				synced++
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  %s ✓\n", name)
			}

			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Synced %d secret(s) to %s\n", synced, repo)
			return nil
		},
	}

	cmd.Flags().StringVar(&repo, "repo", "", "target repository (owner/name; default: from git remote)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would be synced without making changes")
	cmd.Flags().BoolVarP(&force, "force", "f", false, "skip confirmation prompts")
	cmd.Flags().StringVar(&envFile, "env-file", "", "path to .env file (default: .env in config directory)")

	return cmd
}

func parseEnvFile(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	secrets := make(map[string]string)
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		// Use the same unquoting logic that auth.UpsertEnvFile writes so
		// that values with special characters ($, ", `, !, \) and embedded
		// newlines or carriage returns round-trip correctly.
		value := auth.UnquoteEnvValue(strings.TrimSpace(parts[1]))
		secrets[key] = value
	}
	return secrets, nil
}

var githubAPIBase = "https://api.github.com"

func getRepoPublicKey(client *http.Client, token, repo string) ([]byte, string, error) {
	url := fmt.Sprintf("%s/repos/%s/actions/secrets/public-key", githubAPIBase, repo)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, "", fmt.Errorf("GitHub API returned %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Key   string `json:"key"`
		KeyID string `json:"key_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, "", err
	}

	keyBytes, err := base64.StdEncoding.DecodeString(result.Key)
	if err != nil {
		return nil, "", fmt.Errorf("decode public key: %w", err)
	}
	return keyBytes, result.KeyID, nil
}

func encryptSecret(publicKey []byte, secret string) (string, error) {
	var recipientKey [32]byte
	if len(publicKey) != 32 {
		return "", fmt.Errorf("public key must be 32 bytes, got %d", len(publicKey))
	}
	copy(recipientKey[:], publicKey)

	encrypted, err := box.SealAnonymous(nil, []byte(secret), &recipientKey, rand.Reader)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(encrypted), nil
}

func putSecret(client *http.Client, token, repo, name, encryptedValue, keyID string) error {
	url := fmt.Sprintf("%s/repos/%s/actions/secrets/%s", githubAPIBase, repo, name)

	body, err := json.Marshal(map[string]string{
		"encrypted_value": encryptedValue,
		"key_id":          keyID,
	})
	if err != nil {
		return err
	}

	req, err := http.NewRequest("PUT", url, strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusNoContent {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("GitHub API returned %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}
