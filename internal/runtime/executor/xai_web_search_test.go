package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

// The pending hosted-search case reproduces the production response shape:
// public progress text, no function calls, then completed with a pending search.
func xaiWebSearchTestEvents(name, hostedStatus string) []string {
	events := []string{`{"type":"response.created","response":{"id":"resp_search","created_at":1}}`}
	var item string
	if hostedStatus != "" {
		events = append(events, `{"type":"response.output_text.delta","output_index":0,"delta":"I will check the sources."}`)
		item = fmt.Sprintf(`{"id":"ws_search","type":"web_search_call","status":%q,"action":{"type":"search","query":"Polymarket Steam login","sources":[]}}`, hostedStatus)
	} else {
		item = fmt.Sprintf(`{"id":"fc_search","type":"function_call","call_id":"call_search","name":%q,"arguments":"{\"query\":\"web_search\"}","status":"completed"}`, name)
		events = append(events, `{"type":"response.output_item.added","output_index":0,"item":`+item+`}`)
		events = append(events, `{"type":"response.function_call_arguments.done","item_id":"fc_search","output_index":0,"arguments":"{\"query\":\"web_search\"}"}`)
		events = append(events, `{"type":"response.output_item.done","output_index":0,"item":`+item+`}`)
	}
	events = append(events, `{"type":"response.completed","response":{"id":"resp_search","status":"completed","output":[`+item+`],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}}`)
	return events
}

func TestXAIClientWebSearchRoundTrip(t *testing.T) {
	for _, format := range []sdktranslator.Format{sdktranslator.FormatOpenAI, sdktranslator.FormatOpenAIResponse} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", format, stream), func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, errRead := io.ReadAll(r.Body)
					if errRead != nil {
						t.Error(errRead)
						return
					}
					for _, path := range []string{"tools.0.name", "input.#(type==function_call).name", "tool_choice.name"} {
						if got := gjson.GetBytes(body, path).String(); got != "operax_web_search_2" {
							t.Errorf("upstream %s = %q, want collision-free alias", path, got)
						}
					}
					if got := gjson.GetBytes(body, "input.#(type==function_call).arguments").String(); got != `{"query":"web_search"}` {
						t.Errorf("history arguments changed: %s", got)
					}
					if got := gjson.GetBytes(body, "tools.1.name").String(); got != "operax_web_search" {
						t.Errorf("unrelated function changed: %q", got)
					}
					w.Header().Set("Content-Type", "text/event-stream")
					for _, event := range xaiWebSearchTestEvents("operax_web_search_2", "") {
						_, _ = fmt.Fprintf(w, "data: %s\n\n", event)
					}
				}))
				defer server.Close()
				payload := []byte(`{"model":"grok-4.6","input":[{"role":"user","content":"Search"},{"type":"function_call","call_id":"old","name":"web_search","arguments":"{\"query\":\"web_search\"}"},{"type":"function_call_output","call_id":"old","output":"Earlier result"}],"tools":[{"type":"function","name":"web_search","parameters":{"type":"object"}},{"type":"function","name":"operax_web_search","parameters":{"type":"object"}}],"tool_choice":{"type":"function","name":"web_search"}}`)
				if format == sdktranslator.FormatOpenAI {
					payload = []byte(`{"model":"grok-4.6","messages":[{"role":"user","content":"Search"},{"role":"assistant","tool_calls":[{"id":"old","type":"function","function":{"name":"web_search","arguments":"{\"query\":\"web_search\"}"}}]},{"role":"tool","tool_call_id":"old","content":"Earlier result"}],"tools":[{"type":"function","function":{"name":"web_search","parameters":{"type":"object"}}},{"type":"function","function":{"name":"operax_web_search","parameters":{"type":"object"}}}],"tool_choice":{"type":"function","function":{"name":"web_search"}}}`)
				}
				exec := NewXAIExecutor(&config.Config{})
				auth := &cliproxyauth.Auth{Provider: "xai", Attributes: map[string]string{"base_url": server.URL}, Metadata: map[string]any{"access_token": "test-token"}}
				req := cliproxyexecutor.Request{Model: "grok-4.6", Payload: payload}
				opts := cliproxyexecutor.Options{SourceFormat: format, Stream: stream}
				var output bytes.Buffer
				if stream {
					result, err := exec.ExecuteStream(context.Background(), auth, req, opts)
					if err != nil {
						t.Fatal(err)
					}
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							t.Fatal(chunk.Err)
						}
						output.Write(chunk.Payload)
					}
				} else {
					result, err := exec.Execute(context.Background(), auth, req, opts)
					if err != nil {
						t.Fatal(err)
					}
					output.Write(result.Payload)
				}
				if strings.Contains(output.String(), "operax_web_search") || !strings.Contains(output.String(), `"name":"web_search"`) {
					t.Fatalf("client function name was not restored: %s", output.String())
				}
				if format == sdktranslator.FormatOpenAI && !strings.Contains(output.String(), `"finish_reason":"tool_calls"`) {
					t.Fatalf("expected tool_calls continuation: %s", output.String())
				}
			})
		}
	}
}

func TestXAIWebSearchCompletionHTTP(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, status := range []string{"in_progress", "completed"} {
			t.Run(fmt.Sprintf("stream=%v/status=%s", stream, status), func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					for _, event := range xaiWebSearchTestEvents("", status) {
						_, _ = fmt.Fprintf(w, "data: %s\n\n", event)
					}
				}))
				defer server.Close()
				exec := NewXAIExecutor(&config.Config{})
				auth := &cliproxyauth.Auth{Provider: "xai", Attributes: map[string]string{"base_url": server.URL}}
				req := cliproxyexecutor.Request{Model: "grok-4.6", Payload: []byte(`{"model":"grok-4.6","messages":[{"role":"user","content":"Search"}],"tools":[{"type":"function","function":{"name":"web_search","parameters":{"type":"object"}}}]}`)}
				opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, Stream: stream}
				var output bytes.Buffer
				var resultErr error
				if stream {
					result, err := exec.ExecuteStream(context.Background(), auth, req, opts)
					if err != nil {
						t.Fatal(err)
					}
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							resultErr = chunk.Err
						}
						output.Write(chunk.Payload)
					}
				} else {
					result, err := exec.Execute(context.Background(), auth, req, opts)
					resultErr = err
					output.Write(result.Payload)
				}
				if status == "completed" {
					if resultErr != nil {
						t.Fatalf("completed hosted search rejected: %v", resultErr)
					}
					return
				}
				assertXAIUnfinishedSearchError(t, resultErr, output.String())
			})
		}
	}
}

func TestXAIWebSearchWebsocket(t *testing.T) {
	for _, status := range []string{"", "in_progress"} {
		t.Run("hosted-status="+status, func(t *testing.T) {
			upgrader := websocket.Upgrader{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, errUpgrade := upgrader.Upgrade(w, r, nil)
				if errUpgrade != nil {
					t.Error(errUpgrade)
					return
				}
				defer func() { _ = conn.Close() }()
				_, body, errRead := conn.ReadMessage()
				if errRead != nil {
					t.Error(errRead)
					return
				}
				if got := gjson.GetBytes(body, "tools.0.name").String(); got != "operax_web_search" {
					t.Errorf("upstream websocket tool name = %q", got)
				}
				for _, event := range xaiWebSearchTestEvents("operax_web_search", status) {
					if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(event)); errWrite != nil {
						t.Error(errWrite)
						return
					}
				}
			}))
			defer server.Close()
			exec := NewXAIWebsocketsExecutor(&config.Config{})
			auth := &cliproxyauth.Auth{Provider: "xai", Attributes: map[string]string{"base_url": server.URL, "websockets": "true"}}
			req := cliproxyexecutor.Request{Model: "grok-4.6", Payload: []byte(`{"model":"grok-4.6","input":"Search","tools":[{"type":"function","name":"web_search","parameters":{"type":"object"}}]}`)}
			result, err := exec.ExecuteStream(context.Background(), auth, req, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, Stream: true})
			if err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			var resultErr error
			for chunk := range result.Chunks {
				if chunk.Err != nil {
					resultErr = chunk.Err
				}
				output.Write(chunk.Payload)
			}
			if status != "" {
				assertXAIUnfinishedSearchError(t, resultErr, output.String())
			} else if resultErr != nil || strings.Contains(output.String(), "operax_web_search") || !strings.Contains(output.String(), `"name":"web_search"`) {
				t.Fatalf("websocket function round trip failed: err=%v output=%s", resultErr, output.String())
			}
		})
	}
}

func assertXAIUnfinishedSearchError(t *testing.T, err error, output string) {
	t.Helper()
	status, ok := err.(interface{ StatusCode() int })
	if !ok || status.StatusCode() != http.StatusBadGateway || gjson.Get(err.Error(), "error.code").String() != "incomplete_web_search" {
		t.Fatalf("unfinished search error = %v, want incomplete_web_search / 502", err)
	}
	if strings.Contains(output, `"finish_reason":"stop"`) || strings.Contains(output, `"type":"response.completed"`) {
		t.Fatalf("unfinished search reported success: %s", output)
	}
}
