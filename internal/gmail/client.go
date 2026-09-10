package gmail

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"
	gapi "google.golang.org/api/gmail/v1"
	"google.golang.org/api/option"
)

type Email struct {
	ID          string
	Subject     string
	From        string
	BodyPreview string
	ReceivedAt  time.Time
}

type Client struct {
	svc *gapi.Service
}

// OnTokenRefresh is called when the OAuth token is auto-refreshed, so the caller can persist it.
type OnTokenRefresh func(newToken *oauth2.Token)

func NewClientFromToken(ctx context.Context, oauthCfg *oauth2.Config, tok *oauth2.Token, onRefresh OnTokenRefresh) (*Client, error) {
	tokenSource := oauthCfg.TokenSource(ctx, tok)
	svc, err := gapi.NewService(ctx, option.WithTokenSource(tokenSource))
	if err != nil {
		return nil, fmt.Errorf("gmail.NewClientFromToken: create service: %w", err)
	}
	// Check if token was refreshed during creation
	newTok, err := tokenSource.Token()
	if err == nil && newTok.AccessToken != tok.AccessToken && onRefresh != nil {
		onRefresh(newTok)
	}
	return &Client{svc: svc}, nil
}

func (c *Client) FetchSince(ctx context.Context, since time.Time) ([]Email, error) {
	query := fmt.Sprintf("after:%d", since.Unix())
	var allIDs []string
	pageToken := ""

	for {
		req := c.svc.Users.Messages.List("me").Q(query).MaxResults(100).Context(ctx)
		if pageToken != "" {
			req = req.PageToken(pageToken)
		}
		resp, err := req.Do()
		if err != nil {
			return nil, fmt.Errorf("gmail.FetchSince: list: %w", err)
		}
		for _, msg := range resp.Messages {
			allIDs = append(allIDs, msg.Id)
		}
		if resp.NextPageToken == "" {
			break
		}
		pageToken = resp.NextPageToken
	}

	if len(allIDs) == 0 {
		return nil, nil
	}

	const concurrency = 20
	sem := make(chan struct{}, concurrency)
	var mu sync.Mutex
	var emails []Email
	var fetchErr error

	var wg sync.WaitGroup
	for _, id := range allIDs {
		wg.Add(1)
		go func(msgID string) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()

			msg, err := c.svc.Users.Messages.Get("me", msgID).Format("full").Context(ctx).Do()
			if err != nil {
				mu.Lock()
				if fetchErr == nil {
					fetchErr = fmt.Errorf("gmail.FetchSince: get %s: %w", msgID, err)
				}
				mu.Unlock()
				return
			}

			email := parseMessage(msg)
			mu.Lock()
			emails = append(emails, email)
			mu.Unlock()
		}(id)
	}
	wg.Wait()

	if fetchErr != nil {
		return emails, fetchErr
	}
	return emails, nil
}

func parseMessage(msg *gapi.Message) Email {
	e := Email{
		ID:         msg.Id,
		ReceivedAt: time.Unix(msg.InternalDate/1000, 0),
	}
	for _, h := range msg.Payload.Headers {
		switch strings.ToLower(h.Name) {
		case "subject":
			e.Subject = h.Value
		case "from":
			e.From = h.Value
		}
	}
	e.BodyPreview = extractBodyPreview(msg.Payload, 400)
	return e
}

func extractBodyPreview(payload *gapi.MessagePart, maxLen int) string {
	if body := decodeBody(payload, maxLen); body != "" {
		return body
	}
	for _, part := range payload.Parts {
		if body := extractBodyPreview(part, maxLen); body != "" {
			return body
		}
	}
	return ""
}

func decodeBody(part *gapi.MessagePart, maxLen int) string {
	if part.Body == nil || part.Body.Data == "" {
		return ""
	}
	if !strings.HasPrefix(part.MimeType, "text/plain") && !strings.HasPrefix(part.MimeType, "text/html") {
		return ""
	}
	decoded, err := base64.URLEncoding.DecodeString(part.Body.Data)
	if err != nil {
		return ""
	}
	text := string(decoded)
	if strings.HasPrefix(part.MimeType, "text/html") {
		text = stripHTML(text)
	}
	text = strings.Join(strings.Fields(text), " ")
	if len(text) > maxLen {
		text = text[:maxLen]
	}
	return text
}

func stripHTML(s string) string {
	var b strings.Builder
	inTag := false
	for _, r := range s {
		if r == '<' {
			inTag = true
			continue
		}
		if r == '>' {
			inTag = false
			b.WriteByte(' ')
			continue
		}
		if !inTag {
			b.WriteRune(r)
		}
	}
	return b.String()
}
