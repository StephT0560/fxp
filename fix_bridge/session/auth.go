package session

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"strings"
)

// Authenticator decides whether a Logon from senderCompID may proceed.
type Authenticator interface {
	Authenticate(senderCompID, password string) error
}

var errAuthFailed = errors.New("authentication failed")

// CredentialAuth is an allow-list of CompIDs, each with its Password(554).
type CredentialAuth struct {
	creds map[string]string
}

func NewCredentialAuth(creds map[string]string) *CredentialAuth {
	return &CredentialAuth{creds: creds}
}

func (a *CredentialAuth) Authenticate(senderCompID, password string) error {
	want, ok := a.creds[senderCompID]
	if !ok || subtle.ConstantTimeCompare([]byte(want), []byte(password)) != 1 {
		return errAuthFailed
	}
	return nil
}

// ParseCredentials parses "COMPID:password,COMPID2:password2".
func ParseCredentials(spec string) (map[string]string, error) {
	creds := make(map[string]string)
	for _, entry := range strings.Split(spec, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		compID, password, ok := strings.Cut(entry, ":")
		if !ok || compID == "" || password == "" {
			return nil, fmt.Errorf("invalid credential entry %q (want COMPID:password)", entry)
		}
		creds[compID] = password
	}
	if len(creds) == 0 {
		return nil, errors.New("no credentials configured")
	}
	return creds, nil
}
