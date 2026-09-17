package helps

import (
	"bytes"
	"testing"

	"github.com/tidwall/gjson"
)

func TestXAIWebSearchAliasesPreserveHostedTools(t *testing.T) {
	native := []byte(`{"tools":[{"type":"web_search"}],"tool_choice":{"type":"web_search"}}`)
	if got, alias := AliasXAIClientWebSearch(native); alias != "" || !bytes.Equal(got, native) {
		t.Fatalf("hosted-only request changed: %s, alias=%q", got, alias)
	}
	mixed := []byte(`{"tools":[{"type":"function","name":"web_search"},{"type":"web_search"}],"tool_choice":{"type":"allowed_tools","tools":[{"type":"function","name":"web_search"},{"type":"web_search"}]},"input":[{"role":"user","content":"web_search"}]}`)
	got, alias := AliasXAIClientWebSearch(mixed)
	if alias != "operax_web_search" || gjson.GetBytes(got, "tool_choice.tools.0.name").String() != alias {
		t.Fatalf("allowed function choice not aliased consistently: %s", got)
	}
	if gjson.GetBytes(got, "tools.1.type").String() != "web_search" || gjson.GetBytes(got, "tool_choice.tools.1.type").String() != "web_search" || gjson.GetBytes(got, "input.0.content").String() != "web_search" {
		t.Fatalf("hosted tool or user content changed: %s", got)
	}
	for _, data := range [][]byte{
		[]byte(`{"type":"response.output_item.done","item":{"type":"web_search_call","status":"completed"}}`),
		[]byte(`{"type":"response.completed","response":{"output":[{"type":"web_search_call","status":"completed"}]}}`),
	} {
		if got := RestoreXAIClientWebSearch(data, alias); !bytes.Equal(got, data) || XAIHasUnfinishedWebSearch(got) {
			t.Fatalf("completed hosted search changed or rejected: %s", got)
		}
	}
}
