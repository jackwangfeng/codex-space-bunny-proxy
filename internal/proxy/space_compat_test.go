package proxy

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestSpaceCompatDeclaresExecutableToolsAndReturnsRealRead(t *testing.T) {
	t.Setenv("ZEN_SPACE_BUNNY_COMPAT", "1")
	file := filepath.Join(t.TempDir(), "a'$(literal).txt")
	if err := os.WriteFile(file, []byte("first\nsecond\nthird\n"), 0600); err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Tools []ChatTool `json:"tools"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		found := map[string]bool{}
		for _, tool := range body.Tools {
			found[tool.Function.Name] = true
		}
		validSession := regexp.MustCompile(`^ses_[A-Za-z0-9]{26}$`).MatchString(r.Header.Get("x-opencode-session"))
		validRequest := regexp.MustCompile(`^msg_[A-Za-z0-9]{26}$`).MatchString(r.Header.Get("x-opencode-request"))
		if !strings.HasPrefix(r.UserAgent(), "opencode/") || !found["bash"] || !found["read"] || !validSession || !validRequest {
			w.WriteHeader(403)
			fmt.Fprint(w, `{"error":"FreeTierError"}`)
			return
		}
		for _, tool := range body.Tools {
			if tool.Function.Name == "bash" && !strings.Contains(string(tool.Function.Parameters), `"command"`) {
				t.Error("bash schema not adapted")
			}
		}
		args, _ := json.Marshal(map[string]any{"filePath": file, "offset": 2, "limit": 1})
		completion := ChatCompletion{ID: "test", Model: "space-bunny-free", Choices: []CompletionChoice{{Message: CompletionMessage{ToolCalls: []ToolCall{{ID: "call_read", Type: "function", Function: FunctionCall{Name: "read", Arguments: string(args)}}}}}}}
		json.NewEncoder(w).Encode(completion)
	}))
	defer upstream.Close()
	cfg := LoadConfig()
	cfg.Upstream = upstream.URL
	gateway := httptest.NewServer(handleResponses(cfg))
	defer gateway.Close()
	req := `{"model":"space-bunny-free","input":"Read the file","tools":[{"type":"function","name":"exec_command","description":"Run shell commands","parameters":{"type":"object","properties":{"cmd":{"type":"string"}},"required":["cmd"]}}]}`
	resp, err := http.Post(gateway.URL, "application/json", strings.NewReader(req))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var result struct {
		Output []struct{ Name, Arguments string }
	}
	json.NewDecoder(resp.Body).Decode(&result)
	if resp.StatusCode != 200 {
		t.Fatalf("got HTTP %d; want compatible upstream accepted", resp.StatusCode)
	}
	if len(result.Output) != 1 || result.Output[0].Name != "exec_command" {
		t.Fatalf("unsupported Codex tool output: %+v", result.Output)
	}
	var args struct{ Cmd string }
	json.Unmarshal([]byte(result.Output[0].Arguments), &args)
	output, err := exec.Command("bash", "-c", args.Cmd).CombinedOutput()
	if err != nil {
		t.Fatalf("read adapter failed: %s %v", output, err)
	}
	if string(output) != "2: second\n" {
		t.Fatalf("real file read output %q", output)
	}
}

func TestSpaceCompatStreamingBashArgumentsAndReplay(t *testing.T) {
	t.Setenv("ZEN_SPACE_BUNNY_COMPAT", "1")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []ChatMessage `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		for _, m := range body.Messages {
			for _, c := range m.ToolCalls {
				if c.Function.Name != "bash" || !strings.Contains(c.Function.Arguments, `"command"`) {
					t.Errorf("replay not adapted: %+v", c)
				}
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"id\":\"test\",\"choices\":[{\"delta\":{\"content\":\"Checking\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"id\":\"test\",\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_bash\",\"function\":{\"name\":\"bash\",\"arguments\":\"{\\\"command\\\":\"}}]}}]}\n\n")
		fmt.Fprint(w, "data: {\"id\":\"test\",\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"\\\"printf OK\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer upstream.Close()
	cfg := LoadConfig()
	cfg.Upstream = upstream.URL
	gateway := httptest.NewServer(handleResponses(cfg))
	defer gateway.Close()
	req := `{"model":"space-bunny-free","stream":true,"input":[{"type":"function_call","call_id":"previous","name":"exec_command","arguments":"{\"cmd\":\"cat a\"}"},{"type":"function_call_output","call_id":"previous","output":"file contents"}],"tools":[{"type":"function","name":"exec_command","parameters":{"type":"object","properties":{"cmd":{"type":"string"}}}}]}`
	resp, err := http.Post(gateway.URL, "application/json", strings.NewReader(req))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	bytes, _ := io.ReadAll(resp.Body)
	var got string
	for _, line := range strings.Split(string(bytes), "\n") {
		if strings.HasPrefix(line, "data: ") {
			var e map[string]any
			json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &e)
			if e["type"] == "response.function_call_arguments.done" {
				got, _ = e["arguments"].(string)
			}
		}
	}
	var args struct{ Cmd string }
	json.Unmarshal([]byte(got), &args)
	if args.Cmd != "printf OK" {
		t.Fatalf("streamed arguments not adapted: %q; stream %s", got, bytes)
	}
	if !strings.Contains(string(bytes), `"name":"exec_command"`) {
		t.Fatalf("unsupported tool name: %s", bytes)
	}
}

func TestSpaceCompatForcedToolChoice(t *testing.T) {
	t.Setenv("ZEN_SPACE_BUNNY_COMPAT", "1")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ToolChoice struct{ Function struct{ Name string } } `json:"tool_choice"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if body.ToolChoice.Function.Name != "bash" {
			t.Errorf("forced tool still %q", body.ToolChoice.Function.Name)
		}
		fmt.Fprint(w, `{"id":"test","choices":[{"message":{"content":"OK"}}]}`)
	}))
	defer upstream.Close()
	cfg := LoadConfig()
	cfg.Upstream = upstream.URL
	gateway := httptest.NewServer(handleResponses(cfg))
	defer gateway.Close()
	req := `{"model":"space-bunny-free","input":"test","tool_choice":{"type":"function","function":{"name":"exec_command"}},"tools":[{"type":"function","name":"exec_command","parameters":{"type":"object","properties":{"cmd":{"type":"string"}}}}]}`
	resp, err := http.Post(gateway.URL, "application/json", strings.NewReader(req))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
}

func TestSpaceProxyDoesNotChangeOtherModels(t *testing.T) {
	t.Setenv("ZEN_SPACE_BUNNY_COMPAT", "1")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"id":"direct","choices":[{"message":{"content":"direct"}}]}`)
	}))
	defer upstream.Close()
	outboundProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"id":"proxy","choices":[{"message":{"content":"proxy"}}]}`)
	}))
	defer outboundProxy.Close()
	t.Setenv("ZEN_SPACE_BUNNY_HTTP_PROXY", outboundProxy.URL)
	cfg := LoadConfig()
	cfg.Upstream = upstream.URL
	gateway := httptest.NewServer(handleResponses(cfg))
	defer gateway.Close()
	for _, item := range []struct{ model, want string }{{"space-bunny-free", "proxy"}, {"another-model", "direct"}} {
		req := fmt.Sprintf(`{"model":%q,"input":"test","tools":[{"type":"function","name":"exec_command","parameters":{"type":"object","properties":{"cmd":{"type":"string"}}}}]}`, item.model)
		resp, err := http.Post(gateway.URL, "application/json", strings.NewReader(req))
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if !strings.Contains(string(b), `"text":"`+item.want+`"`) {
			t.Errorf("model %s routing: %s", item.model, b)
		}
	}
}

func TestSpaceCompactionWithoutToolsUsesTextOnlyCompatibility(t *testing.T) {
	t.Setenv("ZEN_SPACE_BUNNY_COMPAT", "1")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Tools      []ChatTool    `json:"tools"`
			ToolChoice string        `json:"tool_choice"`
			Messages   []ChatMessage `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		found := map[string]bool{}
		for _, tool := range body.Tools {
			found[tool.Function.Name] = true
		}
		if !strings.HasPrefix(r.UserAgent(), "opencode/") || !found["bash"] || !found["read"] {
			w.WriteHeader(403)
			fmt.Fprint(w, `{"error":"FreeTierError"}`)
			return
		}
		if body.ToolChoice != "none" {
			t.Error("compaction must not invoke tools")
		}
		for _, m := range body.Messages {
			for _, c := range m.ToolCalls {
				if c.Function.Name == "exec_command" {
					t.Error("historical tool not translated")
				}
			}
		}
		fmt.Fprint(w, `{"id":"summary","choices":[{"message":{"content":"Progress: tests passed. Next: continue review."}}]}`)
	}))
	defer upstream.Close()
	cfg := LoadConfig()
	cfg.Upstream = upstream.URL
	gateway := httptest.NewServer(handleResponses(cfg))
	defer gateway.Close()
	req := `{"model":"space-bunny-free","tools":[],"input":[{"type":"function_call","call_id":"old","name":"exec_command","arguments":"{\"cmd\":\"git status\"}"},{"type":"function_call_output","call_id":"old","output":"clean"},{"type":"message","role":"user","content":[{"type":"input_text","text":"You are performing a CONTEXT CHECKPOINT COMPACTION. Create a handoff summary."}]}]}`
	resp, err := http.Post(gateway.URL, "application/json", strings.NewReader(req))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !strings.Contains(string(b), "Progress: tests passed") {
		t.Fatalf("compaction failed: %d %s", resp.StatusCode, b)
	}
}

func TestSpaceCompactionRejectsUnexpectedToolCalls(t *testing.T) {
	body := `{"id":"bad","choices":[{"message":{"tool_calls":[{"id":"bad","type":"function","function":{"name":"read","arguments":"{\"filePath\":\"/tmp/not-to-be-read\"}"}}]}}]}`
	rec := httptest.NewRecorder()
	responsesNonStreaming(rec, spaceResponseBody(io.NopCloser(strings.NewReader(body)), false, true), "space-bunny-free")
	if rec.Code != 502 || strings.Contains(rec.Body.String(), `"type":"function_call"`) {
		t.Fatalf("tool call escaped text-only compaction: %d %s", rec.Code, rec.Body.String())
	}
	stream := "data: {\"id\":\"bad\",\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"bad\",\"function\":{\"name\":\"read\",\"arguments\":\"{}\"}}]}}]}\n\ndata: [DONE]\n\n"
	rec = httptest.NewRecorder()
	responsesStreaming(rec, spaceResponseBody(io.NopCloser(strings.NewReader(stream)), true, true), "space-bunny-free")
	if !strings.Contains(rec.Body.String(), "response.failed") || strings.Contains(rec.Body.String(), "response.function_call_arguments") {
		t.Fatalf("streamed tool escaped compaction: %s", rec.Body.String())
	}
}
