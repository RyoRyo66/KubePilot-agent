package platform

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"kubeagent/internal/core"
)

// Documents are saved separately from frequently polled incident state.
type knowledgeLibrary struct {
	mu     sync.RWMutex
	store  *encryptedStore
	custom []core.KnowledgeDocument
	index  *core.KnowledgeIndex
}

func openKnowledgeLibrary(dir string, platform *encryptedStore) (*knowledgeLibrary, error) {
	k := &knowledgeLibrary{store: &encryptedStore{path: filepath.Join(dir, "knowledge.enc"), aead: platform.aead}}
	if err := k.store.loadInto(&k.custom); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	k.index = core.NewKnowledgeIndex(append(core.BuiltinDocuments(), k.custom...))
	return k, nil
}

func (k *knowledgeLibrary) snapshot() *core.KnowledgeIndex {
	k.mu.RLock()
	defer k.mu.RUnlock()
	return k.index
}

func (k *knowledgeLibrary) documents() []core.KnowledgeDocument {
	k.mu.RLock()
	defer k.mu.RUnlock()
	return append(core.BuiltinDocuments(), k.custom...)
}

// Replace the in-memory snapshot only after its encrypted file has committed.
func (k *knowledgeLibrary) commit(next []core.KnowledgeDocument) error {
	if err := k.store.save(next); err != nil {
		return err
	}
	k.custom = next
	k.index = core.NewKnowledgeIndex(append(core.BuiltinDocuments(), next...))
	return nil
}

func (k *knowledgeLibrary) add(title, content string) error {
	title = strings.TrimSpace(title)
	content = strings.TrimSpace(strings.ReplaceAll(content, "\r\n", "\n"))
	control := func(r rune) bool { return unicode.IsControl(r) && r != '\n' && r != '\t' }
	if title == "" || utf8.RuneCountInString(title) > 100 || strings.IndexFunc(title, unicode.IsControl) >= 0 {
		return errors.New("资料名称须为 1–100 个字符的单行文字")
	}
	if content == "" || len(content) > 32<<10 || strings.IndexFunc(content, control) >= 0 {
		return errors.New("请提供不超过 32 KiB 的 Markdown 或纯文本正文")
	}
	doc := core.KnowledgeDocument{ID: "doc-" + randomID()[:16], Title: title, Content: content}
	index := core.NewKnowledgeIndex([]core.KnowledgeDocument{doc})
	if n := index.PassageCount(); n == 0 || n > 96 {
		return errors.New("每份资料须包含正文，最多支持 96 个段落分块")
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if len(k.custom) >= 16 {
		return errors.New("当前工作空间最多可导入 16 份资料")
	}
	for _, d := range k.custom {
		if strings.EqualFold(d.Title, title) {
			return errors.New("已存在同名资料，可移除旧版后重新导入")
		}
	}
	return k.commit(append(append([]core.KnowledgeDocument(nil), k.custom...), doc))
}

func (k *knowledgeLibrary) remove(id string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	for i, d := range k.custom {
		if d.ID == id {
			next := append([]core.KnowledgeDocument(nil), k.custom[:i]...)
			return k.commit(append(next, k.custom[i+1:]...))
		}
	}
	return errors.New("未找到可移除的工作空间资料")
}

func (k *knowledgeLibrary) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/platform/knowledge", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, 200, k.documents())
	})
	mux.HandleFunc("GET /api/platform/knowledge/search", func(w http.ResponseWriter, r *http.Request) {
		query := strings.TrimSpace(r.URL.Query().Get("q"))
		if query == "" || utf8.RuneCountInString(query) > 240 {
			bad(w, errors.New("请输入 1–240 个字符的故障现象或关键词"))
			return
		}
		jsonResponse(w, 200, k.snapshot().Search(query))
	})
	mux.HandleFunc("POST /api/platform/knowledge", func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Title   string `json:"title"`
			Content string `json:"content"`
		}
		if err := readJSON(w, r, &b, true); err != nil {
			bad(w, err)
			return
		}
		if err := k.add(b.Title, b.Content); err != nil {
			bad(w, err)
			return
		}
		jsonResponse(w, 201, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /api/platform/knowledge/{id}/remove", func(w http.ResponseWriter, r *http.Request) {
		if err := k.remove(r.PathValue("id")); err != nil {
			bad(w, err)
			return
		}
		jsonResponse(w, 200, map[string]bool{"ok": true})
	})
}
