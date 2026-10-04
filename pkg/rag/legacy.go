package rag

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Ownera1/pi-rag-go/internal/model"
)

// legacyProvider resolves the registry metadata used by the TypeScript
// embedding fingerprint. An unknown or unsupported provider remains usable
// for BM25, but cannot be used to search vectors.
func legacyProvider(root, providerID, modelName string, dimensions int) (model.ProviderConfig, string, error) {
	var entry struct {
		Type    string `json:"type"`
		BaseURL string `json:"baseUrl"`
		Auth    struct {
			Env string `json:"env"`
		} `json:"auth"`
		Models struct {
			Embedding map[string]struct {
				Dimensions *int `json:"dimensions"`
			} `json:"embedding"`
		} `json:"models"`
	}
	registryPath := filepath.Join(root, "provider.json")
	data, err := os.ReadFile(registryPath)
	if os.IsNotExist(err) && providerID == "voyage" {
		// Match the bundled TypeScript registry when the store has no override.
		entry.Type = "voyage"
		entry.BaseURL = "https://api.voyageai.com/v1"
		entry.Auth.Env = "VOYAGE_API_KEY"
		entry.Models.Embedding = map[string]struct {
			Dimensions *int `json:"dimensions"`
		}{}
		defaultDim := 1024
		entry.Models.Embedding["voyage-4-lite"] = struct {
			Dimensions *int `json:"dimensions"`
		}{&defaultDim}
		entry.Models.Embedding["voyage-4"] = struct {
			Dimensions *int `json:"dimensions"`
		}{&defaultDim}
	} else {
		if err != nil {
			return model.ProviderConfig{}, "", fmt.Errorf("read legacy provider registry: %w", err)
		}
		var registry struct {
			Version   int                        `json:"version"`
			Providers map[string]json.RawMessage `json:"providers"`
		}
		if err := json.Unmarshal(data, &registry); err != nil || registry.Version != 1 {
			return model.ProviderConfig{}, "", fmt.Errorf("invalid legacy provider registry")
		}
		definition, ok := registry.Providers[providerID]
		if !ok {
			return model.ProviderConfig{}, "", fmt.Errorf("legacy provider %q is not registered", providerID)
		}
		if err := json.Unmarshal(definition, &entry); err != nil {
			return model.ProviderConfig{}, "", fmt.Errorf("invalid legacy provider %q: %w", providerID, err)
		}
	}
	if entry.Type != "voyage" || entry.BaseURL == "" || entry.Auth.Env == "" {
		return model.ProviderConfig{}, "", fmt.Errorf("legacy provider %q is not a configured Voyage adapter", providerID)
	}
	modelSpec, ok := entry.Models.Embedding[modelName]
	if !ok {
		return model.ProviderConfig{}, "", fmt.Errorf("legacy provider %q has no embedding model %q", providerID, modelName)
	}
	contractDimensions := dimensions
	if modelSpec.Dimensions != nil {
		contractDimensions = *modelSpec.Dimensions
	}
	// Property order and casing match JSON.stringify in fingerprint.ts.
	contract, _ := json.Marshal(struct {
		Provider   string `json:"provider"`
		Type       string `json:"type"`
		BaseURL    string `json:"baseUrl"`
		Model      string `json:"model"`
		Dimensions int    `json:"dimensions"`
	}{providerID, entry.Type, entry.BaseURL, modelName, contractDimensions})
	hash := sha256.Sum256(contract)
	config := model.ProviderConfig{
		Type:       "voyage",
		Model:      modelName,
		Dimensions: dimensions,
		BaseURL:    entry.BaseURL,
		APIKeyEnv:  entry.Auth.Env,
	}
	return config, hex.EncodeToString(hash[:])[:16], nil
}
