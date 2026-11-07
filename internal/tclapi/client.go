package tclapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// APIError is a JSON-RPC error returned by the device.
type APIError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Method  string `json:"-"`
}

func (e *APIError) Error() string {
	return fmt.Sprintf("%s: [%d] %s", e.Method, e.Code, e.Message)
}

// ErrAuth means the device rejected the admin password.
var ErrAuth = errors.New("tclapi: authentication failed")

// Client talks to one router's JRD web API.
type Client struct {
	Host     string // address used in the URL and Origin/Referer
	VHost    string // Host header (4g.wifi on EE72)
	Password string
	HTTP     *http.Client

	Token string // session token, valid after Login
	SK    string // per-session parameter key

	seq int
}

// New returns a client for the router at host.
func New(host, password string) *Client {
	return &Client{
		Host:     host,
		VHost:    DefaultVHost,
		Password: password,
		HTTP:     &http.Client{Timeout: 10 * time.Second},
	}
}

type rpcRequest struct {
	Seq     int64  `json:"_"`
	ID      string `json:"id"`
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  string `json:"params"`
}

type rpcResponse struct {
	ID     string          `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// post sends one encrypted RPC call and returns the decrypted result.
func (c *Client) post(ctx context.Context, method string, params any, key string) (json.RawMessage, error) {
	c.seq++
	enc, err := Encrypt(params, key)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(rpcRequest{
		Seq:     time.Now().UnixMilli(),
		ID:      fmt.Sprintf("%d.0", c.seq),
		JSONRPC: "2.0",
		Method:  method,
		Params:  enc,
	})
	if err != nil {
		return nil, err
	}

	url := fmt.Sprintf("http://%s/jrd/webapi?name=%s", c.Host, method)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Host = c.VHost
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("_TclRequestVerificationKey", VerificationKey)
	req.Header.Set("device_name", DeviceName)
	req.Header.Set("custom_id", CustomID)
	req.Header.Set("Origin", "http://"+c.Host)
	req.Header.Set("Referer", "http://"+c.Host+"/")
	if c.Token != "" {
		req.Header.Set("_TclRequestVerificationToken", c.Token)
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("tclapi: %s: %w", method, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("tclapi: %s: HTTP %d", method, resp.StatusCode)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, fmt.Errorf("tclapi: %s: empty response", method)
	}

	var rr rpcResponse
	if err := json.Unmarshal(raw, &rr); err != nil {
		return nil, fmt.Errorf("tclapi: %s: non-JSON response: %w", method, err)
	}
	if rr.Error != nil {
		return nil, &APIError{Code: rr.Error.Code, Message: rr.Error.Message, Method: method}
	}
	if len(rr.Result) == 0 {
		return nil, fmt.Errorf("tclapi: %s: no result", method)
	}

	// result is a quoted base64 envelope.
	var envelope string
	if err := json.Unmarshal(rr.Result, &envelope); err != nil {
		return nil, fmt.Errorf("tclapi: %s: result is not a string: %w", method, err)
	}
	plain, err := Decrypt(envelope, key)
	if err != nil {
		return nil, fmt.Errorf("tclapi: %s: decrypt: %w", method, err)
	}
	return json.RawMessage(plain), nil
}

// Salt returns the persistent per-device login salt.
func (c *Client) Salt(ctx context.Context) (string, error) {
	raw, err := c.CallPublic(ctx, "GetDeviceSt", struct{}{})
	if err != nil {
		return "", err
	}
	var st struct {
		Salt string `json:"Salt"`
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		return "", err
	}
	return st.Salt, nil
}

// CallPublic invokes a method that does not require a session.
func (c *Client) CallPublic(ctx context.Context, method string, params any) (json.RawMessage, error) {
	return c.post(ctx, method, params, PublicKey)
}

// Call invokes an authenticated method. Login is performed on first use.
func (c *Client) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	if c.Token == "" {
		if err := c.Login(ctx); err != nil {
			return nil, err
		}
	}
	return c.post(ctx, method, params, c.SK)
}

// Login performs the salt + PBKDF2 + Login sequence and stores the session.
func (c *Client) Login(ctx context.Context) error {
	raw, err := c.CallPublic(ctx, "GetDeviceSt", struct{}{})
	if err != nil {
		return fmt.Errorf("tclapi: GetDeviceSt: %w", err)
	}
	var st struct {
		Salt string `json:"Salt"`
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		return fmt.Errorf("tclapi: GetDeviceSt: %w", err)
	}
	if st.Salt == "" {
		return errors.New("tclapi: GetDeviceSt returned no salt")
	}

	hash := PBKDF2Password(c.Password, st.Salt)
	raw, err = c.CallPublic(ctx, "Login", map[string]any{
		"UserName": XOREncrypt("admin"),
		"Password": hash,
	})
	if err != nil {
		return fmt.Errorf("%w: %v", ErrAuth, err)
	}
	var lr struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(raw, &lr); err != nil {
		return fmt.Errorf("tclapi: Login: %w", err)
	}
	if lr.Token == "" {
		return ErrAuth
	}
	c.Token = lr.Token
	c.SK = FormatStr(hash)
	return nil
}
