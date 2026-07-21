package trae

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func buildTraeToolShimInstructions(openaiReq []byte) string {
	tools := gjson.GetBytes(openaiReq, "tools")
	if !tools.Exists() || !tools.IsArray() {
		return ""
	}

	var lines []string
	hasBash := false
	hasRead := false
	for _, tool := range tools.Array() {
		name := firstNonEmpty(
			tool.Get("function.name").String(),
			tool.Get("name").String(),
		)
		if name == "" {
			continue
		}
		switch name {
		case "Bash":
			hasBash = true
		case "Read":
			hasRead = true
		}
		description := firstNonEmpty(
			tool.Get("function.description").String(),
			tool.Get("description").String(),
			"No description",
		)
		parameters := firstNonEmpty(
			tool.Get("function.parameters").Raw,
			tool.Get("input_schema").Raw,
			`{"type":"object","properties":{}}`,
		)
		lines = append(lines, fmt.Sprintf("- %s: %s parameters=%s", name, description, parameters))
	}
	if len(lines) == 0 {
		return ""
	}

	instructions := []string{
		"External tool protocol:",
		"The following client-provided tools are available. If the user asks for information that requires one of these tools, call the matching tool before answering.",
		"To call a tool, output exactly one line in this format and no other text: tool_calls=[{\"name\":\"tool_name\",\"arguments\":{\"key\":\"value\"}}]",
		"Do not simulate tool execution in prose. Do not print shell command fences, command output, <textarea> blocks, or pretend tool results.",
		"After emitting tool_calls=..., stop immediately and wait for the client to execute the tool.",
	}
	if hasRead {
		instructions = append(instructions, "For file reads, call Read with an absolute file_path argument instead of printing cat output.")
	}
	if hasBash {
		instructions = append(instructions, "For shell commands, call Bash with a command argument instead of printing a ```bash fenced block.")
	}
	instructions = append(instructions,
		"Declared tools:",
	)
	return strings.Join(append(instructions, lines...), "\n")
}

func buildTraeToolNameNormalizer(openaiReq, originalReq []byte) func(string) string {
	requestTools := make(map[string]string)
	addRequestTool := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		key := traeToolNameKey(name)
		if key == "" {
			return
		}
		if _, exists := requestTools[key]; !exists {
			requestTools[key] = name
		}
	}
	for _, raw := range [][]byte{openaiReq, originalReq} {
		if len(raw) == 0 || !gjson.ValidBytes(raw) {
			continue
		}
		tools := gjson.GetBytes(raw, "tools")
		if !tools.Exists() || !tools.IsArray() {
			continue
		}
		tools.ForEach(func(_, tool gjson.Result) bool {
			addRequestTool(tool.Get("function.name").String())
			addRequestTool(tool.Get("name").String())
			return true
		})
	}
	if len(requestTools) == 0 {
		return func(name string) string {
			return name
		}
	}
	return func(name string) string {
		key := traeToolNameKey(name)
		if mapped := requestTools[key]; mapped != "" {
			return mapped
		}
		for _, alias := range traeToolNameAliases(key) {
			if mapped := requestTools[traeToolNameKey(alias)]; mapped != "" {
				return mapped
			}
		}
		return name
	}
}

func traeToolNameKey(name string) string {
	var b strings.Builder
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z':
			b.WriteByte(c)
		case c >= 'A' && c <= 'Z':
			b.WriteByte(c + ('a' - 'A'))
		case c >= '0' && c <= '9':
			b.WriteByte(c)
		}
	}
	return b.String()
}

func traeToolNameAliases(key string) []string {
	switch key {
	case "executebash", "runbash", "bashcommand", "runcommand", "executeshell", "runshell", "shell":
		return []string{"Bash"}
	case "readfile", "openfile":
		return []string{"Read"}
	case "writefile", "createfile":
		return []string{"Write"}
	case "editfile":
		return []string{"Edit"}
	case "multieditfile":
		return []string{"MultiEdit"}
	default:
		return nil
	}
}

func normalizeTraeToolArguments(toolName, arguments string) string {
	switch traeToolNameKey(toolName) {
	case "read":
		return normalizeTraeFilePathArgument(arguments, "file_path", "file_name", "path")
	default:
		return arguments
	}
}

func normalizeTraeFilePathArgument(arguments string, canonicalKey string, aliasKeys ...string) string {
	arguments = strings.TrimSpace(arguments)
	if arguments == "" || !gjson.Valid(arguments) {
		return arguments
	}
	root := gjson.Parse(arguments)
	if !root.IsObject() {
		return arguments
	}
	filePath := strings.TrimSpace(root.Get(canonicalKey).String())
	if filePath == "" {
		for _, key := range aliasKeys {
			if candidate := strings.TrimSpace(root.Get(key).String()); candidate != "" {
				filePath = candidate
				break
			}
		}
	}
	if filePath == "" {
		return arguments
	}
	if !filepath.IsAbs(filePath) {
		if absPath, errAbs := filepath.Abs(filePath); errAbs == nil {
			filePath = absPath
		}
	}
	normalized, errSet := sjson.Set(arguments, canonicalKey, filePath)
	if errSet != nil {
		return arguments
	}
	for _, key := range aliasKeys {
		if gjson.Get(normalized, key).Exists() {
			next, errDel := sjson.Delete(normalized, key)
			if errDel != nil {
				return normalized
			}
			normalized = next
		}
	}
	if compacted, ok := compactJSONBytes([]byte(normalized)); ok {
		return compacted
	}
	return normalized
}

func traeToolSignature(name, arguments string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	arguments = strings.TrimSpace(arguments)
	if arguments == "" {
		arguments = "{}"
	}
	if gjson.Valid(arguments) {
		if compacted, ok := compactJSONBytes([]byte(arguments)); ok {
			arguments = compacted
		}
	}
	return name + "\x00" + arguments
}

func compactJSONBytes(raw []byte) (string, bool) {
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err == nil {
		return buf.String(), true
	}
	return "", false
}
