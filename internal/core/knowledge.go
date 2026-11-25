package core

import (
	"crypto/sha256"
	"encoding/hex"
	"math"
	"sort"
	"strings"
	"unicode"
)

// KnowledgeDocument belongs to the reference library, separate from repair rules.
type KnowledgeDocument struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Content string `json:"content"`
	Source  string `json:"source,omitempty"`
}

type Passage struct {
	DocumentID string `json:"documentId"`
	Title      string `json:"title"`
	Section    string `json:"section,omitempty"`
	Version    string `json:"version"`
	Source     string `json:"source,omitempty"`
	Text       string `json:"text"`
}

func BuiltinDocuments() []KnowledgeDocument {
	docs := make([]KnowledgeDocument, 0, len(Runbooks)+2)
	for _, book := range Runbooks {
		docs = append(docs, KnowledgeDocument{"kb-" + book.Type, book.Title + " · " + book.Type, book.Conclusion + "\n" + strings.Join(book.Checks, "\n"), book.Source})
	}
	return append(docs,
		KnowledgeDocument{"kb-rollout", "发布超时 · ProgressDeadlineExceeded", "Deployment 报告 RolloutFailure 或超过发布期限时，先对照期望副本、可用副本和所属 ReplicaSet。若尚未创建 Pod，优先查看 ReplicaSet 的事件。若已有 Pod，核对就绪条件、容器状态和近期事件。单凭发布超时无法定位根因。", ""},
		KnowledgeDocument{"kb-quota", "副本创建被拒 · FailedCreate", "ReplicaSet 的 FailedCreate 事件若包含 exceeded quota，记录被拒的资源、请求量、已用量及配额上限。按事件中的资源名区分 CPU、内存和对象数配额。结合当前命名空间的 ResourceQuota 核对，缺少权限时在诊断中列出待确认项。", ""},
	)
}

type indexedPassage struct {
	p      Passage
	tf     map[string]int
	length int
}

// KnowledgeIndex is immutable after construction and safe for concurrent searches.
type KnowledgeIndex struct {
	rows    []indexedPassage
	df      map[string]int
	average float64
}

func (idx *KnowledgeIndex) PassageCount() int { return len(idx.rows) }

func terms(s string) []string {
	var result []string
	var word []rune
	flush := func() {
		if len(word) > 0 {
			result = append(result, string(word))
			word = word[:0]
		}
	}
	var han rune
	for _, r := range strings.ToLower(s) {
		if unicode.Is(unicode.Han, r) {
			flush()
			if han != 0 {
				result = append(result, string([]rune{han, r}))
			}
			han = r
		} else {
			han = 0
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				word = append(word, r)
			} else {
				flush()
			}
		}
	}
	flush()
	return result
}

func documentPassages(doc KnowledgeDocument) []Passage {
	sum := sha256.Sum256([]byte(doc.Title + "\n" + doc.Content))
	version := hex.EncodeToString(sum[:6])
	var passages []Passage
	var section string
	var paragraph []rune
	flush := func() {
		body := strings.TrimSpace(string(paragraph))
		if body != "" {
			passages = append(passages, Passage{doc.ID, doc.Title, section, version, doc.Source, body})
		}
		paragraph = paragraph[:0]
	}
	for _, line := range strings.Split(doc.Content, "\n") {
		line = strings.TrimSpace(line)
		trimmed := strings.TrimLeft(line, "#")
		if n := len(line) - len(trimmed); n >= 1 && n <= 6 && strings.HasPrefix(trimmed, " ") {
			flush()
			section = string(limitRunes(strings.TrimSpace(trimmed), 100))
			continue
		}
		if line == "" {
			continue
		}
		lineRunes := []rune(line)
		if len(paragraph) > 0 {
			if len(paragraph)+len(lineRunes) >= 480 {
				flush()
			} else {
				paragraph = append(paragraph, '\n')
			}
		}
		for len(lineRunes) > 480 {
			paragraph = append(paragraph, lineRunes[:480]...)
			flush()
			lineRunes = lineRunes[480:]
		}
		paragraph = append(paragraph, lineRunes...)
	}
	flush()
	return passages
}

func limitRunes(s string, n int) []rune {
	r := []rune(s)
	if len(r) > n {
		return r[:n]
	}
	return r
}

func NewKnowledgeIndex(docs []KnowledgeDocument) *KnowledgeIndex {
	idx := &KnowledgeIndex{df: make(map[string]int)}
	for _, doc := range docs {
		for _, p := range documentPassages(doc) {
			row := indexedPassage{p: p, tf: make(map[string]int)}
			for _, field := range []struct {
				text   string
				weight int
			}{{p.Title, 3}, {p.Section, 2}, {p.Text, 1}} {
				for _, term := range terms(field.text) {
					row.tf[term] += field.weight
					row.length += field.weight
				}
			}
			for term := range row.tf {
				idx.df[term]++
			}
			idx.rows = append(idx.rows, row)
			idx.average += float64(row.length)
		}
	}
	if len(idx.rows) > 0 {
		idx.average /= float64(len(idx.rows))
	}
	return idx
}

// Search ranks matching passages by BM25, using title and heading boosts.
func (idx *KnowledgeIndex) Search(query string) []Passage {
	matches := []Passage{}
	if idx.average == 0 {
		return matches
	}
	words := make(map[string]bool)
	for _, term := range terms(query) {
		if idx.df[term] > 0 {
			words[term] = true
		}
	}
	if len(words) == 0 {
		return matches
	}
	// Stable term order keeps scores deterministic across calls.
	keys := make([]string, 0, len(words))
	for term := range words {
		keys = append(keys, term)
	}
	sort.Strings(keys)
	type candidate struct {
		i     int
		score float64
	}
	var ranked []candidate
	for i, row := range idx.rows {
		score := 0.0
		norm := 1.2 * (0.25 + 0.75*float64(row.length)/idx.average)
		for _, key := range keys {
			tf := float64(row.tf[key])
			if tf == 0 {
				continue
			}
			idf := math.Log1p((float64(len(idx.rows)-idx.df[key]) + 0.5) / (float64(idx.df[key]) + 0.5))
			score += idf * tf * 2.2 / (tf + norm)
		}
		if score > 0 {
			ranked = append(ranked, candidate{i, score})
		}
	}
	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].score > ranked[j].score })
	for _, c := range ranked {
		if c.score < ranked[0].score*0.22 {
			break
		}
		matches = append(matches, idx.rows[c.i].p)
		if len(matches) == 3 {
			break
		}
	}
	return matches
}

func IncidentKnowledgeQuery(i Incident) string {
	var q strings.Builder
	q.WriteString(i.Type + " " + i.Title + " " + i.Target.Name)
	for _, e := range i.Evidence {
		q.WriteString("\n" + e.Summary + " " + string(limitRunes(e.Detail, 1600)))
	}
	return q.String()
}
