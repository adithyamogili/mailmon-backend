package gmail

import (
	"encoding/json"
	"fmt"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	gapi "google.golang.org/api/gmail/v1"
)

func NewOAuthConfig(clientID, clientSecret, redirectURL string) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Endpoint:     google.Endpoint,
		Scopes:       []string{gapi.GmailReadonlyScope},
		RedirectURL:  redirectURL,
	}
}

func TokenToJSON(tok *oauth2.Token) (string, error) {
	data, err := json.Marshal(tok)
	if err != nil {
		return "", fmt.Errorf("gmail.TokenToJSON: %w", err)
	}
	return string(data), nil
}

func TokenFromJSON(s string) (*oauth2.Token, error) {
	var tok oauth2.Token
	if err := json.Unmarshal([]byte(s), &tok); err != nil {
		return nil, fmt.Errorf("gmail.TokenFromJSON: %w", err)
	}
	return &tok, nil
}
