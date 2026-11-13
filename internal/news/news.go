package news

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"
)

const maxBlockAge = 6 * time.Hour

var athens = func() *time.Location {
	loc, err := time.LoadLocation("Europe/Athens")
	if err != nil {
		return time.Local
	}
	return loc
}()

// Item is one speech-ready news item from the dashboard's news block.
type Item struct {
	Topic       string   `json:"topic"`
	Category    string   `json:"category"`
	SummaryText string   `json:"summary_text"`
	Sources     []Source `json:"sources"`
}

// Source is one source article backing a news item.
type Source struct {
	Newspaper string `json:"newspaper"`
	Title     string `json:"title"`
	URL       string `json:"url"`
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
	return &Client{
		baseURL: baseURL,
		user:    user,
		pass:    pass,
		client:  &http.Client{Timeout: 10 * time.Minute},
	}
}

// Fetch returns the news block items for the given slot. If the stored block is
// missing (404) or stale (older than 6h or not generated today in Athens), it
// attempts one synchronous POST /api/briefing-news/generate call to recover.
func (c *Client) Fetch(ctx context.Context, slot string) ([]Item, error) {
	items, err := c.fetchOnce(ctx, http.MethodGet, "/api/briefing-news?slot="+slot)
	if err == nil {
		return items, nil
	}
	log.Printf("[news] GET block for %s unavailable/stale (%v); triggering on-demand generation", slot, err)
	return c.fetchOnce(ctx, http.MethodPost, "/api/briefing-news/generate?slot="+slot)
}

func (c *Client) fetchOnce(ctx context.Context, method, path string) ([]Item, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, nil)
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
	if err := validateBlock(block, time.Now()); err != nil {
		return nil, err
	}
	return block.Items, nil
}

func validateBlock(block Block, now time.Time) error {
	if len(block.Items) == 0 {
		return fmt.Errorf("news block is empty")
	}
	if block.Generated == "" {
		return fmt.Errorf("news block missing generated timestamp")
	}
	genTime, err := time.Parse(time.RFC3339, block.Generated)
	if err != nil {
		return fmt.Errorf("invalid news block timestamp %q: %w", block.Generated, err)
	}
	nowAthens := now.In(athens)
	genAthens := genTime.In(athens)
	if genAthens.Format("2006-01-02") != nowAthens.Format("2006-01-02") {
		return fmt.Errorf("stale news block from %s (today is %s)", genAthens.Format("2006-01-02"), nowAthens.Format("2006-01-02"))
	}
	if age := nowAthens.Sub(genAthens); age > maxBlockAge {
		return fmt.Errorf("stale news block (age %s > %s)", age.Round(time.Minute), maxBlockAge)
	}
	return nil
}
