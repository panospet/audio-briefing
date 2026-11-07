package tts

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client synthesizes speech via the Python voice-service (edge-tts, reliable).
type Client struct {
	baseURL    string
	user, pass string
	client     *http.Client
}

func NewClient(baseURL, user, pass string) *Client {
	return &Client{
		baseURL: baseURL,
		user:    user,
		pass:    pass,
		client:  &http.Client{Timeout: 10 * time.Minute},
	}
}

// Synthesize turns text into MP3 audio bytes using the given voice.
// Chunking, XML-escaping, and edge-tts are handled by the voice-service.
func (c *Client) Synthesize(ctx context.Context, text, voice string) ([]byte, error) {
	reqBody, err := json.Marshal(map[string]string{
		"type":  "text",
		"text":  text,
		"voice": voice,
	})
	if err != nil {
		return nil, err
	}

	var lastErr error
	for attempt := 1; attempt <= 2; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/synthesize", bytes.NewReader(reqBody))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		if c.user != "" {
			req.SetBasicAuth(c.user, c.pass)
		}
		resp, err := c.client.Do(req)
		if err != nil {
			lastErr = err
		} else if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
			resp.Body.Close()
			lastErr = fmt.Errorf("voice-service status %d: %s", resp.StatusCode, string(body))
		} else {
			audio, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil {
				lastErr = err
			} else {
				return audio, nil
			}
		}
		if attempt < 2 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(2 * time.Second):
			}
		}
	}
	return nil, lastErr
}
