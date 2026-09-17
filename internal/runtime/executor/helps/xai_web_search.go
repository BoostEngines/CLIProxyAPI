package helps

import (
	"fmt"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// AliasXAIClientWebSearch runs after tool and history normalization. Grok can
// mistake a client function named web_search for its hosted search tool.
func AliasXAIClientWebSearch(body []byte) ([]byte, string) {
	reserved := make(map[string]bool)
	var paths []string
	collect := func(item gjson.Result, path, kind string) {
		if item.Get("type").String() != kind {
			return
		}
		name := item.Get("name").String()
		reserved[name] = true
		if name == "web_search" {
			paths = append(paths, path+".name")
		}
	}
	for index, tool := range gjson.GetBytes(body, "tools").Array() {
		collect(tool, fmt.Sprintf("tools.%d", index), "function")
	}
	for index, item := range gjson.GetBytes(body, "input").Array() {
		collect(item, fmt.Sprintf("input.%d", index), "function_call")
	}
	collect(gjson.GetBytes(body, "tool_choice"), "tool_choice", "function")
	for index, tool := range gjson.GetBytes(body, "tool_choice.tools").Array() {
		collect(tool, fmt.Sprintf("tool_choice.tools.%d", index), "function")
	}
	if len(paths) == 0 {
		return body, ""
	}
	alias := "operax_web_search"
	for suffix := 2; reserved[alias]; suffix++ {
		alias = fmt.Sprintf("operax_web_search_%d", suffix)
	}
	original := body
	for _, path := range paths {
		updated, errSet := sjson.SetBytes(body, path, alias)
		if errSet != nil {
			return original, ""
		}
		body = updated
	}
	return body, alias
}

// RestoreXAIClientWebSearch restores names before protocol translation and
// reasoning replay caching. Arguments, call IDs and hosted tools stay intact.
func RestoreXAIClientWebSearch(data []byte, alias string) []byte {
	if alias == "" {
		return data
	}
	restore := func(path string) {
		item := gjson.GetBytes(data, path)
		if item.Get("type").String() == "function_call" && item.Get("name").String() == alias {
			if updated, errSet := sjson.SetBytes(data, path+".name", "web_search"); errSet == nil {
				data = updated
			}
		}
	}
	restore("item")
	for index := range gjson.GetBytes(data, "response.output").Array() {
		restore(fmt.Sprintf("response.output.%d", index))
	}
	return data
}

// XAIHasUnfinishedWebSearch rejects the observed completed response containing
// pending hosted searches. Never turn these items into client function calls:
// a real hosted search may already have executed upstream.
func XAIHasUnfinishedWebSearch(data []byte) bool {
	if gjson.GetBytes(data, "type").String() != "response.completed" {
		return false
	}
	for _, item := range gjson.GetBytes(data, "response.output").Array() {
		if item.Get("type").String() == "web_search_call" {
			switch item.Get("status").String() {
			case "in_progress", "searching":
				return true
			}
		}
	}
	return false
}
