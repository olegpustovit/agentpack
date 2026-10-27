package network

import (
	"errors"
	"os"
	"path/filepath"
)

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
	if err := EnsurePrivateDirectory(directory); err != nil {
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
	record, err := loadNode(home)
	if err != nil {
		return "", err
	}
	return "http://" + record.Address, nil
}
