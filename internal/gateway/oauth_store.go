package gateway

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
)

var errOAuthGrant = errors.New("invalid OAuth grant")

type oauthClient struct {
	RedirectURIs  []string `json:"redirect_uris"`
	ClientName    string   `json:"client_name,omitempty"`
	TokenAuth     string   `json:"token_endpoint_auth_method,omitempty"`
	GrantTypes    []string `json:"grant_types,omitempty"`
	ResponseTypes []string `json:"response_types,omitempty"`
}

type oauthRecord struct {
	Kind         string       `json:"kind"`
	Expires      time.Time    `json:"expires"`
	ClientID     string       `json:"client_id,omitempty"`
	Redirect     string       `json:"redirect,omitempty"`
	Challenge    string       `json:"challenge,omitempty"`
	Resource     string       `json:"resource,omitempty"`
	Family       string       `json:"family,omitempty"`
	GrantExpires time.Time    `json:"grant_expires,omitempty"`
	Query        string       `json:"query,omitempty"`
	Session      string       `json:"session,omitempty"`
	Email        string       `json:"email,omitempty"`
	SessionEpoch int64        `json:"session_epoch,omitempty"`
	Client       *oauthClient `json:"client,omitempty"`
}

func oauthKey(secret string) []byte {
	hash := sha256.Sum256([]byte(secret))
	return []byte(hex.EncodeToString(hash[:]))
}

func oauthGet(tx *sql.Tx, secret string) (oauthRecord, error) {
	var record oauthRecord
	if len(secret) == 0 || len(secret) > 256 {
		return record, errOAuthGrant
	}
	key := string(oauthKey(secret))
	var expires, grantExpires int64
	err := tx.QueryRow(`SELECT kind, expires_at, client_id, redirect_uri, challenge, resource, family, grant_expires_at, query, session, email, session_epoch FROM oauth_tokens WHERE secret_hash = ?`, key).Scan(&record.Kind, &expires, &record.ClientID, &record.Redirect, &record.Challenge, &record.Resource, &record.Family, &grantExpires, &record.Query, &record.Session, &record.Email, &record.SessionEpoch)
	if err == nil {
		record.Expires = time.UnixMilli(expires)
		if grantExpires != 0 {
			record.GrantExpires = time.UnixMilli(grantExpires)
		}
		if !time.Now().Before(record.Expires) {
			return record, errOAuthGrant
		}
		return record, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return record, err
	}
	var data []byte
	if err := tx.QueryRow("SELECT metadata FROM oauth_clients WHERE secret_hash = ?", key).Scan(&data); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return record, errOAuthGrant
		}
		return record, err
	}
	record.Kind = "client"
	if json.Unmarshal(data, &record.Client) != nil || record.Client == nil {
		return record, errOAuthGrant
	}
	return record, nil
}

func oauthPut(tx *sql.Tx, secret string, record oauthRecord) error {
	key := string(oauthKey(secret))
	if record.Kind == "client" {
		data, err := json.Marshal(record.Client)
		if err != nil {
			return err
		}
		_, err = tx.Exec("INSERT INTO oauth_clients(secret_hash, metadata) VALUES (?, ?) ON CONFLICT(secret_hash) DO UPDATE SET metadata = excluded.metadata", key, string(data))
		return err
	}
	var grantExpires int64
	if !record.GrantExpires.IsZero() {
		grantExpires = record.GrantExpires.UnixMilli()
	}
	_, err := tx.Exec(`INSERT INTO oauth_tokens(secret_hash, kind, expires_at, client_id, redirect_uri, challenge, resource, family, grant_expires_at, query, session, email, session_epoch)
 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
 ON CONFLICT(secret_hash) DO UPDATE SET kind=excluded.kind, expires_at=excluded.expires_at, client_id=excluded.client_id, redirect_uri=excluded.redirect_uri, challenge=excluded.challenge, resource=excluded.resource, family=excluded.family, grant_expires_at=excluded.grant_expires_at, query=excluded.query, session=excluded.session, email=excluded.email, session_epoch=excluded.session_epoch`,
		key, record.Kind, record.Expires.UnixMilli(), record.ClientID, record.Redirect, record.Challenge, record.Resource, record.Family, grantExpires, record.Query, record.Session, record.Email, record.SessionEpoch)
	return err
}

func oauthPrune(tx *sql.Tx) error {
	if _, err := tx.Exec("DELETE FROM oauth_tokens WHERE expires_at <= ?", time.Now().UnixMilli()); err != nil {
		return err
	}
	var count int
	if err := tx.QueryRow("SELECT (SELECT count(*) FROM oauth_clients) + (SELECT count(*) FROM oauth_tokens)").Scan(&count); err != nil {
		return err
	}
	if count >= 8192 {
		return errors.New("OAuth credential limit reached")
	}
	return nil
}

func oauthAccountValid(tx *sql.Tx, record oauthRecord) error {
	var valid bool
	if err := tx.QueryRow("SELECT EXISTS(SELECT 1 FROM users WHERE email=? AND disabled=0 AND sessions_valid_after=?)", record.Email, record.SessionEpoch).Scan(&valid); err != nil {
		return err
	}
	if !valid {
		return errOAuthGrant
	}
	return nil
}

func oauthRevokeFamily(tx *sql.Tx, family string) error {
	if family == "" {
		return nil
	}
	_, err := tx.Exec("DELETE FROM oauth_tokens WHERE family = ?", family)
	return err
}
