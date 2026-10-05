package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/zogami00/you-as-bee/internal/proto"
)

// Client is a typed standard-library HTTP client for the yabd management API.
// It is used by yabd's own CLI and is reusable by the Windows client.
type Client struct {
	// BaseURL is the API root, e.g. "http://pi.local:3241".
	BaseURL string
	// Token is the bearer token sent with every request.
	Token string
	// HTTP is the underlying client. When nil a client with Timeout is used.
	HTTP *http.Client
	// Timeout bounds each request. Defaults to 15s when zero.
	Timeout time.Duration
}

// APIError is a non-2xx response body.
type APIError struct {
	Status  int
	Code    string
	Message string
}

// Error implements error.
func (e *APIError) Error() string {
	return fmt.Sprintf("api: %s (%d): %s", e.Code, e.Status, e.Message)
}

// NewClient returns a Client for baseURL and token.
func NewClient(baseURL, token string, timeout time.Duration) *Client {
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), Token: token, Timeout: timeout}
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	return &http.Client{Timeout: timeout}
}

// Info fetches GET /v1/info.
func (c *Client) Info(ctx context.Context) (proto.Info, error) {
	var info proto.Info
	err := c.do(ctx, http.MethodGet, "/v1/info", nil, &info)
	return info, err
}

// Devices fetches GET /v1/devices.
func (c *Client) Devices(ctx context.Context) ([]proto.Device, error) {
	var resp proto.ListDevicesResponse
	if err := c.do(ctx, http.MethodGet, "/v1/devices", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Devices, nil
}

// Device fetches GET /v1/devices/{id}.
func (c *Client) Device(ctx context.Context, id string) (proto.Device, error) {
	var dev proto.Device
	err := c.do(ctx, http.MethodGet, "/v1/devices/"+url.PathEscape(id), nil, &dev)
	return dev, err
}

// Export posts POST /v1/devices/{id}/export.
func (c *Client) Export(ctx context.Context, id string, force bool) error {
	path := "/v1/devices/" + url.PathEscape(id) + "/export"
	if force {
		path += "?force=true"
	}
	return c.do(ctx, http.MethodPost, path, nil, nil)
}

// Unexport posts POST /v1/devices/{id}/unexport.
func (c *Client) Unexport(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/v1/devices/"+url.PathEscape(id)+"/unexport", nil, nil)
}

// Reset posts POST /v1/devices/{id}/reset.
func (c *Client) Reset(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/v1/devices/"+url.PathEscape(id)+"/reset", nil, nil)
}

// Events streams GET /v1/events as server-sent events. The returned channel is
// closed when ctx is cancelled or the stream ends.
func (c *Client) Events(ctx context.Context) (<-chan proto.Event, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/v1/events", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "text/event-stream")

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return nil, decodeError(resp)
	}

	out := make(chan proto.Event)
	go func() {
		defer close(out)
		defer resp.Body.Close()
		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var ev proto.Event
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev); err != nil {
				continue
			}
			select {
			case out <- ev:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

func (c *Client) do(ctx context.Context, method, path string, body io.Reader, dst any) error {
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return decodeError(resp)
	}
	if dst == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(dst)
}

func decodeError(resp *http.Response) error {
	var body proto.Error
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	_ = json.Unmarshal(bytes.TrimSpace(data), &body)
	if body.Code == "" {
		body.Code = "http_error"
	}
	if body.Message == "" {
		body.Message = strings.TrimSpace(string(data))
	}
	return &APIError{Status: resp.StatusCode, Code: body.Code, Message: body.Message}
}

// IsNotFound reports whether err is an APIError with status 404.
func IsNotFound(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Status == http.StatusNotFound
}
