// Package ee71api implements the plain (unencrypted) JSON-RPC variant used by
// the EE71 stock firmware. It shares the login primitives with tclapi; the
// difference is that parameters and results travel as ordinary JSON.
package ee71api

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/rctphone/ee-setup/internal/tclapi"
)

// CookiePrefix prefixes the cookie value the web UI expects.
const CookiePrefix = "1D4B9765B16C3A64AD97489B1610498B"

// ErrAuth means the password was rejected.
var ErrAuth = errors.New("ee71api: authentication failed")

// Client talks to an EE71 web API.
type Client struct {
	Host     string
	Password string
	HTTP     *http.Client

	Token string // value of the _TclRequestVerificationToken header
	seq   int
}

// New returns a client for the router at host.
func New(host, password string) *Client {
	return &Client{
		Host:     host,
		Password: password,
		HTTP:     &http.Client{Timeout: 10 * time.Second},
	}
}

type response struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (c *Client) post(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.seq++
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
		"id":      fmt.Sprintf("%d", c.seq),
	})
	if err != nil {
		return nil, err
	}
	url := fmt.Sprintf("http://%s/jrd/webapi", c.Host)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("_TclRequestVerificationKey", tclapi.VerificationKey)
	req.Header.Set("Referer", "http://"+c.Host+"/index.html")
	if c.Token != "" {
		req.Header.Set("_TclRequestVerificationToken", c.Token)
		req.Header.Set("Cookie", "t="+CookiePrefix+c.Token)
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ee71api: %s: %w", method, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ee71api: %s: HTTP %d", method, resp.StatusCode)
	}
	var rr response
	if err := json.Unmarshal(raw, &rr); err != nil {
		return nil, fmt.Errorf("ee71api: %s: non-JSON response", method)
	}
	if rr.Error != nil {
		return nil, fmt.Errorf("ee71api: %s: [%d] %s", method, rr.Error.Code, rr.Error.Message)
	}
	return rr.Result, nil
}

// Salt returns the persistent login salt.
func (c *Client) Salt(ctx context.Context) (string, error) {
	raw, err := c.post(ctx, "GetDeviceSt", struct{}{})
	if err != nil {
		return "", err
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil && s != "" {
		return s, nil
	}
	var obj struct {
		Salt string `json:"Salt"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil || obj.Salt == "" {
		return "", errors.New("ee71api: no salt in GetDeviceSt")
	}
	return obj.Salt, nil
}

// Login authenticates and stores the verification token.
func (c *Client) Login(ctx context.Context) error {
	salt, err := c.Salt(ctx)
	if err != nil {
		return err
	}
	hash := tclapi.PBKDF2Password(c.Password, salt)
	raw, err := c.post(ctx, "Login", map[string]any{
		"UserName": tclapi.XOREncrypt("admin"),
		"Password": hash,
	})
	if err != nil {
		return fmt.Errorf("%w: %v", ErrAuth, err)
	}
	var lr struct {
		Token  string `json:"token"`
		Param0 string `json:"param0"`
		Param1 string `json:"param1"`
	}
	if err := json.Unmarshal(raw, &lr); err != nil {
		return fmt.Errorf("ee71api: Login: %w", err)
	}
	if lr.Token == "" {
		return ErrAuth
	}
	tok, err := encryptToken(lr.Token, lr.Param0, lr.Param1)
	if err != nil {
		return err
	}
	c.Token = tok
	return nil
}

// Call invokes an authenticated method.
func (c *Client) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	if c.Token == "" {
		if err := c.Login(ctx); err != nil {
			return nil, err
		}
	}
	return c.post(ctx, method, params)
}

// encryptToken reproduces the web UI's encrypt_c(): XOR-obfuscate the token,
// AES-128-CBC it with param0/param1 (zero padded to 16 bytes) and base64 it.
func encryptToken(token, key16, iv16 string) (string, error) {
	obf := tclapi.XORBytes(token)
	pad := 16 - len(obf)%16
	obf = append(obf, bytes.Repeat([]byte{byte(pad)}, pad)...)
	key := make([]byte, 16)
	iv := make([]byte, 16)
	copy(key, key16)
	copy(iv, iv16)
	return aesEncrypt(obf, key, iv)
}

func aesEncrypt(data, key, iv []byte) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	out := make([]byte, len(data))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(out, data)
	return base64.StdEncoding.EncodeToString(out), nil
}
