package network

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"time"

	bolt "go.etcd.io/bbolt"
)

var oauthBucket = []byte("oauth")
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
	Client       *oauthClient `json:"client,omitempty"`
}

func oauthKey(secret string) []byte {
	hash := sha256.Sum256([]byte(secret))
	return []byte(hex.EncodeToString(hash[:]))
}

func openOAuth(home string) (*bolt.DB, error) {
	directory := filepath.Join(home, "network")
	if err := EnsurePrivateDirectory(directory); err != nil {
		return nil, err
	}
	db, err := bolt.Open(filepath.Join(directory, "oauth.db"), 0600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		return nil, err
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists(oauthBucket)
		return err
	}); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func oauthGet(tx *bolt.Tx, secret string) (oauthRecord, error) {
	var record oauthRecord
	if len(secret) == 0 || len(secret) > 256 {
		return record, errOAuthGrant
	}
	data := tx.Bucket(oauthBucket).Get(oauthKey(secret))
	if data == nil || json.Unmarshal(data, &record) != nil || record.Kind != "client" && !time.Now().Before(record.Expires) {
		return record, errOAuthGrant
	}
	return record, nil
}

func oauthPut(tx *bolt.Tx, secret string, record oauthRecord) error {
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return tx.Bucket(oauthBucket).Put(oauthKey(secret), data)
}

func oauthPrune(tx *bolt.Tx) error {
	cursor := tx.Bucket(oauthBucket).Cursor()
	count := 0
	for key, value := cursor.First(); key != nil; key, value = cursor.Next() {
		var record oauthRecord
		if json.Unmarshal(value, &record) != nil {
			return errors.New("invalid OAuth storage record")
		}
		if record.Kind != "client" && !time.Now().Before(record.Expires) {
			if err := cursor.Delete(); err != nil {
				return err
			}
		} else {
			count++
		}
	}
	if count >= 8192 {
		return errors.New("OAuth credential limit reached")
	}
	return nil
}

func oauthRevokeFamily(tx *bolt.Tx, family string) error {
	if family == "" {
		return nil
	}
	cursor := tx.Bucket(oauthBucket).Cursor()
	for key, value := cursor.First(); key != nil; key, value = cursor.Next() {
		var record oauthRecord
		if err := json.Unmarshal(value, &record); err != nil {
			return err
		}
		if record.Family == family {
			if err := cursor.Delete(); err != nil {
				return err
			}
		}
	}
	return nil
}
