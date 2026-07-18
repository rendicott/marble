package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ToolDef is a discovered MCP tool.
type ToolDef struct {
	Server      string
	Name        string // original MCP name
	MarbleName  string // mcp_server_tool
	Description string
	InputSchema map[string]interface{}
}

// ResourceDef is a discovered resource.
type ResourceDef struct {
	Server      string
	URI         string
	Name        string
	Description string
	MimeType    string
}

// PromptDef is a discovered prompt template.
type PromptDef struct {
	Server      string
	Name        string
	Description string
	// Arguments from MCP if present
	Arguments []PromptArg
}

// PromptArg is a prompt argument descriptor.
type PromptArg struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

// Session is a connected MCP server (stdio or HTTP).
type Session interface {
	Name() string
	Close() error
	ListTools(ctx context.Context) ([]ToolDef, error)
	CallTool(ctx context.Context, name string, args map[string]interface{}) (string, error)
	ListResources(ctx context.Context) ([]ResourceDef, error)
	ReadResource(ctx context.Context, uri string) (string, error)
	ListPrompts(ctx context.Context) ([]PromptDef, error)
	GetPrompt(ctx context.Context, name string, args map[string]string) (string, error)
}

// jsonRPCConn is a bidirectional JSON-RPC 2.0 connection over framed messages.
type jsonRPCConn struct {
	name   string
	w      io.Writer
	r      io.Reader
	mu     sync.Mutex
	nextID int64
	pending map[string]chan rpcResp
	readErr error
	closed  chan struct{}
	closeOnce sync.Once
}

type rpcReq struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      interface{} `json:"id,omitempty"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
}

type rpcResp struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
	Method  string          `json:"method,omitempty"` // notifications / requests from server
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func newJSONRPCConn(name string, r io.Reader, w io.Writer) *jsonRPCConn {
	c := &jsonRPCConn{
		name:    name,
		w:       w,
		r:       r,
		pending: make(map[string]chan rpcResp),
		closed:  make(chan struct{}),
	}
	go c.readLoop()
	return c
}

func (c *jsonRPCConn) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return nil
}

func (c *jsonRPCConn) Call(ctx context.Context, method string, params interface{}, result interface{}) error {
	id := atomic.AddInt64(&c.nextID, 1)
	idStr := strconv.FormatInt(id, 10)
	ch := make(chan rpcResp, 1)
	c.mu.Lock()
	c.pending[idStr] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, idStr)
		c.mu.Unlock()
	}()

	req := rpcReq{JSONRPC: "2.0", ID: id, Method: method, Params: params}
	if err := c.writeMsg(req); err != nil {
		return err
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.closed:
		return fmt.Errorf("mcp %s: connection closed", c.name)
	case resp := <-ch:
		if resp.Error != nil {
			return fmt.Errorf("mcp %s: %s (%d)", c.name, resp.Error.Message, resp.Error.Code)
		}
		if result != nil && len(resp.Result) > 0 {
			if err := json.Unmarshal(resp.Result, result); err != nil {
				return fmt.Errorf("mcp %s decode: %w", c.name, err)
			}
		}
		return nil
	}
}

func (c *jsonRPCConn) Notify(method string, params interface{}) error {
	req := rpcReq{JSONRPC: "2.0", Method: method, Params: params}
	return c.writeMsg(req)
}

func (c *jsonRPCConn) writeMsg(v interface{}) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	// Newline-delimited JSON (current MCP SDK / tavily-mcp stdio transport).
	// Content-Length framing is legacy and breaks modern servers.
	if _, err := c.w.Write(body); err != nil {
		return err
	}
	_, err = c.w.Write([]byte("\n"))
	return err
}

func (c *jsonRPCConn) readLoop() {
	for {
		select {
		case <-c.closed:
			return
		default:
		}
		body, err := readFramed(c.r)
		if err != nil {
			c.mu.Lock()
			c.readErr = err
			for _, ch := range c.pending {
				select {
				case ch <- rpcResp{Error: &rpcError{Message: err.Error()}}:
				default:
				}
			}
			c.mu.Unlock()
			c.Close()
			return
		}
		var resp rpcResp
		if err := json.Unmarshal(body, &resp); err != nil {
			continue
		}
		// server→client request/notification: ignore for v1 (no sampling)
		if resp.Method != "" && len(resp.ID) == 0 {
			continue
		}
		if len(resp.ID) == 0 {
			continue
		}
		// id may be number or string
		var idStr string
		var n int64
		if err := json.Unmarshal(resp.ID, &n); err == nil {
			idStr = strconv.FormatInt(n, 10)
		} else {
			_ = json.Unmarshal(resp.ID, &idStr)
		}
		c.mu.Lock()
		ch := c.pending[idStr]
		c.mu.Unlock()
		if ch != nil {
			select {
			case ch <- resp:
			default:
			}
		}
	}
}

func readFramed(r io.Reader) ([]byte, error) {
	// Newline-delimited JSON (MCP SDK serializeMessage = JSON + "\n").
	// Also accept legacy Content-Length framing for older servers.
	var line bytes.Buffer
	buf := make([]byte, 1)
	for {
		if _, err := io.ReadFull(r, buf); err != nil {
			return nil, err
		}
		if buf[0] == '\n' {
			break
		}
		if buf[0] != '\r' {
			line.WriteByte(buf[0])
		}
		if line.Len() > 16<<20 {
			return nil, fmt.Errorf("mcp line too large")
		}
	}
	b := bytes.TrimSpace(line.Bytes())
	if len(b) == 0 {
		return readFramed(r)
	}
	// Legacy: first line is Content-Length: N
	lower := bytes.ToLower(b)
	if bytes.HasPrefix(lower, []byte("content-length:")) {
		n, err := strconv.Atoi(strings.TrimSpace(string(b[len("content-length:"):])))
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("bad content-length")
		}
		// consume header block until blank line
		var prevNL bool
		for {
			if _, err := io.ReadFull(r, buf); err != nil {
				return nil, err
			}
			if buf[0] == '\n' {
				if prevNL {
					break
				}
				prevNL = true
				continue
			}
			if buf[0] == '\r' {
				continue
			}
			prevNL = false
		}
		if n > 16<<20 {
			return nil, fmt.Errorf("mcp message too large")
		}
		body := make([]byte, n)
		if _, err := io.ReadFull(r, body); err != nil {
			return nil, err
		}
		return body, nil
	}
	return b, nil
}

// Initialize handshake for MCP.
func mcpInitialize(ctx context.Context, c *jsonRPCConn, clientName string) error {
	params := map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo": map[string]interface{}{
			"name":    clientName,
			"version": "0.1.0",
		},
	}
	var result map[string]interface{}
	if err := c.Call(ctx, "initialize", params, &result); err != nil {
		return err
	}
	_ = c.Notify("notifications/initialized", map[string]interface{}{})
	return nil
}

func defaultTimeout(d time.Duration) time.Duration {
	if d <= 0 {
		return 60 * time.Second
	}
	return d
}
