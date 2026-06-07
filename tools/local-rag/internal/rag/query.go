package rag

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
)

var tokenRE = regexp.MustCompile(`[A-Za-z0-9_./:-]+|[\p{Han}]{2,}`)

func Tokenize(text string) []string {
	stop := map[string]bool{"the": true, "and": true, "or": true, "of": true, "for": true, "有哪些": true, "怎麼": true, "取得": true}
	matches := tokenRE.FindAllString(strings.ToLower(text), -1)
	var terms []string
	for _, term := range matches {
		term = strings.Trim(term, "./:-")
		if len([]rune(term)) >= 2 && !stop[term] {
			terms = append(terms, term)
		}
	}
	return terms
}

func ExpandQuery(query string) string {
	expansions := map[string]string{
		"認證":           "auth authentication credential certificate cert token activation provision provisioning",
		"凭證":           "auth authentication credential certificate cert token activation provision provisioning",
		"组成":           "architecture component service runtime inventory deployment",
		"組成":           "architecture component service runtime inventory deployment",
		"video server": "rtk_video_cloud API media storage WebRTC MQTT Postgres",
		"device":       "device registry activation certificate token transport provisioning",
		"webrtc":       "streaming WebRTC TURN ICE session",
		"mqtt":         "MQTT broker transport EMQX topic",
	}
	expanded := []string{query}
	lower := strings.ToLower(query)
	keys := make([]string, 0, len(expansions))
	for key := range expansions {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, term := range keys {
		if strings.Contains(lower, strings.ToLower(term)) {
			expanded = append(expanded, expansions[term])
		}
	}
	return strings.Join(expanded, " ")
}

func LexicalScore(query, text string) float64 {
	terms := map[string]bool{}
	for _, term := range Tokenize(query) {
		terms[term] = true
	}
	if len(terms) == 0 {
		return 0
	}
	haystack := strings.ToLower(text)
	hits := 0
	for term := range terms {
		if strings.Contains(haystack, term) {
			hits++
		}
	}
	return float64(hits) / float64(len(terms))
}

func AuthorityWeight(classification, layer, path string) float64 {
	weight := 0.0
	switch classification {
	case "source":
		weight += 0.5
	case "reference-only":
		weight -= 0.2
	}
	switch layer {
	case "contracts":
		weight += 1.0
	case "workspace":
		weight += 0.5
	case "service":
		weight += 0.25
	case "generated":
		weight -= 0.25
	}
	if strings.Contains(path, "/rtk_cloud_contracts_doc/") && !strings.HasPrefix(path, "repos/rtk_cloud_contracts_doc/") {
		weight -= 0.5
	}
	return weight
}

func Cosine(left, right []float64) float64 {
	if len(left) == 0 || len(left) != len(right) {
		return 0
	}
	var dot, leftNorm, rightNorm float64
	for i := range left {
		dot += left[i] * right[i]
		leftNorm += left[i] * left[i]
		rightNorm += right[i] * right[i]
	}
	if leftNorm == 0 || rightNorm == 0 {
		return 0
	}
	return dot / (math.Sqrt(leftNorm) * math.Sqrt(rightNorm))
}

func DetectConflicts(results []SearchResult) []Conflict {
	if len(results) < 2 {
		return nil
	}
	limit := min(5, len(results))
	layers := map[string]bool{}
	terms := []string{"legacy", "deprecated", "instead", "before", "only", "must", "should"}
	hasConflictTerm := false
	var paths []string
	for _, result := range results[:limit] {
		layers[result.SourceLayer] = true
		paths = append(paths, result.FilePath)
		lower := strings.ToLower(result.Content)
		for _, term := range terms {
			if strings.Contains(lower, term) {
				hasConflictTerm = true
			}
		}
	}
	if len(layers) > 1 && hasConflictTerm {
		return []Conflict{{
			Type:    "possible_source_mismatch",
			Message: "Multiple source layers mention this topic; verify the canonical contract before treating service-local notes as normative.",
			Paths:   paths,
		}}
	}
	return nil
}

func ComposeAnswer(query string, results []SearchResult, conflicts []Conflict) string {
	if len(results) == 0 {
		return "直接答案\n找不到足夠的本地索引內容回答這個問題。\n\n依據來源\n無。\n\n相關文件\n無。\n\n不確定或衝突\n請先執行 full index 或放寬 filter。"
	}
	top := results[:min(4, len(results))]
	var directLines []string
	var sourceLines []string
	for i, result := range top {
		directLines = append(directLines, "- "+SummarizeChunk(result.Content))
		sourceLines = append(sourceLines, fmt.Sprintf("- [%d] %s:%d-%d (%s, %s, %s)", i+1, result.FilePath, result.LineStart, result.LineEnd, result.SourceLayer, result.DocClassification, short(result.CommitSHA, 12)))
	}
	relatedSet := map[string]bool{}
	for _, result := range results {
		relatedSet[result.FilePath] = true
	}
	var related []string
	for path := range relatedSet {
		related = append(related, path)
	}
	sort.Strings(related)
	if len(related) > 8 {
		related = related[:8]
	}
	conflictText := "- 未偵測到明確衝突；仍應以引用來源中的 canonical/source 文件為準。"
	if len(conflicts) > 0 {
		lines := make([]string, len(conflicts))
		for i, conflict := range conflicts {
			lines[i] = "- " + conflict.Message
		}
		conflictText = strings.Join(lines, "\n")
	}
	return "直接答案\n" + strings.Join(directLines, "\n") +
		"\n\n依據來源\n" + strings.Join(sourceLines, "\n") +
		"\n\n相關文件\n- " + strings.Join(related, "\n- ") +
		"\n\n不確定或衝突\n" + conflictText
}

func SummarizeChunk(content string) string {
	text := regexp.MustCompile(`\s+`).ReplaceAllString(strings.TrimSpace(content), " ")
	text = regexp.MustCompile(`^#+\s*`).ReplaceAllString(text, "")
	if len([]rune(text)) > 260 {
		runes := []rune(text)
		return string(runes[:260]) + "..."
	}
	return text
}

func AnswerPrompt(query string, results []SearchResult, conflicts []Conflict) string {
	var contexts []string
	for i, result := range results[:min(8, len(results))] {
		contexts = append(contexts, fmt.Sprintf("[%d] %s:%d-%d (%s/%s)\n%s", i+1, result.FilePath, result.LineStart, result.LineEnd, result.SourceLayer, result.DocClassification, result.Content))
	}
	conflictJSON, _ := json.Marshal(conflicts)
	return "你是本機 RTK Cloud workspace RAG 助手。只能根據 CONTEXT 回答，必須用繁體中文，並保留四段標題：" +
		"直接答案、依據來源、相關文件、不確定或衝突。不要編造未出現在 CONTEXT 的事實。\n\n" +
		"QUESTION:\n" + query + "\n\nCONTEXT:\n" + strings.Join(contexts, "\n\n") + "\n\nPOSSIBLE_CONFLICTS:\n" + string(conflictJSON)
}

func ConfidenceNotes(results []SearchResult, usedLLM bool) []string {
	notes := []string{"answer_llm=unavailable; used extractive local summary"}
	if usedLLM {
		notes[0] = "answer_llm=openai"
	}
	if len(results) == 0 {
		notes = append(notes, "no_results")
	} else if results[0].SourceLayer == "contracts" {
		notes = append(notes, "top_result_is_canonical_contract")
	} else {
		notes = append(notes, "top_result_is_not_contract; verify against contracts when API behavior matters")
	}
	for _, result := range results {
		if result.SourceLayer == "generated" {
			notes = append(notes, "some_results_are_reference_only_or_copied_docs")
			break
		}
	}
	return notes
}
