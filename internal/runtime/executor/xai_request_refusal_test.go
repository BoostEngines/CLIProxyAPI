package executor

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestXAIRequestRefusalDoesNotRotateOrSuspendAccounts(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "http", true: "http-stream"}[stream], func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"error":"I'm sorry, I can't help with that request."}`))
			}))
			defer server.Close()
			manager := cliproxyauth.NewManager(nil, nil, nil)
			manager.SetRetryConfig(2, time.Second, 2)
			manager.RegisterExecutor(NewXAIExecutor(&config.Config{}))
			model := "grok-4.7"
			for _, id := range []string{"refusal-first", "refusal-second"} {
				auth := &cliproxyauth.Auth{ID: id, Provider: "xai", Attributes: map[string]string{"base_url": server.URL, "api_key": "fixture"}}
				registry.GetGlobalRegistry().RegisterClient(id, "xai", []*registry.ModelInfo{{ID: model}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
				if _, err := manager.Register(context.Background(), auth); err != nil {
					t.Fatal(err)
				}
			}
			request := cliproxyexecutor.Request{Model: model, Payload: []byte(`{"model":"grok-4.7","input":"fixture"}`)}
			options := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, Stream: stream}
			for n := int32(1); n <= 2; n++ {
				var err error
				if stream {
					_, err = manager.ExecuteStream(context.Background(), []string{"xai"}, request, options)
				} else {
					_, err = manager.Execute(context.Background(), []string{"xai"}, request, options)
				}
				var scoped cliproxyexecutor.RequestScopedError
				if !errors.As(err, &scoped) || !scoped.IsRequestScoped() {
					t.Fatalf("request refusal was not retained: %v", err)
				}
				if calls.Load() != n {
					t.Fatalf("refusal rotated/retried credentials: got %d calls, want %d", calls.Load(), n)
				}
				for _, id := range []string{"refusal-first", "refusal-second"} {
					auth, _ := manager.GetByID(id)
					if auth.Unavailable || !auth.NextRetryAfter.IsZero() || auth.ModelStates[model] != nil {
						t.Fatalf("refusal cooled account/model: %#v", auth.ModelStates)
					}
				}
			}
		})
	}
}

func TestXAIWebsocketRefusalPreservesRequestScope(t *testing.T) {
	for _, body := range []string{
		`{"type":"error","status":403,"error":"I'm sorry, I can't help with that request."}`,
		`{"type":"error","status":403,"body":{"error":"I'm sorry, I can't help with that request."}}`,
	} {
		err, ok := parseXAIWebsocketError([]byte(body))
		var scoped cliproxyexecutor.RequestScopedError
		if !ok || !errors.As(err, &scoped) || !scoped.IsRequestScoped() {
			t.Fatalf("websocket refusal lost request scope: %v", err)
		}
	}
}
