package news

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Item is one speech-ready news item from the dashboard's news block.
type Item struct {
	Topic       string `json:"topic"`
	Category    string `json:"category"`
	SummaryText string `json:"summary_text"`
}

// Block mirrors the dashboard's stored news-block envelope.
type Block struct {
	Slot      string `json:"slot"`
	Generated string `json:"generated"`
	Items     []Item `json:"items"`
}

// Client fetches the pre-generated news block from the daily-news dashboard.
type Client struct {
	baseURL string
	user    string
	pass    string
	client  *http.Client
}

func NewClient(baseURL, user, pass string) *Client {
	return &Client{baseURL: baseURL, user: user, pass: pass, client: &http.Client{Timeout: 90 * time.Second}}
}

// Fetch returns the news block items for the given slot.
func (c *Client) Fetch(ctx context.Context, slot string) ([]Item, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+"/api/briefing-news?slot="+slot, nil)
	if err != nil {
		return nil, err
	}
	if c.user != "" {
		req.SetBasicAuth(c.user, c.pass)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("news block status %d: %s", resp.StatusCode, string(body))
	}
	var block Block
	if err := json.NewDecoder(resp.Body).Decode(&block); err != nil {
		return nil, err
	}
	return block.Items, nil
}
