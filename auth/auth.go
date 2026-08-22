// Package auth implements Qoder gateway authentication, encryption, and HTTP client.
package auth

import (
	"bufio"
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

// --- Region Configuration ---

type RegionConfig struct {
	Name     string
	AuthBase string
	ChatBase string
}

var CN = &RegionConfig{
	Name:     "cn",
	AuthBase: "https://gateway.qoder.com.cn",
	ChatBase: "https://gateway.qoder.com.cn",
}

func Resolve(pat string) (string, *RegionConfig) {
	return pat, CN
}

func AuthURL(region *RegionConfig, path string) string {
	return region.AuthBase + path
}

func ChatURL(region *RegionConfig) string {
	return region.ChatBase + "/algo/api/v2/service/pro/sse/agent_chat_generation?FetchKeys=llm_model_result&AgentId=agent_common&Encode=1"
}

func ModelListURL(region *RegionConfig) string {
	return region.ChatBase + "/algo/api/v2/model/list?Encode=1"
}

func FetchModelCatalog(ctx context.Context, sess *SessionContext, region *RegionConfig) (map[string]interface{}, error) {
	return CallGet(ctx, sess, ModelListURL(region))
}

// --- Custom Encoding (Qoder Base64 variant) ---

const customAlphabet = "_doRTgHZBKcGVjlvpC,@aFSx#DPuNJme&i*MzLOEn)sUrthbf%Y^w.(kIQyXqWA!"
const stdAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"

var s2c [128]byte
var c2s [128]byte
var s2cOK [128]bool
var c2sOK [128]bool

func init() {
	for i := 0; i < 64; i++ {
		s := stdAlphabet[i]
		c := customAlphabet[i]
		s2c[s] = c
		s2cOK[s] = true
		c2s[c] = s
		c2sOK[c] = true
	}
	s2c['='] = '$'
	s2cOK['='] = true
	c2s['$'] = '='
	c2sOK['$'] = true
}

func Encode(plaintext []byte) string {
	std := base64.StdEncoding.EncodeToString(plaintext)
	n := len(std)
	a := n / 3
	rearranged := make([]byte, n)
	copy(rearranged[:a], std[n-a:])
	copy(rearranged[a:n-a], std[a:n-a])
	copy(rearranged[n-a:], std[:a])
	result := make([]byte, n)
	for i, ch := range rearranged {
		if ch >= 128 || !s2cOK[ch] {
			panic(fmt.Sprintf("char out of alphabet: %q", ch))
		}
		result[i] = s2c[ch]
	}
	return string(result)
}

func Decode(encoded string) ([]byte, error) {
	n := len(encoded)
	mapped := make([]byte, n)
	for i := 0; i < n; i++ {
		ch := encoded[i]
		if ch >= 128 || !c2sOK[ch] {
			return nil, fmt.Errorf("char out of custom alphabet: %q", ch)
		}
		mapped[i] = c2s[ch]
	}
	a := n / 3
	std := make([]byte, n)
	copy(std[:a], mapped[n-a:])
	copy(std[a:n-a], mapped[a:n-a])
	copy(std[n-a:], mapped[:a])
	return base64.StdEncoding.DecodeString(string(std))
}

// --- Request Signing ---

const appCode = "cosy"
const defaultSecret = "d2FyLCB3YXIgbmV2ZXIgY2hhbmdlcw=="

var secretValue string
var secretOnce sync.Once

func getSecret() string {
	secretOnce.Do(func() {
		secretValue = os.Getenv("QODER_SIGNATURE_SECRET")
		if secretValue == "" {
			secretValue = defaultSecret
		}
	})
	return secretValue
}

func CurrentDate() string {
	return time.Now().UTC().Format("Mon, 02 Jan 2006 15:04:05 GMT")
}

func Sign(date string) string {
	s := appCode + "&" + getSecret() + "&" + date
	h := md5.Sum([]byte(s))
	return fmt.Sprintf("%x", h)
}

// --- Bearer Construction ---

const serverPubKeyPEM = `-----BEGIN PUBLIC KEY-----
MIGfMA0GCSqGSIb3DQEBAQUAA4GNADCBiQKBgQDA8iMH5c02LilrsERw9t6Pv5Nc
4k6Pz1EaDicBMpdpxKduSZu5OANqUq8er4GM95omAGIOPOh+Nx0spthYA2BqGz+l
6HRkPJ7S236FZz73In/KVuLnwI8JJ2CbuJap8kvheCCZpmAWpb/cPx/3Vr/J6I17
XcW+ML9FoCI6AOvOzwIDAQAB
-----END PUBLIC KEY-----`

var rsaPubKey *rsa.PublicKey

func init() {
	block, _ := pem.Decode([]byte(serverPubKeyPEM))
	if block == nil {
		panic("failed to parse server public key PEM")
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		panic(fmt.Sprintf("failed to parse server public key: %v", err))
	}
	rsaPubKey = pub.(*rsa.PublicKey)
}

type AuthIdentity struct {
	Name               string
	Aid                string
	UID                string
	YxUID              string
	OrganizationID     string
	OrganizationName   string
	UserType           string
	SecurityOauthToken string
	RefreshToken       string
}

type SessionContext struct {
	TempKey      []byte
	CosyKey      string
	Info         string
	Identity     AuthIdentity
	MachineID    string
	MachineToken string
	MachineType  string
}

func rsaEncrypt(tempKey []byte) ([]byte, error) {
	return rsa.EncryptPKCS1v15(rand.Reader, rsaPubKey, tempKey)
}

func aesEncrypt(plain, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	padLen := 16 - len(plain)%16
	padded := make([]byte, len(plain)+padLen)
	copy(padded, plain)
	for i := len(plain); i < len(padded); i++ {
		padded[i] = byte(padLen)
	}
	encrypted := make([]byte, len(padded))
	mode := cipher.NewCBCEncrypter(block, key[:16])
	mode.CryptBlocks(encrypted, padded)
	return encrypted, nil
}

func md5Hex(s string) string {
	h := md5.Sum([]byte(s))
	return fmt.Sprintf("%x", h)
}

type authPayloadStruct struct {
	Name               string `json:"name"`
	Aid                string `json:"aid"`
	UID                string `json:"uid"`
	YxUID              string `json:"yx_uid"`
	OrganizationID     string `json:"organization_id"`
	OrganizationName   string `json:"organization_name"`
	UserType           string `json:"user_type"`
	SecurityOauthToken string `json:"security_oauth_token"`
	RefreshToken       string `json:"refresh_token"`
}

func authPayloadJSON(identity AuthIdentity) []byte {
	p := authPayloadStruct{
		Name:               identity.Name,
		Aid:                identity.Aid,
		UID:                identity.UID,
		YxUID:              identity.YxUID,
		OrganizationID:     identity.OrganizationID,
		OrganizationName:   identity.OrganizationName,
		UserType:           identity.UserType,
		SecurityOauthToken: identity.SecurityOauthToken,
		RefreshToken:       identity.RefreshToken,
	}
	b, err := marshalNoEscape(p)
	if err != nil {
		panic(err)
	}
	return b
}

// marshalNoEscape serializes v to compact JSON without HTML escaping,
// matching Python's json.dumps(v, separators=(",", ":")).
func marshalNoEscape(v interface{}) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

func NewSession(identity AuthIdentity, machineID, machineToken, machineType string) *SessionContext {
	tempKey := []byte(uuid.New().String()[:16])
	encrypted, err := rsaEncrypt(tempKey)
	if err != nil {
		panic(err)
	}
	cosyKey := base64.StdEncoding.EncodeToString(encrypted)
	aesPlain := authPayloadJSON(identity)
	aesEncrypted, err := aesEncrypt(aesPlain, tempKey)
	if err != nil {
		panic(err)
	}
	info := base64.StdEncoding.EncodeToString(aesEncrypted)
	return &SessionContext{
		TempKey:      tempKey,
		CosyKey:      cosyKey,
		Info:         info,
		Identity:     identity,
		MachineID:    machineID,
		MachineToken: machineToken,
		MachineType:  machineType,
	}
}

func SignRequest(payloadB64, cosyKey, cosyDate, body, pathWithoutAlgo string) string {
	s := payloadB64 + "\n" + cosyKey + "\n" + cosyDate + "\n" + body + "\n" + pathWithoutAlgo
	return md5Hex(s)
}

type cosyPayloadStruct struct {
	CosyVersion string `json:"cosyVersion"`
	IdeVersion  string `json:"ideVersion"`
	Info        string `json:"info"`
	RequestID   string `json:"requestId"`
	Version     string `json:"version"`
}

func BuildPayloadB64(info string) string {
	p := cosyPayloadStruct{
		CosyVersion: "0.1.43",
		IdeVersion:  "",
		Info:        info,
		RequestID:   uuid.New().String(),
		Version:     "v1",
	}
	b, err := marshalNoEscape(p)
	if err != nil {
		panic(err)
	}
	return base64.StdEncoding.EncodeToString(b)
}

func ComposeBearer(payloadB64, sig string) string {
	return "Bearer COSY." + payloadB64 + "." + sig
}

// --- AuthError ---

type AuthError struct {
	StatusCode int
	Detail     string
}

func (e *AuthError) Error() string {
	s := fmt.Sprintf("HTTP %d", e.StatusCode)
	if e.Detail != "" {
		s += " " + e.Detail
	}
	return s
}

// --- Signature API Client ---

func commonHeaders(machineID, machineToken, machineType, date, sig string) http.Header {
	h := http.Header{}
	h.Set("cosy-machinetoken", machineToken)
	h.Set("cosy-machinetype", machineType)
	h.Set("login-version", "v2")
	h.Set("appcode", appCode)
	h.Set("accept", "application/json")
	h.Set("accept-encoding", "identity")
	h.Set("cosy-version", "0.1.43")
	h.Set("cosy-clienttype", "5")
	h.Set("date", date)
	h.Set("signature", sig)
	h.Set("content-type", "application/json")
	h.Set("cosy-machineid", machineID)
	h.Set("user-agent", "Go-http-client/2.0")
	return h
}

func postEncoded(ctx context.Context, urlStr string, obj interface{}, machineID, machineToken, machineType string) (map[string]interface{}, error) {
	date := CurrentDate()
	sig := Sign(date)
	plain, err := marshalNoEscape(obj)
	if err != nil {
		return nil, err
	}
	body := Encode(plain)
	headers := commonHeaders(machineID, machineToken, machineType, date, sig)

	req, err := http.NewRequestWithContext(ctx, "POST", urlStr, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header = headers

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		drainBody(resp.Body)
		if resp.StatusCode == 401 || resp.StatusCode == 403 {
			return nil, &AuthError{StatusCode: resp.StatusCode, Detail: string(detail)}
		}
		return nil, fmt.Errorf("HTTP %d at %s body=%s", resp.StatusCode, urlStr, string(detail))
	}

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return result, nil
}

type jobTokenInnerStruct struct {
	PersonalToken      string          `json:"personalToken"`
	SecurityOauthToken string          `json:"securityOauthToken"`
	RefreshToken       string          `json:"refreshToken"`
	NeedRefresh        bool            `json:"needRefresh"`
	AuthInfo           json.RawMessage `json:"authInfo"`
}

type jobTokenOuterStruct struct {
	Payload       string `json:"payload"`
	EncodeVersion string `json:"encodeVersion"`
}

var emptyJSON = json.RawMessage("{}")

func requestJobToken(ctx context.Context, personalToken, refreshToken, securityOauthToken string, needRefresh bool, machineID, machineToken, machineType string, region *RegionConfig) (map[string]interface{}, error) {
	urlStr := AuthURL(region, "/algo/api/v3/user/jobToken?Encode=1")
	inner := jobTokenInnerStruct{
		PersonalToken:      personalToken,
		SecurityOauthToken: securityOauthToken,
		RefreshToken:       refreshToken,
		NeedRefresh:        needRefresh,
		AuthInfo:           emptyJSON,
	}
	innerJSON, err := marshalNoEscape(inner)
	if err != nil {
		return nil, err
	}
	outer := jobTokenOuterStruct{
		Payload:       string(innerJSON),
		EncodeVersion: "1",
	}
	return postEncoded(ctx, urlStr, outer, machineID, machineToken, machineType)
}

func ExchangeJobToken(ctx context.Context, personalToken, machineID, machineToken, machineType string, region *RegionConfig) (map[string]interface{}, error) {
	if region == nil {
		region = CN
	}
	return requestJobToken(ctx, personalToken, "", "", false, machineID, machineToken, machineType, region)
}

func RefreshJobToken(ctx context.Context, personalToken, refreshToken, securityOauthToken, machineID, machineToken, machineType string, region *RegionConfig) (map[string]interface{}, error) {
	if region == nil {
		region = CN
	}
	return requestJobToken(ctx, personalToken, refreshToken, securityOauthToken, true, machineID, machineToken, machineType, region)
}

// drainBody reads and discards the remaining response body (up to a cap) so
// the underlying TCP connection can be returned to the pool for reuse.
// Must be called before resp.Body.Close() on error paths where the body was
// only partially read.
func drainBody(body io.Reader) {
	io.Copy(io.Discard, io.LimitReader(body, 1<<20))
}

// --- Bearer API Client ---

func makeCommonHeaders(sess *SessionContext, date, bearer, accept string) http.Header {
	h := http.Header{}
	h.Set("cosy-data-policy", "AGREE")
	h.Set("content-type", "application/json")
	h.Set("cosy-machinetype", sess.MachineType)
	h.Set("cosy-clienttype", "5")
	h.Set("cosy-date", date)
	h.Set("cosy-user", sess.Identity.UID)
	h.Set("cosy-key", sess.CosyKey)
	h.Set("accept", accept)
	h.Set("authorization", bearer)
	h.Set("accept-encoding", "identity")
	h.Set("cosy-version", "0.1.43")
	h.Set("cosy-machineid", sess.MachineID)
	h.Set("cosy-machinetoken", sess.MachineToken)
	h.Set("login-version", "v2")
	h.Set("user-agent", "Go-http-client/2.0")
	return h
}

func buildBearer(sess *SessionContext, date, body, pathSig string) string {
	payloadB64 := BuildPayloadB64(sess.Info)
	sig := SignRequest(payloadB64, sess.CosyKey, date, body, pathSig)
	return ComposeBearer(payloadB64, sig)
}

func sigPath(fullURL string) string {
	u, err := url.Parse(fullURL)
	if err != nil {
		return fullURL
	}
	path := u.Path
	if strings.HasPrefix(path, "/algo") {
		path = path[len("/algo"):]
	}
	return path
}

func CallPost(ctx context.Context, sess *SessionContext, fullURL string, jsonBody map[string]interface{}) (map[string]interface{}, error) {
	return call(ctx, sess, "POST", fullURL, jsonBody, nil)
}

func CallGet(ctx context.Context, sess *SessionContext, fullURL string) (map[string]interface{}, error) {
	return call(ctx, sess, "GET", fullURL, nil, nil)
}

func call(ctx context.Context, sess *SessionContext, method, fullURL string, jsonBody map[string]interface{}, extraHeaders map[string]string) (map[string]interface{}, error) {
	pathSig := sigPath(fullURL)
	body := ""
	if jsonBody != nil {
		plain, err := marshalNoEscape(jsonBody)
		if err != nil {
			return nil, err
		}
		body = Encode(plain)
	}
	date := strconv.FormatInt(time.Now().Unix(), 10)
	bearer := buildBearer(sess, date, body, pathSig)
	headers := makeCommonHeaders(sess, date, bearer, "application/json")
	for k, v := range extraHeaders {
		headers.Set(k, v)
	}

	var reqBody io.Reader
	if body != "" {
		reqBody = strings.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, fullURL, reqBody)
	if err != nil {
		return nil, err
	}
	req.Header = headers

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		drainBody(resp.Body)
		if resp.StatusCode == 401 || resp.StatusCode == 403 {
			return nil, &AuthError{StatusCode: resp.StatusCode, Detail: string(detail)}
		}
		return nil, fmt.Errorf("HTTP %d body=%s", resp.StatusCode, string(detail))
	}

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return result, nil
}

// StreamCallback is called for each non-empty SSE line.
type StreamCallback func(line string) error

// --- Configurable stream timeouts ---

// DefaultChatHeaderTimeout bounds how long a chat stream may take to start
// responding (time to response headers). DefaultChatIdleTimeout bounds how
// long an established SSE stream may stay silent (no bytes received) before
// we give up. Without the idle bound a wedged gateway connection (TCP alive,
// no data) pins a goroutine + connection forever, and the server
// intentionally sets no WriteTimeout for SSE.
const (
	DefaultChatHeaderTimeout = 120 * time.Second
	DefaultChatIdleTimeout   = 5 * time.Minute
)

// StreamTimeouts bundles the two chat stream timeouts. Both can be changed
// at runtime via SetStreamTimeouts; new requests pick the values up
// immediately, in-flight streams keep running with the old ones.
type StreamTimeouts struct {
	Header time.Duration // wait for the chat stream to start responding
	Idle   time.Duration // max silence on an established SSE stream
}

func (t StreamTimeouts) normalized() StreamTimeouts {
	if t.Header <= 0 {
		t.Header = DefaultChatHeaderTimeout
	}
	if t.Idle <= 0 {
		t.Idle = DefaultChatIdleTimeout
	}
	return t
}

var (
	streamTimeoutsVal atomic.Pointer[StreamTimeouts]
	streamClientVal   atomic.Pointer[http.Client]
)

func init() {
	SetStreamTimeouts(DefaultChatHeaderTimeout, DefaultChatIdleTimeout)
}

// SetStreamTimeouts configures the chat stream timeouts and rebuilds the
// dedicated stream client. The client is swapped atomically so concurrent
// requests never observe a half-updated state.
func SetStreamTimeouts(header, idle time.Duration) {
	t := StreamTimeouts{Header: header, Idle: idle}.normalized()
	// Own transport (cloned from the default) isolates the long-lived SSE
	// connection pool from the short-request clients, and lets the header
	// timeout change without touching them. ResponseHeaderTimeout is the
	// only deadline: a Client.Timeout here would truncate long generations.
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = t.Header
	streamClientVal.Store(&http.Client{Transport: transport})
	streamTimeoutsVal.Store(&t)
}

// CurrentStreamTimeouts returns the effective stream timeouts.
func CurrentStreamTimeouts() StreamTimeouts {
	if t := streamTimeoutsVal.Load(); t != nil {
		return *t
	}
	return StreamTimeouts{}.normalized()
}

// isDoneLine reports whether an SSE line carries the terminal [DONE]
// marker. The Qoder gateway wraps [DONE] inside its Encode-layer envelope
// ({"body":"[DONE]",...}) rather than emitting a bare "data: [DONE]";
// both forms are accepted so test fakes and real traffic both terminate
// cleanly.
func isDoneLine(line string) bool {
	if !strings.HasPrefix(line, "data:") {
		return false
	}
	payload := strings.TrimSpace(line[5:])
	if payload == "[DONE]" {
		return true
	}
	var env struct{ Body string `json:"body"` }
	if json.Unmarshal([]byte(payload), &env) == nil && env.Body == "[DONE]" {
		return true
	}
	return false
}

// isFinishEvent reports whether an SSE line is the gateway's terminal
// "event:finish" marker. Some models (MiniMax) end their stream with this
// event instead of a [DONE] frame, so it must count as a clean termination
// just like [DONE].
func isFinishEvent(line string) bool {
	return strings.HasPrefix(line, "event:") && strings.TrimSpace(line[len("event:"):]) == "finish"
}

// isErrorEvent reports whether an SSE line is the gateway's terminal
// "event:error" marker: the upstream provider failed and the stream is
// about to end without [DONE]. Observed on qmodel_preview provider outages
// (429 provider_error "All backends failed").
func isErrorEvent(line string) bool {
	return strings.HasPrefix(line, "event:") && strings.TrimSpace(line[len("event:"):]) == "error"
}

// inStreamError describes a gateway failure delivered inside the SSE stream
// (HTTP status is still 200). Kept when seen so the EOF-without-[DONE]
// fallback reports the real cause instead of a misleading "connection
// truncated".
type inStreamError struct {
	status int
	detail string
}

func (e *inStreamError) Error() string {
	if e.status > 0 {
		return fmt.Sprintf("gateway in-stream error (HTTP %d): %s", e.status, e.detail)
	}
	return fmt.Sprintf("gateway in-stream error: %s", e.detail)
}

// detectInStreamGatewayError extracts a terminal gateway/provider failure
// from an SSE data line. Two shapes are recognized:
//
//  1. Envelope frame with an HTTP error status, e.g. (qmodel_preview
//	     provider outage):
//	     {"body":"{\"code\":\"provider_error\",\"message\":\"All backends failed\"...}",
//	      "statusCodeValue":429,"statusCode":"TOO_MANY_REQUESTS"}
//
//  2. Bare success:false gateway error frame, e.g.
//	     {"success":false,"msgCode":500,"message":"Internal Server Error"}
//
// A 200 OK envelope with normal chat/usage/[DONE] bodies returns nil.
func detectInStreamGatewayError(line string) *inStreamError {
	if !strings.HasPrefix(line, "data:") {
		return nil
	}
	payload := strings.TrimSpace(line[len("data:"):])
	var env struct {
		Body          string `json:"body"`
		StatusCode    string `json:"statusCode"`
		StatusValue   int    `json:"statusCodeValue"`
	}
	if err := json.Unmarshal([]byte(payload), &env); err != nil {
		return nil
	}
	if env.StatusValue >= 400 {
		detail := env.Body
		if detail == "" {
			detail = env.StatusCode
		}
		return &inStreamError{status: env.StatusValue, detail: trimJSONMessage(detail)}
	}
	if env.Body != "" || env.StatusCode != "" {
		// A well-formed envelope with a 2xx status is a normal frame.
		return nil
	}
	var bare struct {
		Success  bool             `json:"success"`
		MsgCode  int              `json:"msgCode"`
		MsgInfo  string           `json:"msgInfo"`
		Message  string           `json:"message"`
	}
	if err := json.Unmarshal([]byte(payload), &bare); err != nil {
		return nil
	}
	if !bare.Success && (bare.MsgCode >= 400 || bare.Message != "" || bare.MsgInfo != "") {
		// Only treat it as a failure when a message/code is present; a bare
		// {"success":false} with no reason is too ambiguous to fail on.
		detail := bare.Message
		if detail == "" {
			detail = bare.MsgInfo
		}
		if detail == "" {
			detail = fmt.Sprintf("msgCode %d", bare.MsgCode)
		}
		return &inStreamError{status: bare.MsgCode, detail: detail}
	}
	return nil
}

// trimJSONMessage pulls the "message" field out of a JSON-string body when
// present ("{\"code\":\"provider_error\",\"message\":\"All backends failed\"...}")
// so the surfaced error reads "provider_error: All backends failed" instead
// of a wall of escaped JSON.
func trimJSONMessage(body string) string {
	var m struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(body), &m); err == nil {
		if m.Code != "" && m.Message != "" {
			return m.Code + ": " + m.Message
		}
		if m.Message != "" {
			return m.Message
		}
	}
	return strings.TrimSpace(body)
}

// OpenStreamLines sends a POST and reads SSE response line by line.
func OpenStreamLines(ctx context.Context, sess *SessionContext, fullURL string, jsonBody []byte, extraHeaders map[string]string, callback StreamCallback) error {
	pathSig := sigPath(fullURL)
	body := Encode(jsonBody)
	date := strconv.FormatInt(time.Now().Unix(), 10)
	bearer := buildBearer(sess, date, body, pathSig)
	headers := makeCommonHeaders(sess, date, bearer, "text/event-stream")
	headers.Set("cache-control", "no-cache")
	for k, v := range extraHeaders {
		headers.Set(k, v)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", fullURL, strings.NewReader(body))
	if err != nil {
		return err
	}
	req.Header = headers

	resp, err := streamClientVal.Load().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		drainBody(resp.Body)
		if resp.StatusCode == 401 || resp.StatusCode == 403 {
			return &AuthError{StatusCode: resp.StatusCode, Detail: string(errBody)}
		}
		return fmt.Errorf("HTTP %d %s", resp.StatusCode, string(errBody))
	}

	// Cut off silent streams: long generations are fine (lines keep
	// arriving), only a silent connection is killed. The watchdog polls a
	// last-activity timestamp instead of sharing a timer with the read loop —
	// Reset on a fired-but-undrained timer is racy under the pre-Go-1.23
	// timer semantics this module builds with.
	timeouts := CurrentStreamTimeouts()
	idleErr := fmt.Errorf("stream idle for over %s", timeouts.Idle)
	var lastLineNs atomic.Int64
	lastLineNs.Store(time.Now().UnixNano())
	var idleFired atomic.Bool
	cancelRead := func() {
		// Cancel the body read; scanner.Scan() then returns with an error.
		if c, ok := resp.Body.(interface{ SetReadDeadline(time.Time) error }); ok {
			c.SetReadDeadline(time.Now())
		} else {
			// Best effort for bodies without deadline support (HTTP/1.1):
			// closing unblocks the read with a "closed network connection"
			// error, which idleFired maps back to idleErr below.
			resp.Body.Close()
		}
	}

	// The gateway always terminates its stream with a [DONE] marker. An EOF
	// before it means the connection was cut mid-response; treating that as
	// success would silently truncate the answer and still emit our own
	// [DONE] to the client.
	sawDone := false
	var gwErr *inStreamError

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	done := make(chan struct{})
	defer close(done)
	go func() {
		checkEvery := timeouts.Idle / 4
		if checkEvery < 10*time.Millisecond {
			checkEvery = 10 * time.Millisecond
		}
		ticker := time.NewTicker(checkEvery)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				if time.Since(time.Unix(0, lastLineNs.Load())) >= timeouts.Idle {
					idleFired.Store(true)
					cancelRead()
					return
				}
			}
		}
	}()
	for scanner.Scan() {
		line := scanner.Text()
		lastLineNs.Store(time.Now().UnixNano())
		if line == "" {
			continue
		}
		if isDoneLine(line) {
			sawDone = true
		}
		if isFinishEvent(line) {
			// MiniMax-style termination: no [DONE] frame, just event:finish
			// followed by a telemetry frame and EOF.
			sawDone = true
		}
		if isErrorEvent(line) {
			// Provider-side failure: the stream will end without [DONE]; make
			// sure the EOF fallback reports the real cause (from the error
			// frames seen before this event) instead of "truncated".
			if gwErr == nil {
				gwErr = &inStreamError{detail: "upstream stream failed (event:error)"}
			}
		}
		if e := detectInStreamGatewayError(line); e != nil && gwErr == nil {
			gwErr = e
		}
		isAuthErr, detail := detectInStreamAuthError(line)
		if isAuthErr {
			return &AuthError{StatusCode: 401, Detail: detail}
		}
		if err := callback(line); err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	if err := scanner.Err(); err != nil {
		if idleFired.Load() || errors.Is(err, os.ErrDeadlineExceeded) {
			return idleErr
		}
		return err
	}
	if !sawDone {
		// Prefer the gateway's own failure reason when it sent error frames
		// before the premature EOF; "connection truncated" is the fallback.
		if gwErr != nil {
			return gwErr
		}
		return fmt.Errorf("stream ended without [DONE] marker (connection truncated?)")
	}
	return nil
}

// detectInStreamAuthError detects auth failures inside the SSE stream.
func detectInStreamAuthError(line string) (bool, string) {
	s := strings.TrimSpace(line)
	if !strings.HasPrefix(s, "data:") {
		return false, ""
	}
	var obj map[string]interface{}
	if err := json.Unmarshal([]byte(strings.TrimSpace(s[5:])), &obj); err != nil {
		return false, ""
	}
	scv, ok := obj["statusCodeValue"].(float64)
	if ok && (scv == 401 || scv == 403) {
		return true, fmt.Sprintf("%v %s", scv, extractBodyMessage(obj))
	}
	msg := extractBodyMessage(obj)
	bodyObj := parseBody(obj)
	if bodyMap, ok := bodyObj.(map[string]interface{}); ok {
		code := bodyMap["code"]
		if code == "105" || code == float64(105) {
			if msg != "" {
				return true, msg
			}
			return true, "Login expired"
		}
	}
	return false, ""
}

func parseBody(obj map[string]interface{}) interface{} {
	body, ok := obj["body"]
	if !ok {
		return nil
	}
	switch b := body.(type) {
	case map[string]interface{}, []interface{}:
		return b
	case string:
		var parsed interface{}
		if err := json.Unmarshal([]byte(b), &parsed); err != nil {
			return nil
		}
		return parsed
	}
	return nil
}

func extractBodyMessage(obj map[string]interface{}) string {
	bodyObj := parseBody(obj)
	bodyMap, ok := bodyObj.(map[string]interface{})
	if !ok {
		return ""
	}
	if msg, ok := bodyMap["message"].(string); ok && msg != "" {
		return msg
	}
	if code, ok := bodyMap["code"]; ok && code != nil {
		s := fmt.Sprintf("%v", code)
		if s != "" && s != "<nil>" {
			return s
		}
	}
	return ""
}
