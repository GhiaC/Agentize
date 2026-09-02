package browseruse

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	defaultMaxResponseBytes   = int64(2 << 20)
	defaultMaxScreenshotBytes = int64(10 << 20)
	defaultMaxDownloadBytes   = int64(25 << 20)
	sessionHeader             = "X-Agentize-Session-ID"
)

// Config configures the HTTP sidecar client.
type Config struct {
	BaseURL            string
	Token              string
	HTTPClient         *http.Client
	MaxResponseBytes   int64
	MaxScreenshotBytes int64
	MaxDownloadBytes   int64
}

// Client is an HTTP implementation of Service.
type Client struct {
	baseURL            *url.URL
	token              string
	httpClient         *http.Client
	maxResponseBytes   int64
	maxScreenshotBytes int64
	maxDownloadBytes   int64
}

var (
	_ Service           = (*Client)(nil)
	_ ScreenshotService = (*Client)(nil)
	_ DownloadService   = (*Client)(nil)
	_ TabService        = (*Client)(nil)
	_ LiveTabService    = (*Client)(nil)
	_ DebugService      = (*Client)(nil)
	_ AdminDebugService = (*Client)(nil)
)

// APIError is returned for a non-2xx response from the sidecar.
type APIError struct {
	StatusCode int
	Message    string
}

func (e *APIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("browser-use service returned HTTP %d", e.StatusCode)
	}
	return fmt.Sprintf("browser-use service returned HTTP %d: %s", e.StatusCode, e.Message)
}

// Busy reports a sidecar 503 that means the Chromium session is already owned
// by an autonomous job. Callers should retry with status, not start another run.
func (e *APIError) Busy() bool {
	if e == nil || e.StatusCode != http.StatusServiceUnavailable {
		return false
	}
	return strings.Contains(strings.ToLower(e.Message), "busy")
}

// IsBusy reports whether err is a sidecar session-busy rejection.
func IsBusy(err error) bool {
	var api *APIError
	return errors.As(err, &api) && api.Busy()
}

// NewClient validates config and creates a sidecar client.
func NewClient(config Config) (*Client, error) {
	baseURL, err := url.Parse(strings.TrimSpace(config.BaseURL))
	if err != nil {
		return nil, fmt.Errorf("parse browser-use base URL: %w", err)
	}
	if baseURL.Scheme != "http" && baseURL.Scheme != "https" {
		return nil, errors.New("browser-use base URL must use http or https")
	}
	if baseURL.Host == "" {
		return nil, errors.New("browser-use base URL must include a host")
	}
	baseURL.RawQuery = ""
	baseURL.Fragment = ""
	baseURL.Path = strings.TrimRight(baseURL.Path, "/")

	token := strings.TrimSpace(config.Token)
	if token == "" {
		return nil, errors.New("browser-use sidecar token is required")
	}
	httpClient := config.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 2 * time.Minute}
	}
	maxResponseBytes := config.MaxResponseBytes
	if maxResponseBytes <= 0 {
		maxResponseBytes = defaultMaxResponseBytes
	}
	maxScreenshotBytes := config.MaxScreenshotBytes
	if maxScreenshotBytes <= 0 {
		maxScreenshotBytes = defaultMaxScreenshotBytes
	}
	maxDownloadBytes := config.MaxDownloadBytes
	if maxDownloadBytes <= 0 {
		maxDownloadBytes = defaultMaxDownloadBytes
	}

	return &Client{
		baseURL:            baseURL,
		token:              token,
		httpClient:         httpClient,
		maxResponseBytes:   maxResponseBytes,
		maxScreenshotBytes: maxScreenshotBytes,
		maxDownloadBytes:   maxDownloadBytes,
	}, nil
}

// Health checks whether the sidecar HTTP process is ready.
func (c *Client) Health(ctx context.Context) error {
	return c.do(ctx, http.MethodGet, "/health", "", nil, nil)
}

// Start creates an asynchronous browser job.
func (c *Client) Start(ctx context.Context, sessionID string, request StartJobRequest) (*Job, error) {
	if strings.TrimSpace(request.Task) == "" {
		return nil, errors.New("browser-use task is required")
	}
	var job Job
	if err := c.do(ctx, http.MethodPost, "/v1/jobs", sessionID, request, &job); err != nil {
		return nil, err
	}
	return &job, nil
}

// Get returns a job snapshot. wait enables bounded server-side long polling.
func (c *Client) Get(ctx context.Context, sessionID, jobID string, wait time.Duration) (*Job, error) {
	path, err := jobPath(jobID)
	if err != nil {
		return nil, err
	}
	if wait < 0 {
		return nil, errors.New("browser-use wait duration cannot be negative")
	}
	if wait > 60*time.Second {
		wait = 60 * time.Second
	}
	if wait > 0 {
		path += "?wait_seconds=" + strconv.FormatFloat(wait.Seconds(), 'f', 3, 64)
	}
	var job Job
	if err := c.do(ctx, http.MethodGet, path, sessionID, nil, &job); err != nil {
		return nil, err
	}
	return &job, nil
}

// Cancel requests cancellation and returns the resulting job snapshot.
func (c *Client) Cancel(ctx context.Context, sessionID, jobID string) (*Job, error) {
	path, err := jobPath(jobID)
	if err != nil {
		return nil, err
	}
	var job Job
	if err := c.do(ctx, http.MethodPost, path+"/cancel", sessionID, nil, &job); err != nil {
		return nil, err
	}
	return &job, nil
}

// Screenshot returns the latest PNG captured for a browser job.
func (c *Client) Screenshot(ctx context.Context, sessionID, jobID string) (*Screenshot, error) {
	path, err := jobPath(jobID)
	if err != nil {
		return nil, err
	}
	payload, contentType, err := c.doBytes(
		ctx,
		http.MethodGet,
		path+"/screenshot",
		sessionID,
		c.maxScreenshotBytes,
		"image/png",
		"browser-use screenshot",
	)
	if err != nil {
		return nil, err
	}
	if contentType == "" {
		contentType = "image/png"
	}
	return &Screenshot{
		Data:     payload,
		Name:     "browser-" + jobID + ".png",
		MIMEType: contentType,
	}, nil
}

// Downloads lists files downloaded by a browser job without returning their
// contents. Use Download to retrieve one selected file.
func (c *Client) Downloads(ctx context.Context, sessionID, jobID string) ([]Download, error) {
	path, err := jobPath(jobID)
	if err != nil {
		return nil, err
	}
	var response struct {
		Files []Download `json:"files"`
	}
	if err := c.do(ctx, http.MethodGet, path+"/downloads", sessionID, nil, &response); err != nil {
		return nil, err
	}
	return response.Files, nil
}

// Download retrieves one browser-job download. The name must come from the
// preceding Downloads response, avoiding paths controlled by the model.
func (c *Client) Download(ctx context.Context, sessionID, jobID, name string) (*DownloadFile, error) {
	path, err := jobPath(jobID)
	if err != nil {
		return nil, err
	}
	name, err = downloadName(name)
	if err != nil {
		return nil, err
	}
	payload, contentType, err := c.doBytes(
		ctx,
		http.MethodGet,
		path+"/downloads/"+url.PathEscape(name),
		sessionID,
		c.maxDownloadBytes,
		"application/octet-stream",
		"browser-use download",
	)
	if err != nil {
		return nil, err
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	return &DownloadFile{Download: Download{Name: name, MIMEType: contentType, Size: int64(len(payload))}, Data: payload}, nil
}

// Tabs returns the current tabs for a persistent browser session.
func (c *Client) Tabs(ctx context.Context, sessionID string) ([]BrowserTab, error) {
	var response struct {
		Tabs []BrowserTab `json:"tabs"`
	}
	if err := c.do(ctx, http.MethodGet, "/v1/tabs", sessionID, nil, &response); err != nil {
		return nil, err
	}
	return response.Tabs, nil
}

// OpenTab opens a web URL directly in a persistent browser session without an
// autonomous browser task or LLM call.
func (c *Client) OpenTab(ctx context.Context, sessionID, rawURL string) ([]BrowserTab, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return nil, errors.New("browser-use URL is required")
	}
	var response struct {
		Tabs []BrowserTab `json:"tabs"`
	}
	if err := c.do(ctx, http.MethodPost, "/v1/tabs/open", sessionID, map[string]string{"url": rawURL}, &response); err != nil {
		return nil, err
	}
	return response.Tabs, nil
}

// TabScreenshot returns a fresh PNG of one persistent browser tab.
func (c *Client) TabScreenshot(ctx context.Context, sessionID, tabID string) (*Screenshot, error) {
	tabID, err := tabPath(tabID)
	if err != nil {
		return nil, err
	}
	payload, contentType, err := c.doBytes(
		ctx,
		http.MethodGet,
		"/v1/tabs/"+url.PathEscape(tabID)+"/screenshot",
		sessionID,
		c.maxScreenshotBytes,
		"image/png",
		"browser-use tab screenshot",
	)
	if err != nil {
		return nil, err
	}
	if contentType == "" {
		contentType = "image/png"
	}
	return &Screenshot{Data: payload, Name: "browser-tab-" + tabID + ".png", MIMEType: contentType}, nil
}

// CloseTab closes one tab in a persistent browser session and returns the
// remaining tab snapshot.
func (c *Client) CloseTab(ctx context.Context, sessionID, tabID string) ([]BrowserTab, error) {
	tabID, err := tabPath(tabID)
	if err != nil {
		return nil, err
	}
	var response struct {
		Tabs []BrowserTab `json:"tabs"`
	}
	if err := c.do(ctx, http.MethodPost, "/v1/tabs/"+url.PathEscape(tabID)+"/close", sessionID, nil, &response); err != nil {
		return nil, err
	}
	return response.Tabs, nil
}

// Debug returns recent browser jobs and bounded network-load metadata for the
// protected Agentize debugger.
func (c *Client) Debug(ctx context.Context, jobLimit, loadLimit int) (*DebugSnapshot, error) {
	if jobLimit < 1 {
		jobLimit = 20
	}
	if jobLimit > 100 {
		jobLimit = 100
	}
	if loadLimit < 0 {
		loadLimit = 0
	}
	if loadLimit > 250 {
		loadLimit = 250
	}
	path := "/v1/debug/jobs?limit=" + strconv.Itoa(jobLimit) +
		"&load_limit=" + strconv.Itoa(loadLimit) +
		"&session_limit=50"
	var snapshot DebugSnapshot
	if err := c.do(ctx, http.MethodGet, path, "", nil, &snapshot); err != nil {
		return nil, err
	}
	return &snapshot, nil
}

// JobLogs returns persisted debug lines for one browser job.
func (c *Client) JobLogs(ctx context.Context, jobID string, limit int) (*JobLogs, error) {
	if limit < 1 {
		limit = 200
	}
	if limit > 500 {
		limit = 500
	}
	path, err := debugJobPath(jobID)
	if err != nil {
		return nil, err
	}
	var logs JobLogs
	if err := c.do(ctx, http.MethodGet, path+"/logs?limit="+strconv.Itoa(limit), "", nil, &logs); err != nil {
		return nil, err
	}
	return &logs, nil
}

// AdminCancel cancels a browser job from the operator debugger.
func (c *Client) AdminCancel(ctx context.Context, jobID string) (*Job, error) {
	path, err := debugJobPath(jobID)
	if err != nil {
		return nil, err
	}
	var job Job
	if err := c.do(ctx, http.MethodPost, path+"/cancel", "", nil, &job); err != nil {
		return nil, err
	}
	return &job, nil
}

// KillSession kills the persistent Chromium profile for one Agentize session.
func (c *Client) KillSession(ctx context.Context, sessionID string) (*DebugSession, error) {
	if strings.TrimSpace(sessionID) == "" {
		return nil, errors.New("browser-use session ID is required")
	}
	var session DebugSession
	if err := c.do(
		ctx,
		http.MethodPost,
		"/v1/debug/sessions/"+url.PathEscape(sessionID)+"/kill",
		"",
		nil,
		&session,
	); err != nil {
		return nil, err
	}
	return &session, nil
}

// AdminCloseTab closes one tab from the operator debugger.
func (c *Client) AdminCloseTab(ctx context.Context, sessionID, tabID string) ([]BrowserTab, error) {
	tabID, err := tabPath(tabID)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(sessionID) == "" {
		return nil, errors.New("browser-use session ID is required")
	}
	var response struct {
		Tabs []BrowserTab `json:"tabs"`
	}
	if err := c.do(
		ctx,
		http.MethodPost,
		"/v1/debug/sessions/"+url.PathEscape(sessionID)+"/tabs/"+url.PathEscape(tabID)+"/close",
		"",
		nil,
		&response,
	); err != nil {
		return nil, err
	}
	return response.Tabs, nil
}

func jobPath(jobID string) (string, error) {
	jobID, err := validateJobID(jobID)
	if err != nil {
		return "", err
	}
	return "/v1/jobs/" + jobID, nil
}

func debugJobPath(jobID string) (string, error) {
	jobID, err := validateJobID(jobID)
	if err != nil {
		return "", err
	}
	return "/v1/debug/jobs/" + jobID, nil
}

func validateJobID(jobID string) (string, error) {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return "", errors.New("browser-use job ID is required")
	}
	for _, char := range jobID {
		if (char < 'a' || char > 'z') &&
			(char < 'A' || char > 'Z') &&
			(char < '0' || char > '9') &&
			char != '-' && char != '_' {
			return "", errors.New("browser-use job ID contains invalid characters")
		}
	}
	return jobID, nil
}

func tabPath(tabID string) (string, error) {
	tabID = strings.TrimSpace(tabID)
	if tabID == "" {
		return "", errors.New("browser-use tab ID is required")
	}
	for _, char := range tabID {
		if (char < 'a' || char > 'z') &&
			(char < 'A' || char > 'Z') &&
			(char < '0' || char > '9') &&
			char != '-' && char != '_' {
			return "", errors.New("browser-use tab ID contains invalid characters")
		}
	}
	return tabID, nil
}

func downloadName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 255 || name == "." || name == ".." || strings.ContainsAny(name, `/\\`) {
		return "", errors.New("browser-use download name is invalid")
	}
	return name, nil
}

func (c *Client) do(
	ctx context.Context,
	method, path, sessionID string,
	requestBody interface{},
	responseBody interface{},
) error {
	var body io.Reader
	if requestBody != nil {
		encoded, err := json.Marshal(requestBody)
		if err != nil {
			return fmt.Errorf("encode browser-use request: %w", err)
		}
		body = bytes.NewReader(encoded)
	}

	endpoint := *c.baseURL
	endpoint.Path = strings.TrimRight(c.baseURL.Path, "/") + path
	if queryIndex := strings.IndexByte(path, '?'); queryIndex >= 0 {
		endpoint.Path = strings.TrimRight(c.baseURL.Path, "/") + path[:queryIndex]
		endpoint.RawQuery = path[queryIndex+1:]
	}
	httpRequest, err := http.NewRequestWithContext(ctx, method, endpoint.String(), body)
	if err != nil {
		return fmt.Errorf("create browser-use request: %w", err)
	}
	httpRequest.Header.Set("Accept", "application/json")
	httpRequest.Header.Set("Authorization", "Bearer "+c.token)
	if requestBody != nil {
		httpRequest.Header.Set("Content-Type", "application/json")
	}
	if sessionID != "" {
		httpRequest.Header.Set(sessionHeader, sessionID)
	}

	response, err := c.httpClient.Do(httpRequest)
	if err != nil {
		return fmt.Errorf("call browser-use service: %w", err)
	}
	defer response.Body.Close()

	limited := io.LimitReader(response.Body, c.maxResponseBytes+1)
	payload, err := io.ReadAll(limited)
	if err != nil {
		return fmt.Errorf("read browser-use response: %w", err)
	}
	if int64(len(payload)) > c.maxResponseBytes {
		return errors.New("browser-use response exceeded configured size limit")
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return decodeAPIError(response.StatusCode, payload)
	}
	if responseBody == nil || len(bytes.TrimSpace(payload)) == 0 {
		return nil
	}
	if err := json.Unmarshal(payload, responseBody); err != nil {
		return fmt.Errorf("decode browser-use response: %w", err)
	}
	return nil
}

func (c *Client) doBytes(
	ctx context.Context,
	method, path, sessionID string,
	maxBytes int64,
	accept, resourceName string,
) ([]byte, string, error) {
	endpoint := *c.baseURL
	endpoint.Path = strings.TrimRight(c.baseURL.Path, "/") + path
	request, err := http.NewRequestWithContext(ctx, method, endpoint.String(), nil)
	if err != nil {
		return nil, "", fmt.Errorf("create browser-use request: %w", err)
	}
	request.Header.Set("Accept", accept)
	request.Header.Set("Authorization", "Bearer "+c.token)
	if sessionID != "" {
		request.Header.Set(sessionHeader, sessionID)
	}

	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, "", fmt.Errorf("call browser-use service: %w", err)
	}
	defer response.Body.Close()

	limited := io.LimitReader(response.Body, maxBytes+1)
	payload, err := io.ReadAll(limited)
	if err != nil {
		return nil, "", fmt.Errorf("read browser-use response: %w", err)
	}
	if int64(len(payload)) > maxBytes {
		return nil, "", fmt.Errorf("%s exceeded configured size limit", resourceName)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, "", decodeAPIError(response.StatusCode, payload)
	}
	if len(payload) == 0 {
		return nil, "", fmt.Errorf("%s response was empty", resourceName)
	}
	contentType := strings.TrimSpace(strings.Split(response.Header.Get("Content-Type"), ";")[0])
	if accept == "image/png" && !strings.HasPrefix(contentType, "image/") {
		return nil, "", fmt.Errorf("browser-use screenshot returned unexpected content type %q", contentType)
	}
	return payload, contentType, nil
}

func decodeAPIError(statusCode int, payload []byte) error {
	var body struct {
		Detail interface{} `json:"detail"`
	}
	message := strings.TrimSpace(string(payload))
	if json.Unmarshal(payload, &body) == nil && body.Detail != nil {
		switch detail := body.Detail.(type) {
		case string:
			message = detail
		default:
			if encoded, err := json.Marshal(detail); err == nil {
				message = string(encoded)
			}
		}
	}
	if len(message) > 1000 {
		message = message[:1000] + "..."
	}
	return &APIError{StatusCode: statusCode, Message: message}
}
