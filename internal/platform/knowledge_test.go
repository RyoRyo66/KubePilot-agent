package platform

import (
	"encoding/json"
	"kubeagent/internal/core"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkspaceKnowledgeLifecycle(t *testing.T) {
	dir := t.TempDir()
	h, err := NewHub(dir, HubOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if h != nil {
			h.Close()
		}
	}()
	a := accountTestClient{t: t, handler: h.Handler("")}
	b := accountTestClient{t: t, handler: h.Handler("")}
	const route = "/api/platform/knowledge"
	const query = route + "/search?q=OnlyInWorkspaceAlpha"
	if w := a.call("GET", route, nil); w.Code != 401 {
		t.Fatal("anonymous access")
	}
	a.register("library-alpha")
	aid := a.create()
	b.register("library-beta")
	b.create()
	doc := map[string]string{"title": "Alpha 复盘", "content": "# 启动异常\nOnlyInWorkspaceAlpha 对照启动事件"}
	if w := a.call("POST", route, doc); w.Code != 201 {
		t.Fatal(w.Body)
	}
	if w := a.call("POST", route, doc); w.Code != 409 {
		t.Fatal("duplicate title accepted")
	}
	if w := b.call("GET", query, nil); w.Code != 200 || strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatal("cross-workspace results", w.Body)
	}
	if w := b.call("GET", route, nil); strings.Contains(w.Body.String(), "OnlyInWorkspaceAlpha") {
		t.Fatal("cross-workspace document list")
	}
	raw, err := os.ReadFile(filepath.Join(dir, "workspaces", aid, "knowledge.enc"))
	if err != nil || strings.Contains(string(raw), "OnlyInWorkspaceAlpha") {
		t.Fatal("missing encrypted storage", err)
	}
	h.Close()
	h, err = NewHub(dir, HubOptions{})
	if err != nil {
		t.Fatal(err)
	}
	a.handler = h.Handler("")
	b.handler = h.Handler("")
	w := a.call("GET", query, nil)
	var hits []core.Passage
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &hits) != nil || len(hits) != 1 || hits[0].Section != "启动异常" {
		t.Fatal("restart lost document", w.Body)
	}
	remove := route + "/" + hits[0].DocumentID + "/remove"
	if w := b.call("POST", remove, map[string]string{}); w.Code != 409 {
		t.Fatal("foreign delete accepted")
	}
	if w := a.call("POST", route+"/kb-quota/remove", map[string]string{}); w.Code != 409 {
		t.Fatal("built-in delete accepted")
	}
	if w := a.call("POST", remove, map[string]string{}); w.Code != 200 {
		t.Fatal(w.Body)
	}
	if w := a.call("GET", query, nil); strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatal("stale match after removal")
	}
}
