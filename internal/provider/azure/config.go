// Package azure reaches machines through Azure Bastion.
//
// Two things shape everything here. Reachability is a network join, as it is
// on Oracle: a bastion lives in one virtual network and reaches the machines
// in it, so a healthy machine can be unreachable because nobody put a bastion
// in its network. And unlike every other cloud here, a gateway can exist and
// still be useless - Bastion's native client, which is the only way to reach a
// machine from a terminal rather than a browser, needs both a Standard or
// Premium SKU and tunnelling switched on. A Basic bastion is a real, running,
// correctly configured resource that this tool cannot use, and saying so
// plainly is most of what this provider is for.
package azure

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
)

// ConfigDir is where the az CLI keeps its state. AZURE_CONFIG_DIR overrides
// it, the same variable the vendor CLI honours.
func ConfigDir() string {
	if d := os.Getenv("AZURE_CONFIG_DIR"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".azure")
}

// profilePath is the file the az CLI writes its known subscriptions to.
func profilePath() string {
	if d := ConfigDir(); d != "" {
		return filepath.Join(d, "azureProfile.json")
	}
	return ""
}

// Configured reports whether this machine has any Azure sign-in at all.
//
// Like the other providers this touches only the filesystem and environment:
// detection runs on every invocation, including for clouds the user has never
// used, so it must not make a network call.
func Configured() bool {
	if os.Getenv("AZURE_CLIENT_ID") != "" && os.Getenv("AZURE_TENANT_ID") != "" {
		return true
	}
	p := profilePath()
	if p == "" {
		return false
	}
	st, err := os.Stat(p)
	return err == nil && !st.IsDir() && st.Size() > 0
}

// Subscription is one subscription the local sign-in knows about.
type Subscription struct {
	ID      string
	Name    string
	Default bool
}

// Subscriptions reads what the az CLI has cached.
//
// It reads the file rather than calling the API because it is also used to
// resolve which subscription "no --profile" means, and that answer has to be
// available before any client is built. The file is UTF-8 with a BOM on some
// platforms, which json.Unmarshal will not accept.
func Subscriptions() ([]Subscription, error) {
	p := profilePath()
	if p == "" {
		return nil, fmt.Errorf("cannot locate the Azure config directory")
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	raw = bytes.TrimPrefix(raw, []byte{0xEF, 0xBB, 0xBF})

	var doc struct {
		Subscriptions []struct {
			ID        string `json:"id"`
			Name      string `json:"name"`
			IsDefault bool   `json:"isDefault"`
			State     string `json:"state"`
		} `json:"subscriptions"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("reading %s: %w", p, err)
	}
	var out []Subscription
	for _, s := range doc.Subscriptions {
		// A disabled subscription answers every call with an error, so
		// including one turns a clean sweep into a page of noise.
		if s.State != "" && !strings.EqualFold(s.State, "Enabled") {
			continue
		}
		out = append(out, Subscription{ID: s.ID, Name: s.Name, Default: s.IsDefault})
	}
	return out, nil
}

// defaultSubscription is what "no --profile" means here: whichever one the az
// CLI would use with no flags.
func defaultSubscription() (Subscription, error) {
	subs, err := Subscriptions()
	if err != nil {
		return Subscription{}, err
	}
	for _, s := range subs {
		if s.Default {
			return s, nil
		}
	}
	if len(subs) == 1 {
		return subs[0], nil
	}
	return Subscription{}, fmt.Errorf("no default subscription; run: az account set --subscription <name>")
}

// resolve turns what the user asked for into a subscription. A name is
// accepted as readily as an ID, because nobody remembers the ID.
func resolve(account string) (Subscription, error) {
	if account == "" {
		return defaultSubscription()
	}
	subs, err := Subscriptions()
	if err != nil {
		return Subscription{}, err
	}
	for _, s := range subs {
		if strings.EqualFold(s.ID, account) || strings.EqualFold(s.Name, account) {
			return s, nil
		}
	}
	return Subscription{}, fmt.Errorf("no subscription named %q; run: az account list", account)
}

// credential builds the token source.
//
// DefaultAzureCredential ends at the az CLI's own cached sign-in, which is how
// this works for a person at a terminal, while still picking up a service
// principal or managed identity from the environment when one is set. That
// ordering means ssmate never asks for a secret: whatever signed az in signs
// this in too.
func credential() (*azidentity.DefaultAzureCredential, error) {
	return azidentity.NewDefaultAzureCredential(nil)
}

// credentialProblem turns a token failure into the command that fixes it.
func credentialProblem(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "AADSTS700082"), strings.Contains(msg, "expired"),
		strings.Contains(msg, "re-authenticate"), strings.Contains(msg, "AADSTS50173"):
		return fmt.Errorf("the Azure sign-in has expired; run: az login")
	case strings.Contains(msg, "please run 'az login'"), strings.Contains(msg, "az login"):
		return fmt.Errorf("not signed in to Azure; run: az login")
	}
	return err
}
