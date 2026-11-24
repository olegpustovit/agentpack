package state

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Credentials owns the replicated sign-ins: MCP connections with their plugin approvals, agent
// sign-in files, and the local record of which agent sign-in this machine installed.
type Credentials struct{ s *Store }

func (s *Store) Credentials() Credentials { return Credentials{s} }

// MCPCredential is one stored connection. Client and State are JSON owned by the gateway; the
// repository reads only State's generation.
type MCPCredential struct {
	ID, Match, Kind, Label, Account, Client, State, CreatedNode string
	CreatedAt                                                   int64
	Plugins                                                     []string
}

const mcpCredentialColumns = "id,match,kind,label,account,client,state,created_node,created_at"

func scanMCP(row interface{ Scan(...any) error }) (MCPCredential, error) {
	var c MCPCredential
	err := row.Scan(&c.ID, &c.Match, &c.Kind, &c.Label, &c.Account, &c.Client, &c.State, &c.CreatedNode, &c.CreatedAt)
	return c, err
}

func (c Credentials) queryMCP(ctx context.Context, where string, args ...any) ([]MCPCredential, error) {
	rows, err := c.s.Reader.QueryContext(ctx, "SELECT "+mcpCredentialColumns+" FROM mcp_credentials WHERE "+where+" ORDER BY created_at,id", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []MCPCredential
	for rows.Next() {
		credential, err := scanMCP(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, credential)
	}
	return result, rows.Err()
}

// MCP returns one connection without its approvals, or sql.ErrNoRows.
func (c Credentials) MCP(ctx context.Context, id string) (MCPCredential, error) {
	return scanMCP(c.s.Reader.QueryRowContext(ctx, "SELECT "+mcpCredentialColumns+" FROM mcp_credentials WHERE id=?", id))
}

// MCPOfKind lists every account's connections of one kind, without approvals.
func (c Credentials) MCPOfKind(ctx context.Context, kind string) ([]MCPCredential, error) {
	return c.queryMCP(ctx, "kind=?", kind)
}

// MCPForAccount lists an account's connections with the plugins approved for each.
func (c Credentials) MCPForAccount(ctx context.Context, account string) ([]MCPCredential, error) {
	credentials, err := c.queryMCP(ctx, "account=?", account)
	if err != nil {
		return nil, err
	}
	rows, err := c.s.Reader.QueryContext(ctx, "SELECT a.credential_id,a.module FROM mcp_credential_approvals a JOIN mcp_credentials c ON c.id=a.credential_id WHERE c.account=? ORDER BY a.module", account)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, module string
		if err := rows.Scan(&id, &module); err != nil {
			return nil, err
		}
		for i := range credentials {
			if credentials[i].ID == id {
				credentials[i].Plugins = append(credentials[i].Plugins, module)
			}
		}
	}
	return credentials, rows.Err()
}

// SetMCPState replaces a connection's state. When generation is not nil, a stored state with a
// higher generation (a newer login) wins and the call changes nothing. It reports whether a row changed.
func (c Credentials) SetMCPState(ctx context.Context, id, state string, generation *int64) (bool, error) {
	query, args := "UPDATE mcp_credentials SET state=? WHERE id=?", []any{state, id}
	if generation != nil {
		query += " AND coalesce(json_extract(state,'$.generation'),0)<=?"
		args = append(args, *generation)
	}
	result, err := c.s.DB.ExecContext(ctx, query, args...)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count > 0, err
}

// SaveMCP inserts a connection under newID, or replaces the client and state of the one with the
// same account, kind, URL and label in place so its ID (and staging) stays put. A replacement's
// generation always rises above the stored one. It returns the ID used.
func (c Credentials) SaveMCP(ctx context.Context, credential MCPCredential, newID string) (string, error) {
	var id string
	err := Transaction(ctx, c.s.DB, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, "SELECT id FROM mcp_credentials WHERE account=? AND kind=? AND match=? AND label=?", credential.Account, credential.Kind, credential.Match, credential.Label).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			id = newID
			_, err = tx.ExecContext(ctx, "INSERT INTO mcp_credentials("+mcpCredentialColumns+") VALUES (?,?,?,?,?,?,?,?,?)",
				id, credential.Match, credential.Kind, credential.Label, credential.Account, credential.Client, credential.State, credential.CreatedNode, time.Now().UnixMilli())
			return err
		}
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE mcp_credentials SET client=?1,state=CASE
 WHEN json_valid(state) AND coalesce(json_extract(state,'$.generation'),0)>=coalesce(json_extract(?2,'$.generation'),0)
 THEN json_set(?2,'$.generation',coalesce(json_extract(state,'$.generation'),0)+1) ELSE ?2 END WHERE id=?3`, credential.Client, credential.State, id)
		return err
	})
	return id, err
}

// DeleteMCP removes a connection and its approvals.
func (c Credentials) DeleteMCP(ctx context.Context, id string) error {
	return Transaction(ctx, c.s.DB, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "DELETE FROM mcp_credential_approvals WHERE credential_id=?", id); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "DELETE FROM mcp_credentials WHERE id=?", id)
		return err
	})
}

func (c Credentials) Approve(ctx context.Context, id, module string) error {
	_, err := c.s.DB.ExecContext(ctx, "INSERT OR IGNORE INTO mcp_credential_approvals(credential_id,module,created_at) VALUES (?,?,?)", id, module, time.Now().UnixMilli())
	return err
}

func (c Credentials) Revoke(ctx context.Context, id, module string) error {
	_, err := c.s.DB.ExecContext(ctx, "DELETE FROM mcp_credential_approvals WHERE credential_id=? AND module=?", id, module)
	return err
}

// Agent returns the shared sign-in record for key, or nil when none was published.
func (c Credentials) Agent(ctx context.Context, key string) ([]byte, error) {
	var value string
	err := c.s.Reader.QueryRowContext(ctx, "SELECT value FROM agent_credentials WHERE key=?", key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return []byte(value), err
}

func (c Credentials) PublishAgent(ctx context.Context, key string, value []byte) error {
	_, err := c.s.DB.ExecContext(ctx, "INSERT INTO agent_credentials(key,value) VALUES (?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", key, string(value))
	return err
}

// AgentInstall is this machine's record of the sign-in file it holds for key (never replicated).
func (c Credentials) AgentInstall(ctx context.Context, key string) (hash []byte, rev int64, ok bool, err error) {
	err = c.s.Reader.QueryRowContext(ctx, "SELECT hash,rev FROM agent_credential_installs WHERE key=?", key).Scan(&hash, &rev)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, 0, false, nil
	}
	return hash, rev, err == nil, err
}

func (c Credentials) RecordAgentInstall(ctx context.Context, key string, hash []byte, rev int64) error {
	_, err := c.s.DB.ExecContext(ctx, "INSERT INTO agent_credential_installs(key,hash,rev) VALUES (?,?,?) ON CONFLICT(key) DO UPDATE SET hash=excluded.hash,rev=excluded.rev", key, hash, rev)
	return err
}
