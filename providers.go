package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// A provider is one way of reaching a model. KeyVar is the variable Hermes
// reads the key from, written to its .env; an empty KeyVar means the key
// goes in config.yaml with the base URL, Hermes' "custom" provider.
type provider struct {
	ID, Name  string
	KeyVar    string
	Base      string // the OpenAI-style base URL; "" is asked of the person
	Anthropic bool   // Anthropic's own headers
	KeysAt    string // where the person gets a key
}

var providers = []provider{
	{ID: "openrouter", Name: "OpenRouter", KeyVar: "OPENROUTER_API_KEY", Base: "https://openrouter.ai/api/v1", KeysAt: "openrouter.ai/keys"},
	{ID: "anthropic", Name: "Anthropic", KeyVar: "ANTHROPIC_API_KEY", Base: "https://api.anthropic.com/v1", Anthropic: true, KeysAt: "console.anthropic.com"},
	{ID: "custom", Name: "An OpenAI-compatible server", KeysAt: "the server's own settings"},
}

func providerByID(id string) (provider, bool) {
	for _, p := range providers {
		if p.ID == id {
			return p, true
		}
	}
	return provider{}, false
}

// errRefused is the provider refusing the key.
var errRefused = errors.New("the provider refused the key")

// listModels asks the provider for its models with the key, which is the
// key's test and the list the person chooses from.
func listModels(ctx context.Context, client *http.Client, p provider, base, key string) ([]string, error) {
	base = strings.TrimRight(base, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/models", nil)
	if err != nil {
		return nil, fmt.Errorf("the address is not one: %w", err)
	}
	if p.Anthropic {
		req.Header.Set("x-api-key", key)
		req.Header.Set("anthropic-version", "2023-06-01")
	} else if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s could not be reached: %w", base, err)
	}
	defer res.Body.Close()
	switch {
	case res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden:
		return nil, errRefused
	case res.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("%s answered %s to the models request", base, res.Status)
	}
	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(&body); err != nil {
		return nil, fmt.Errorf("%s answered something other than a models list", base)
	}
	seen := map[string]bool{}
	var ids []string
	for _, m := range body.Data {
		if m.ID != "" && !seen[m.ID] {
			seen[m.ID] = true
			ids = append(ids, m.ID)
		}
	}
	sort.Strings(ids)
	return ids, nil
}
