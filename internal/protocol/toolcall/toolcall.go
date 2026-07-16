package toolcall

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"
)

var nonName = regexp.MustCompile(`[^a-z0-9_]+`)

var required = map[string][]string{
	"read":          {"file_path"},
	"write":         {"file_path", "content"},
	"edit":          {"file_path", "old_string", "new_string"},
	"update":        {"file_path", "old_string", "new_string"},
	"strreplace":    {"file_path", "old_string", "new_string"},
	"str_replace":   {"file_path", "old_string", "new_string"},
	"stringreplace": {"file_path", "old_string", "new_string"},
	"replace":       {"file_path", "old_string", "new_string"},
	"multiedit":     {"file_path", "edits"},
	"notebookedit":  {"notebook_path", "new_source"},
	"bash":          {"command"},
	"shell":         {"command"},
	"grep":          {"pattern"},
	"glob":          {"pattern"},
	"webfetch":      {"url"},
	"websearch":     {"query"},
	"web_search":    {"query"},
}

var emptyStringOK = map[string]bool{
	"new_string": true,
	"new_source": true,
	"content":    true,
}

var aliases = map[string]string{
	"path": "file_path", "filepath": "file_path", "file": "file_path", "filename": "file_path",
	"target_file": "file_path", "targetfile": "file_path", "targetpath": "file_path", "target_path": "file_path", "file_name": "file_path",
	"oldstring": "old_string", "oldstr": "old_string", "oldtext": "old_string", "old": "old_string", "old_text": "old_string", "original": "old_string", "original_text": "old_string",
	"newstring": "new_string", "newstr": "new_string", "newtext": "new_string", "new": "new_string", "new_text": "new_string", "replacement": "new_string", "replace_with": "new_string",
	"contents": "content", "filecontent": "content", "file_content": "content", "filecontents": "content",
	"notebookpath": "notebook_path", "notebook": "notebook_path",
	"cmd": "command", "shell_command": "command",
	"q": "query", "search": "query", "search_query": "query",
	"uri": "url", "href": "url", "regex": "pattern", "glob_pattern": "pattern",
}

var editAliases = map[string]bool{
	"update": true, "strreplace": true, "str_replace": true, "stringreplace": true,
	"string_replace": true, "fileedit": true, "file_edit": true, "replace": true,
	"strreplaceeditor": true, "str_replace_editor": true,
	"strreplacebasededittool": true, "str_replace_based_edit_tool": true,
}

var protectedNames = map[string]bool{
	"taskupdate": true, "taskcreate": true, "taskget": true, "tasklist": true,
	"taskoutput": true, "taskstop": true, "todowrite": true, "todoread": true,
}

func nameKey(name string) string {
	return nonName.ReplaceAllString(strings.ToLower(strings.TrimSpace(name)), "")
}

func CanonicalName(name string, allowed []string) string {
	raw := strings.TrimSpace(name)
	key := nameKey(raw)
	if raw == "" || key == "" || protectedNames[key] {
		return raw
	}
	byKey := make(map[string]string, len(allowed))
	for _, item := range allowed {
		item = strings.TrimSpace(item)
		if k := nameKey(item); k != "" {
			if _, exists := byKey[k]; !exists {
				byKey[k] = item
			}
		}
	}
	if exact, ok := byKey[key]; ok {
		return exact
	}
	if !editAliases[key] {
		return raw
	}
	if edit, ok := byKey["edit"]; ok {
		return edit
	}
	for _, alternative := range []string{"update", "strreplace", "str_replace", "stringreplace"} {
		if advertised, ok := byKey[alternative]; ok {
			return advertised
		}
	}
	return "Edit"
}

func requiredKeys(name string) []string {
	key := nameKey(name)
	if editAliases[key] {
		key = "edit"
	}
	if keys, ok := required[key]; ok {
		return keys
	}
	for short, keys := range required {
		if strings.HasSuffix(key, "_"+short) || strings.HasSuffix(key, "__"+short) {
			return keys
		}
	}
	return nil
}

func canonicalArgKey(key string) string {
	raw := strings.TrimSpace(key)
	folded := nonName.ReplaceAllString(strings.ToLower(raw), "")
	alnum := strings.ReplaceAll(folded, "_", "")
	if alias, ok := aliases[folded]; ok {
		return alias
	}
	if alias, ok := aliases[alnum]; ok {
		return alias
	}
	return raw
}

type chosenValue struct {
	value     any
	canonical bool
}

func NormalizeObject(input map[string]any) map[string]any {
	chosen := make(map[string]chosenValue, len(input))
	for raw, value := range input {
		canonical := canonicalArgKey(raw)
		current, exists := chosen[canonical]
		if !exists {
			chosen[canonical] = chosenValue{value: value, canonical: raw == canonical}
			continue
		}
		oldEmpty, newEmpty := empty(current.value), empty(value)
		if oldEmpty && !newEmpty {
			chosen[canonical] = chosenValue{value: value, canonical: raw == canonical}
			continue
		}
		if newEmpty {
			continue
		}
		if equal(current.value, value) {
			if raw == canonical && !current.canonical {
				chosen[canonical] = chosenValue{value: value, canonical: true}
			}
			continue
		}
		chosen[canonical] = chosenValue{value: value, canonical: raw == canonical}
	}
	out := make(map[string]any, len(chosen))
	for key, value := range chosen {
		out[key] = value.value
	}
	return out
}

func NormalizeJSON(raw string, toolName string) string {
	text := strings.TrimSpace(raw)
	if text == "" || (text[0] != '{' && text[0] != '[') {
		return raw
	}
	if text[0] != '{' {
		return raw
	}
	pairs, err := decodeObjectPairs(text)
	if err != nil {
		return raw
	}
	chosen := make(map[string]chosenValue, len(pairs))
	order := make([]string, 0, len(pairs))
	for _, pair := range pairs {
		canonical := canonicalArgKey(pair.key)
		current, exists := chosen[canonical]
		if !exists {
			order = append(order, canonical)
			chosen[canonical] = chosenValue{value: pair.value, canonical: pair.key == canonical}
			continue
		}
		oldEmpty, newEmpty := empty(current.value), empty(pair.value)
		if oldEmpty && !newEmpty {
			chosen[canonical] = chosenValue{value: pair.value, canonical: pair.key == canonical}
			continue
		}
		if newEmpty {
			continue
		}
		if equal(current.value, pair.value) {
			if pair.key == canonical && !current.canonical {
				chosen[canonical] = chosenValue{value: pair.value, canonical: true}
			}
			continue
		}
		// JSON object member order is significant for this compatibility repair:
		// later conflicting aliases represent a later authoritative rewrite.
		chosen[canonical] = chosenValue{value: pair.value, canonical: pair.key == canonical}
	}
	var output bytes.Buffer
	output.WriteByte('{')
	for index, key := range order {
		if index > 0 {
			output.WriteByte(',')
		}
		encodedKey, _ := json.Marshal(key)
		encodedValue, err := json.Marshal(chosen[key].value)
		if err != nil {
			return raw
		}
		output.Write(encodedKey)
		output.WriteByte(':')
		output.Write(encodedValue)
	}
	output.WriteByte('}')
	return output.String()
}

type objectPair struct {
	key   string
	value any
}

func decodeObjectPairs(text string) ([]objectPair, error) {
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.UseNumber()
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return nil, err
	}
	pairs := make([]objectPair, 0)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, ok := token.(string)
		if !ok {
			return nil, errors.New("JSON object key is not a string")
		}
		var value any
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		pairs = append(pairs, objectPair{key: key, value: value})
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return nil, errors.New("trailing JSON value")
	} else if !errors.Is(err, io.EOF) {
		return nil, err
	}
	return pairs, nil
}

func EffectiveJSON(raw string, toolName string) string {
	text := strings.TrimSpace(NormalizeJSON(raw, toolName))
	if text != "" {
		return text
	}
	if len(requiredKeys(toolName)) > 0 {
		return ""
	}
	return "{}"
}

func CompleteJSON(raw string, toolName string) bool {
	text := strings.TrimSpace(NormalizeJSON(raw, toolName))
	if text == "" || (text[0] != '{' && text[0] != '[') {
		return false
	}
	var value any
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return false
	}
	if decoder.More() {
		return false
	}
	switch typed := value.(type) {
	case []any:
		return len(typed) > 0 || len(requiredKeys(toolName)) == 0
	case map[string]any:
		keys := requiredKeys(toolName)
		if len(typed) == 0 {
			return len(keys) == 0
		}
		for _, key := range keys {
			item, ok := typed[key]
			if !ok || item == nil {
				return false
			}
			switch v := item.(type) {
			case string:
				if strings.TrimSpace(v) == "" && !emptyStringOK[key] {
					return false
				}
			case []any:
				if len(v) == 0 {
					return false
				}
			case map[string]any:
				if len(v) == 0 {
					return false
				}
			}
		}
		return true
	default:
		return false
	}
}

func Merge(current, incoming, toolName string) string {
	cur, next := strings.TrimSpace(current), strings.TrimSpace(incoming)
	if next == "" {
		return NormalizeJSON(cur, toolName)
	}
	if cur == "" {
		return NormalizeJSON(next, toolName)
	}
	if next == cur || strings.HasPrefix(cur, next) {
		return NormalizeJSON(cur, toolName)
	}
	if strings.HasPrefix(next, cur) {
		return NormalizeJSON(next, toolName)
	}
	if CompleteJSON(next, toolName) {
		return NormalizeJSON(next, toolName)
	}
	return NormalizeJSON(cur+next, toolName)
}

func empty(value any) bool {
	switch v := value.(type) {
	case nil:
		return true
	case string:
		return v == ""
	case []any:
		return len(v) == 0
	case map[string]any:
		return len(v) == 0
	default:
		return false
	}
}

func equal(left, right any) bool {
	a, errA := json.Marshal(left)
	b, errB := json.Marshal(right)
	return errA == nil && errB == nil && bytes.Equal(a, b)
}
