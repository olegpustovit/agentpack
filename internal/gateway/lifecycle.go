package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"

	"github.com/olegpustovit/agentpack/internal/network"
	"github.com/olegpustovit/agentpack/internal/paths"
)

type AccountStatus struct {
	Joining       bool   `json:"joining,omitempty"`
	Claimed       bool   `json:"claimed"`
	Legacy        bool   `json:"legacy"`
	Authenticated bool   `json:"authenticated"`
	Email         string `json:"email"`
}

func GatewayAccountStatus(ctx context.Context, home string) (AccountStatus, error) {
	var status AccountStatus
	data, err := NodeRequest(ctx, home, http.MethodGet, "/api/status")
	if err == nil {
		err = json.Unmarshal(data, &status)
	}
	return status, err
}

var ErrGatewayDisabled = errors.New("gateway is stopped; run agentpack gateway start to enable sharing")

func GatewayEnabled(home string) (bool, error) {
	_, err := os.Stat(filepath.Join(home, "network", "disabled"))
	if os.IsNotExist(err) {
		return true, nil
	}
	return false, err
}
func SetGatewayEnabled(home string, enabled bool) error {
	directory := filepath.Join(home, "network")
	if err := paths.EnsurePrivateDirectory(directory); err != nil {
		return err
	}
	path := filepath.Join(directory, "disabled")
	if enabled {
		err := os.Remove(path)
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return os.WriteFile(path, []byte("Run agentpack gateway start to enable sharing.\n"), 0600)
}
func GatewayURL(home string) (string, error) {
	record, err := network.LoadNode(home)
	if err != nil {
		return "", err
	}
	return "http://" + record.Address, nil
}
