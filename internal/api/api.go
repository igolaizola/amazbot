package api

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/PuerkitoBio/goquery"
)

type Item struct {
	ID            string    `json:"id"`
	Link          string    `json:"link"`
	Title         string    `json:"title"`
	Price         float64   `json:"price"`
	PreviousPrice float64   `json:"previous_price"`
	CreatedAt     time.Time `json:"created_at"`
}

type Client struct {
	client *http.Client
	ctx    context.Context
}

func New(ctx context.Context) *Client {
	cookieJar, err := cookiejar.New(nil)
	if err != nil {
		panic("could not create cookie jar")
		//return nil, fmt.Errorf("could not create cookie jar: %w", err)
	}
	cli := &Client{
		ctx: ctx,
		client: &http.Client{
			Timeout: 10 * time.Second,
			Transport: &transport{
				ctx: ctx,
			},
			Jar: cookieJar,
		},
	}
	cli.client.Get("https://www.amazon.es")
	return cli
}

func ItemID(link string) (string, bool) {
	u, err := url.Parse(link)
	if err != nil {
		return "", false
	}
	if !strings.Contains(u.Host, "amazon.es") {
		return "", false
	}
	split := strings.Split(u.Path, "/")
	var prev string
	for _, s := range split {
		if prev == "dp" {
			return s, true
		}
		prev = s
	}
	return "", false
}

func (c *Client) Search(id string, item *Item, callback func(Item) error) error {
	for {
		select {
		case <-c.ctx.Done():
			return nil
		default:
		}
		err := c.search(id, item, callback)
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			continue
		}
		if errors.Is(err, errBadGateway) {
			continue
		}
		return err
	}
}

var errBadGateway = errors.New("api: 502 bad gateway")

func (c *Client) search(id string, item *Item, callback func(Item) error) error {
	u := fmt.Sprintf("https://www.amazon.es/dp/%s", id)
	r, err := c.client.Get(u)
	if err != nil {
		return fmt.Errorf("api: get request failed: %w", err)
	}
	if r.StatusCode == 502 {
		return errBadGateway
	}
	if r.StatusCode != 200 {
		return fmt.Errorf("api: invalid status code: %s", r.Status)
	}
	defer r.Body.Close()

	doc, err := goquery.NewDocumentFromReader(r.Body)
	if err != nil {
		return err
	}

	// search price
	sel := doc.Find("#priceblock_ourprice").First()
	if sel == nil {
		return nil
	}
	text := strings.Replace(sel.Text(), ",", ".", 1)
	i := strings.Index(text, ".")
	if i < 0 {
		return fmt.Errorf("api: price point not found: %s %s", id, text)
	}
	text = fmt.Sprintf("%s.%s", text[0:i], text[i+1:i+3])
	price, err := strconv.ParseFloat(text, 32)
	if err != nil {
		return fmt.Errorf("api: invalid price value: %s: %w", text, err)
	}

	// search title
	sel = doc.Find("#productTitle").First()
	if sel == nil {
		return nil
	}
	title := strings.Trim(sel.Text(), "\n")

	// search link
	var link string
	doc.Find("link").Each(func(i int, s *goquery.Selection) {
		rel, _ := s.Attr("rel")
		if rel != "canonical" {
			return
		}
		link, _ = s.Attr("href")
	})
	if link == "" {
		return fmt.Errorf("api: link not found: %s", id)
	}

	if item == nil {
		item = &Item{
			PreviousPrice: -1,
			CreatedAt:     time.Now().UTC(),
		}
	} else {
		item.PreviousPrice = item.Price
	}
	item.ID = id
	item.Link = link
	item.Title = title
	item.Price = price
	if item.Price < item.PreviousPrice {
		if err := callback(*item); err != nil {
			return err
		}
	}
	return nil
}

type transport struct {
	lock sync.Mutex
	ctx  context.Context
}

func (t *transport) RoundTrip(r *http.Request) (*http.Response, error) {
	r.Header.Set("Origin", "https://www.amazon.es")
	r.Header.Set("Referer", "https://www.amazon.es")
	r.Header.Set("Sec-Fetch-Dest", "empty")
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	r.Header.Set("Sec-Fetch-User", "?F")
	r.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/73.0.3683.86 Safari/537.36")
	r.Header.Add("Content-Type", "application/json")
	r.Header.Add("cache-control", "no-cache")

	t.lock.Lock()
	defer func() {
		select {
		case <-t.ctx.Done():
		case <-time.After(1000 * time.Millisecond):
		}
		t.lock.Unlock()
	}()
	return http.DefaultTransport.RoundTrip(r)
}
