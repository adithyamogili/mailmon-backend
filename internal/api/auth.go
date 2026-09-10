package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

type googleUserInfo struct {
	Sub     string `json:"sub"`
	Email   string `json:"email"`
	Name    string `json:"name"`
	Picture string `json:"picture"`
}

// verifyGoogleIDToken verifies a Google Sign-In ID token (JWT credential)
// using Google's tokeninfo endpoint and returns user info.
func verifyGoogleIDToken(idToken, expectedClientID string) (*googleUserInfo, error) {
	client := &http.Client{Timeout: 10 * time.Second}

	resp, err := client.Get(
		"https://oauth2.googleapis.com/tokeninfo?id_token=" + url.QueryEscape(idToken),
	)
	if err != nil {
		return nil, fmt.Errorf("verifyGoogleIDToken: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("verifyGoogleIDToken: status %d: %s", resp.StatusCode, string(body))
	}

	var claims struct {
		Sub     string `json:"sub"`
		Email   string `json:"email"`
		Name    string `json:"name"`
		Picture string `json:"picture"`
		Aud     string `json:"aud"`
	}
	if err := json.Unmarshal(body, &claims); err != nil {
		return nil, fmt.Errorf("verifyGoogleIDToken: unmarshal: %w", err)
	}

	if claims.Aud != expectedClientID {
		return nil, fmt.Errorf("verifyGoogleIDToken: audience mismatch: got %s, want %s", claims.Aud, expectedClientID)
	}

	return &googleUserInfo{
		Sub:     claims.Sub,
		Email:   claims.Email,
		Name:    claims.Name,
		Picture: claims.Picture,
	}, nil
}
