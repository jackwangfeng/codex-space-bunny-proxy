package proxy

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
)

const readCommandPrefix = "python3 -c 'import base64,json,pathlib,sys; a=json.loads(base64.b64decode(sys.argv[1])); p=pathlib.Path(a[\"filePath\"]); lines=p.read_text().splitlines(); start=max(1,int(a.get(\"offset\",1))); count=max(0,min(2000,int(a.get(\"limit\",2000)))); print(\"\\n\".join(str(i+1)+\": \"+lines[i] for i in range(start-1,min(len(lines),start-1+count))))' "

var spaceClients sync.Map

func spaceProxyClient(raw string) (*http.Client, error) {
	if client, ok := spaceClients.Load(raw); ok {
		return client.(*http.Client), nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "socks5") {
		return nil, fmt.Errorf("invalid space-bunny outbound proxy")
	}
	transport := proxyHTTPClient.Transport.(*http.Transport).Clone()
	transport.Proxy = http.ProxyURL(u)
	client := &http.Client{Transport: transport}
	actual, _ := spaceClients.LoadOrStore(raw, client)
	return actual.(*http.Client), nil
}

func spaceToolChoice(raw json.RawMessage) json.RawMessage {
	var choice map[string]json.RawMessage
	if json.Unmarshal(raw, &choice) != nil {
		return raw
	}
	var function map[string]string
	if json.Unmarshal(choice["function"], &function) == nil && function["name"] == "exec_command" {
		function["name"] = "bash"
		choice["function"] = marshalNoEscape(function)
		return marshalNoEscape(choice)
	}
	var name string
	if json.Unmarshal(choice["name"], &name) == nil && name == "exec_command" {
		delete(choice, "name")
		choice["function"] = marshalNoEscape(map[string]string{"name": "bash"})
		return marshalNoEscape(choice)
	}
	return raw
}

func spaceCompaction(messages []ChatMessage) bool {
	if len(messages) == 0 {
		return false
	}
	m := messages[len(messages)-1]
	if m.Role != "user" {
		return false
	}
	text := extractResponsesOutput(m.Content)
	return strings.HasPrefix(strings.TrimSpace(text), "You are performing a CONTEXT CHECKPOINT COMPACTION.")
}

func renamedArgs(raw string, from, to string) string {
	var args map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &args) != nil {
		return raw
	}
	if v, ok := args[from]; ok {
		delete(args, from)
		args[to] = v
	}
	return string(marshalNoEscape(args))
}

// The compatibility tools are executable adapters to Codex's actual shell
// function. They never execute on the proxy and retain Codex's sandbox/approval.
func prepareSpaceTools(messages []ChatMessage, tools []ChatTool) ([]ChatMessage, []ChatTool, bool) {
	adapted := append([]ChatTool(nil), tools...)
	found := false
	for i, t := range adapted {
		if t.Function.Name != "exec_command" {
			continue
		}
		var schema map[string]json.RawMessage
		if json.Unmarshal(t.Function.Parameters, &schema) != nil {
			return messages, tools, false
		}
		var props map[string]json.RawMessage
		if json.Unmarshal(schema["properties"], &props) != nil {
			return messages, tools, false
		}
		props["command"] = props["cmd"]
		delete(props, "cmd")
		schema["properties"] = marshalNoEscape(props)
		var required []string
		json.Unmarshal(schema["required"], &required)
		for j := range required {
			if required[j] == "cmd" {
				required[j] = "command"
			}
		}
		schema["required"] = marshalNoEscape(required)
		adapted[i].Function.Name = "bash"
		adapted[i].Function.Parameters = marshalNoEscape(schema)
		found = true
	}
	if !found {
		return messages, tools, false
	}
	adapted = append(adapted, ChatTool{Type: "function", Function: ChatFunction{
		Name: "read", Description: "Read a UTF-8 text file. Return numbered lines. Use absolute filePath; offset is one-based, limit at most 2000. Executed by the Codex shell under its existing permissions.",
		Parameters: json.RawMessage(`{"type":"object","properties":{"filePath":{"type":"string"},"offset":{"type":"integer","minimum":1},"limit":{"type":"integer","minimum":1,"maximum":2000}},"required":["filePath"],"additionalProperties":false}`),
	}})
	history := append([]ChatMessage(nil), messages...)
	for i, m := range history {
		history[i].ToolCalls = append([]ToolCall(nil), m.ToolCalls...)
		for j, c := range history[i].ToolCalls {
			if c.Function.Name != "exec_command" {
				continue
			}
			history[i].ToolCalls[j].Function.Name = "bash"
			history[i].ToolCalls[j].Function.Arguments = renamedArgs(c.Function.Arguments, "cmd", "command")
		}
	}
	return history, adapted, true
}

func spaceHeaders(req *http.Request) {
	var id [13]byte
	rand.Read(id[:])
	nonce := hex.EncodeToString(id[:])
	req.Header.Set("User-Agent", "opencode/1.18.35 zen-proxy-space/0.3.0")
	req.Header.Set("x-opencode-client", "cli")
	req.Header.Set("x-opencode-project", "global")
	req.Header.Set("x-opencode-session", "ses_"+nonce)
	req.Header.Set("x-opencode-session-id", "ses_"+nonce)
	req.Header.Set("x-opencode-request", "msg_"+nonce)
}

func spaceToolResult(tool ToolCall) (ToolCall, error) {
	switch tool.Function.Name {
	case "bash":
		tool.Function.Name = "exec_command"
		tool.Function.Arguments = renamedArgs(tool.Function.Arguments, "command", "cmd")
	case "read":
		var args struct {
			FilePath      string `json:"filePath"`
			Offset, Limit *int
		}
		if err := json.Unmarshal([]byte(tool.Function.Arguments), &args); err != nil {
			return tool, err
		}
		if args.FilePath == "" {
			return tool, fmt.Errorf("read requires filePath")
		}
		command := readCommandPrefix + base64.StdEncoding.EncodeToString([]byte(tool.Function.Arguments))
		tool.Function.Name = "exec_command"
		tool.Function.Arguments = string(marshalNoEscape(map[string]any{"cmd": command, "max_output_tokens": 12000}))
	}
	return tool, nil
}

type spaceStreamCloser struct {
	io.ReadCloser
	original io.Closer
}

func (c *spaceStreamCloser) Close() error {
	err := c.ReadCloser.Close()
	c.original.Close()
	return err
}

func spaceResponseBody(body io.ReadCloser, stream bool, textOnly ...bool) io.ReadCloser {
	blockTools := len(textOnly) > 0 && textOnly[0]
	if !stream {
		var completion ChatCompletion
		err := json.NewDecoder(body).Decode(&completion)
		body.Close()
		if err != nil {
			return io.NopCloser(strings.NewReader(`{"error":"invalid upstream completion"}`))
		}
		for i := range completion.Choices {
			if blockTools && len(completion.Choices[i].Message.ToolCalls) > 0 {
				return io.NopCloser(strings.NewReader(`{"error":"tool call rejected during text-only compaction"}`))
			}
			for j, t := range completion.Choices[i].Message.ToolCalls {
				converted, e := spaceToolResult(t)
				if e != nil {
					return io.NopCloser(strings.NewReader(`{"error":"invalid upstream tool arguments"}`))
				}
				completion.Choices[i].Message.ToolCalls[j] = converted
			}
		}
		return io.NopCloser(bytes.NewReader(marshalNoEscape(completion)))
	}
	reader, writer := io.Pipe()
	go func() {
		defer body.Close()
		pending := map[int]ToolCall{}
		err := iterateSSE(body, func(chunk streamChunk) error {
			for i := range chunk.Choices {
				choice := &chunk.Choices[i]
				if blockTools && len(choice.Delta.ToolCalls) > 0 {
					return fmt.Errorf("tool call rejected during text-only compaction")
				}
				for _, t := range choice.Delta.ToolCalls {
					c := pending[t.Index]
					c.Index = t.Index
					if t.ID != "" {
						c.ID = t.ID
					}
					if t.Type != "" {
						c.Type = t.Type
					}
					c.Function.Name += t.Function.Name
					c.Function.Arguments += t.Function.Arguments
					pending[t.Index] = c
				}
				choice.Delta.ToolCalls = nil
				if choice.FinishReason != nil && len(pending) > 0 {
					keys := make([]int, 0, len(pending))
					for k := range pending {
						keys = append(keys, k)
					}
					sort.Ints(keys)
					for _, k := range keys {
						t, e := spaceToolResult(pending[k])
						if e != nil {
							return e
						}
						choice.Delta.ToolCalls = append(choice.Delta.ToolCalls, t)
					}
					pending = map[int]ToolCall{}
				}
			}
			_, e := fmt.Fprintf(writer, "data: %s\n\n", marshalNoEscape(chunk))
			return e
		})
		if err == nil && len(pending) > 0 {
			err = fmt.Errorf("upstream tool stream ended before completion")
		}
		if err == nil {
			_, err = io.WriteString(writer, "data: [DONE]\n\n")
		}
		writer.CloseWithError(err)
	}()
	return &spaceStreamCloser{ReadCloser: reader, original: body}
}
