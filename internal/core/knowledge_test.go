package core

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestKnowledgeSearchAndChunking(t *testing.T) {
	docs := append(BuiltinDocuments(), KnowledgeDocument{ID: "doc-test", Title: "支付服务复盘", Content: "# 启动协议\nPayInitTimeout 启动握手超时。\n# 票据过期\n" + strings.Repeat("签发票据", 260)})
	idx := NewKnowledgeIndex(docs)
	for q, section := range map[string]string{"payinittimeout": "启动协议", "握手超时": "启动协议", "签发票据": "票据过期"} {
		got := idx.Search(q)
		if len(got) == 0 || got[0].DocumentID != "doc-test" || got[0].Section != section || len(got) > 3 {
			t.Fatalf("%s: %+v", q, got)
		}
		for _, p := range got {
			if utf8.RuneCountInString(p.Text) > 480 || p.Version == "" {
				t.Fatal("invalid reference snapshot")
			}
		}
	}
	if got := idx.Search("unrelatedxyz"); len(got) != 0 {
		t.Fatalf("unrelated results: %+v", got)
	}
}
