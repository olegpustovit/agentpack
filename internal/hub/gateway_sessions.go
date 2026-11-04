package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"

	"github.com/olegpustovit/agentpack/internal/network"
)

func loadHubSessions(ctx context.Context, home string) ([]listedSession, error) {
	infos, err := network.Sessions(ctx, home)
	if err != nil {
		return nil, err
	}
	machineName, _ := os.Hostname()
	if machineName == "" {
		machineName = "local"
	}
	local := listedFromInfos("local", machineName, infos)
	enabled, err := network.GatewayEnabled(home)
	if err != nil || !enabled {
		return local, nil
	}
	data, err := network.NodeRequest(ctx, home, http.MethodGet, "/api/nodes")
	if err != nil {
		return local, nil
	}
	var nodes []network.TailnetNode
	if json.Unmarshal(data, &nodes) != nil {
		return local, nil
	}
	for _, node := range nodes {
		if !node.Online || node.ID == "" {
			continue
		}
		query := url.Values{"node": {node.ID}}
		data, err := network.NodeRequest(ctx, home, http.MethodGet, "/api/sessions?"+query.Encode())
		if err != nil {
			continue
		}
		var peerSessions []network.SessionInfo
		if json.Unmarshal(data, &peerSessions) != nil {
			continue
		}
		name := node.Name
		if name == "" {
			name = node.ID
		}
		local = append(local, listedFromInfos(node.ID, name, peerSessions)...)
	}
	return local, nil
}

func gatewayPeerPath(path, machine string) string {
	if machine == "" || machine == "local" {
		return path
	}
	u, err := url.Parse(path)
	if err != nil {
		return path
	}
	query := u.Query()
	query.Set("node", machine)
	u.RawQuery = query.Encode()
	return u.String()
}
