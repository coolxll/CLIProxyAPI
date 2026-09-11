package trae

import (
	"encoding/json"
	"encoding/xml"
	"errors"
	"html"
	"io"
	"regexp"
	"strings"
)

var (
	deepSeekCloseThinkRe = regexp.MustCompile(`</think[^>]*>`)
	deepSeekOpenThinkRe  = regexp.MustCompile(`<think[^>]*>`)
)

type traeThoughtToolCall struct {
	Name      string
	Arguments string
}

type traeThoughtParseResult struct {
	Content   string
	ToolCalls []traeThoughtToolCall
}

type traeThoughtToolParser struct {
	buffer string
}

type traeInlineToolCallParser struct {
	buffer string
}

type deepSeekThinkTagStripper struct {
	buffer string
}

const traeThoughtToolMarker = "<tool_call>"
const traeInlineToolCallMarker = "tool_calls="
const traeRunCommandStartMarker = "<run_command>"
const traeRunCommandEndMarker = "</run_command>"
const traeFuncCallBeginMarker = "<|FunctionCallBegin|>"
const traeFuncCallEndMarker = "<|FunctionCallEnd|>"
const traeSeedToolCallStartMarker = "<seed:tool_call>"
const traeSeedToolCallEndMarker = "</seed:tool_call>"
const traeFunctionStartMarker = "<function="
const traeFunctionEndMarker = "</function>"
const traeInvokeStartMarker = "<invoke"
const traeInvokeEndMarker = "</invoke>"
const traeXMLToolCallStartMarker = "<tool_call>"
const traeXMLToolCallEndMarker = "</tool_call>"
const maxTraeThoughtToolBuffer = 8192
const maxTraeInlineToolBuffer = 65536
const maxDeepSeekThinkTagBuffer = 8192

func (p *traeThoughtToolParser) Append(chunk string) traeThoughtParseResult {
	p.buffer += chunk
	var result traeThoughtParseResult

	for {
		idx := strings.Index(p.buffer, traeThoughtToolMarker)
		if idx < 0 {
			flushLen := len(p.buffer) - trailingToolMarkerPrefixLen(p.buffer)
			if flushLen > 0 {
				result.Content += p.buffer[:flushLen]
				p.buffer = p.buffer[flushLen:]
			}
			return result
		}

		if idx > 0 {
			result.Content += p.buffer[:idx]
			p.buffer = p.buffer[idx:]
		}

		closeIdx := strings.Index(p.buffer, "/>")
		if closeIdx < 0 {
			if len(p.buffer) > maxTraeThoughtToolBuffer {
				flushLen := len(p.buffer) - trailingToolMarkerPrefixLen(p.buffer)
				if flushLen > 0 {
					result.Content += p.buffer[:flushLen]
					p.buffer = p.buffer[flushLen:]
				}
			}
			return result
		}

		markup := p.buffer[:closeIdx+len("/>")]
		toolCall, ok := parseTraeThoughtToolMarkup(markup)
		if !ok {
			result.Content += markup
			p.buffer = p.buffer[closeIdx+len("/>"):]
			continue
		}
		result.ToolCalls = append(result.ToolCalls, toolCall)
		p.buffer = p.buffer[closeIdx+len("/>"):]
	}
}

func (p *traeThoughtToolParser) Flush() string {
	content := p.buffer
	p.buffer = ""
	return content
}

func (p *traeInlineToolCallParser) Append(chunk string) traeThoughtParseResult {
	p.buffer += chunk
	var result traeThoughtParseResult

	for {
		idx, marker := nextTraeInlineToolMarker(p.buffer)
		if idx < 0 {
			flushLen := len(p.buffer) - trailingInlineToolMarkerPrefixLen(p.buffer)
			if flushLen > 0 {
				result.Content += p.buffer[:flushLen]
				p.buffer = p.buffer[flushLen:]
			}
			return result
		}

		if marker == traeRunCommandStartMarker {
			if idx > 0 {
				result.Content += p.buffer[:idx]
				p.buffer = p.buffer[idx:]
			}
			closeIdx := strings.Index(p.buffer, traeRunCommandEndMarker)
			if closeIdx < 0 {
				if len(p.buffer) > maxTraeInlineToolBuffer {
					result.Content += p.buffer
					p.buffer = ""
				}
				return result
			}
			markup := p.buffer[:closeIdx+len(traeRunCommandEndMarker)]
			toolCall, ok := parseTraeRunCommandMarkup(markup)
			if !ok {
				result.Content += markup
			} else {
				result.ToolCalls = append(result.ToolCalls, toolCall)
			}
			p.buffer = p.buffer[closeIdx+len(traeRunCommandEndMarker):]
			continue
		}

		if marker == traeFuncCallBeginMarker {
			prefix := p.buffer[:idx]
			if idx > 0 {
				p.buffer = p.buffer[idx:]
			}
			// Skip past <|FunctionCallBegin|>
			jsonStart := len(traeFuncCallBeginMarker)
			if jsonStart >= len(p.buffer) {
				if len(p.buffer) > maxTraeInlineToolBuffer {
					result.Content += prefix + p.buffer
					p.buffer = ""
				} else {
					p.buffer = prefix + p.buffer
				}
				return result
			}
			closeIdx := strings.Index(p.buffer, traeFuncCallEndMarker)
			if closeIdx < 0 {
				if len(p.buffer) > maxTraeInlineToolBuffer {
					result.Content += prefix + p.buffer
					p.buffer = ""
				} else {
					p.buffer = prefix + p.buffer
				}
				return result
			}
			// Extract JSON between <|FunctionCallBegin|> and <|FunctionCallEnd|>
			jsonRaw := p.buffer[jsonStart:closeIdx]
			toolCalls, _, complete := parseTraeInlineToolCalls(jsonRaw)
			if !complete || len(toolCalls) == 0 {
				// Malformed or empty — flush entire markup as content
				markup := p.buffer[:closeIdx+len(traeFuncCallEndMarker)]
				result.Content += prefix + markup
				p.buffer = p.buffer[closeIdx+len(traeFuncCallEndMarker):]
				continue
			}
			result.Content += stripInlineToolLabel(prefix, toolCalls[0].Name)
			result.ToolCalls = append(result.ToolCalls, toolCalls...)
			p.buffer = p.buffer[closeIdx+len(traeFuncCallEndMarker):]
			continue
		}

		if marker == traeSeedToolCallStartMarker {
			prefix := p.buffer[:idx]
			if idx > 0 {
				p.buffer = p.buffer[idx:]
			}
			// Skip past <seed:tool_call>
			contentStart := len(traeSeedToolCallStartMarker)
			if contentStart >= len(p.buffer) {
				if len(p.buffer) > maxTraeInlineToolBuffer {
					result.Content += prefix + p.buffer
					p.buffer = ""
				} else {
					p.buffer = prefix + p.buffer
				}
				return result
			}
			closeIdx := strings.Index(p.buffer, traeSeedToolCallEndMarker)
			if closeIdx < 0 {
				if len(p.buffer) > maxTraeInlineToolBuffer {
					result.Content += prefix + p.buffer
					p.buffer = ""
				} else {
					p.buffer = prefix + p.buffer
				}
				return result
			}
			// Extract content between <seed:tool_call> and </seed:tool_call>
			inner := p.buffer[contentStart:closeIdx]
			toolCall, ok := parseSeedToolCall(inner)
			if !ok {
				// Malformed — flush entire markup as content
				markup := p.buffer[:closeIdx+len(traeSeedToolCallEndMarker)]
				result.Content += prefix + markup
				p.buffer = p.buffer[closeIdx+len(traeSeedToolCallEndMarker):]
				continue
			}
			result.Content += stripInlineToolLabel(prefix, toolCall.Name)
			result.ToolCalls = append(result.ToolCalls, toolCall)
			p.buffer = p.buffer[closeIdx+len(traeSeedToolCallEndMarker):]
			continue
		}

		if marker == traeFunctionStartMarker {
			prefix := p.buffer[:idx]
			if idx > 0 {
				p.buffer = p.buffer[idx:]
			}
			// Skip past <function=
			contentStart := len(traeFunctionStartMarker)
			if contentStart >= len(p.buffer) {
				if len(p.buffer) > maxTraeInlineToolBuffer {
					result.Content += prefix + p.buffer
					p.buffer = ""
				} else {
					p.buffer = prefix + p.buffer
				}
				return result
			}
			closeIdx := strings.Index(p.buffer, traeFunctionEndMarker)
			if closeIdx < 0 {
				if len(p.buffer) > maxTraeInlineToolBuffer {
					result.Content += prefix + p.buffer
					p.buffer = ""
				} else {
					p.buffer = prefix + p.buffer
				}
				return result
			}
			// Extract content between <function= and </function>
			inner := p.buffer[contentStart:closeIdx]
			toolCall, ok := parseFunctionMarkup(inner)
			if !ok {
				// Malformed — flush entire markup as content
				markup := p.buffer[:closeIdx+len(traeFunctionEndMarker)]
				result.Content += prefix + markup
				p.buffer = p.buffer[closeIdx+len(traeFunctionEndMarker):]
				continue
			}
			result.Content += stripInlineToolLabel(prefix, toolCall.Name)
			result.ToolCalls = append(result.ToolCalls, toolCall)
			p.buffer = p.buffer[closeIdx+len(traeFunctionEndMarker):]
			continue
		}

		if marker == traeInvokeStartMarker {
			prefix := p.buffer[:idx]
			if idx > 0 {
				p.buffer = p.buffer[idx:]
			}
			openEnd := strings.Index(p.buffer, ">")
			if openEnd < 0 {
				if len(p.buffer) > maxTraeInlineToolBuffer {
					result.Content += prefix + p.buffer
					p.buffer = ""
				} else {
					p.buffer = prefix + p.buffer
				}
				return result
			}
			closeIdx := strings.Index(p.buffer, traeInvokeEndMarker)
			if closeIdx < 0 {
				if len(p.buffer) > maxTraeInlineToolBuffer {
					result.Content += prefix + p.buffer
					p.buffer = ""
				} else {
					p.buffer = prefix + p.buffer
				}
				return result
			}
			markup := p.buffer[:closeIdx+len(traeInvokeEndMarker)]
			toolCalls := parseInvokeToolCalls(markup)
			if len(toolCalls) == 0 {
				result.Content += prefix + markup
				p.buffer = p.buffer[closeIdx+len(traeInvokeEndMarker):]
				continue
			}
			result.Content += stripToolMarkupPrefix(prefix, toolCalls[0].Name)
			result.ToolCalls = append(result.ToolCalls, toolCalls...)
			p.buffer = p.buffer[closeIdx+len(traeInvokeEndMarker):]
			continue
		}

		if marker == traeXMLToolCallStartMarker {
			prefix := p.buffer[:idx]
			if idx > 0 {
				p.buffer = p.buffer[idx:]
			}
			closeIdx := strings.Index(p.buffer, traeXMLToolCallEndMarker)
			if closeIdx < 0 {
				if len(p.buffer) > maxTraeInlineToolBuffer {
					result.Content += prefix + p.buffer
					p.buffer = ""
				} else {
					p.buffer = prefix + p.buffer
				}
				return result
			}
			markup := p.buffer[:closeIdx+len(traeXMLToolCallEndMarker)]
			toolCall, ok := parseXMLToolCall(markup)
			if !ok {
				result.Content += prefix + markup
				p.buffer = p.buffer[closeIdx+len(traeXMLToolCallEndMarker):]
				continue
			}
			result.Content += xmlToolPrefixContent(prefix, toolCall.Name)
			result.ToolCalls = append(result.ToolCalls, toolCall)
			p.buffer = p.buffer[closeIdx+len(traeXMLToolCallEndMarker):]
			p.buffer = trimLeadingXMLWrapperClose(p.buffer)
			continue
		}

		arrayStart := idx + len(traeInlineToolCallMarker)
		for arrayStart < len(p.buffer) && (p.buffer[arrayStart] == ' ' || p.buffer[arrayStart] == '\t' || p.buffer[arrayStart] == '\n' || p.buffer[arrayStart] == '\r') {
			arrayStart++
		}
		if arrayStart >= len(p.buffer) {
			if len(p.buffer) > maxTraeInlineToolBuffer {
				result.Content += p.buffer
				p.buffer = ""
			}
			return result
		}
		if p.buffer[arrayStart] != '[' {
			result.Content += p.buffer[:idx+len(traeInlineToolCallMarker)]
			p.buffer = p.buffer[idx+len(traeInlineToolCallMarker):]
			continue
		}

		toolCalls, consumed, complete := parseTraeInlineToolCalls(p.buffer[arrayStart:])
		if !complete {
			if len(p.buffer) > maxTraeInlineToolBuffer {
				result.Content += p.buffer
				p.buffer = ""
			}
			return result
		}
		if len(toolCalls) == 0 {
			result.Content += p.buffer[:arrayStart+consumed]
			p.buffer = p.buffer[arrayStart+consumed:]
			continue
		}

		prefix := p.buffer[:idx]
		result.Content += stripInlineToolLabel(prefix, toolCalls[0].Name)
		result.ToolCalls = append(result.ToolCalls, toolCalls...)
		p.buffer = p.buffer[arrayStart+consumed:]
		for strings.HasPrefix(p.buffer, "]") {
			p.buffer = p.buffer[1:]
		}
	}
}

func (p *traeInlineToolCallParser) Flush() string {
	content := p.buffer
	p.buffer = ""
	return content
}

func nextTraeInlineToolMarker(s string) (int, string) {
	minIdx := -1
	minMarker := ""
	if idx := strings.Index(s, traeInlineToolCallMarker); idx >= 0 && (minIdx < 0 || idx < minIdx) {
		minIdx = idx
		minMarker = traeInlineToolCallMarker
	}
	if idx := strings.Index(s, traeRunCommandStartMarker); idx >= 0 && (minIdx < 0 || idx < minIdx) {
		minIdx = idx
		minMarker = traeRunCommandStartMarker
	}
	if idx := strings.Index(s, traeFuncCallBeginMarker); idx >= 0 && (minIdx < 0 || idx < minIdx) {
		minIdx = idx
		minMarker = traeFuncCallBeginMarker
	}
	if idx := strings.Index(s, traeSeedToolCallStartMarker); idx >= 0 && (minIdx < 0 || idx < minIdx) {
		minIdx = idx
		minMarker = traeSeedToolCallStartMarker
	}
	if idx := strings.Index(s, traeFunctionStartMarker); idx >= 0 && (minIdx < 0 || idx < minIdx) {
		minIdx = idx
		minMarker = traeFunctionStartMarker
	}
	if idx := strings.Index(s, traeInvokeStartMarker); idx >= 0 && (minIdx < 0 || idx < minIdx) {
		minIdx = idx
		minMarker = traeInvokeStartMarker
	}
	if idx := strings.Index(s, traeXMLToolCallStartMarker); idx >= 0 && (minIdx < 0 || idx < minIdx) {
		minIdx = idx
		minMarker = traeXMLToolCallStartMarker
	}
	if minIdx < 0 {
		return -1, ""
	}
	return minIdx, minMarker
}

func trailingToolMarkerPrefixLen(s string) int {
	return trailingMarkerPrefixLen(s, traeThoughtToolMarker)
}

func trailingInlineToolMarkerPrefixLen(s string) int {
	return trailingMarkerPrefixLen(s, traeInlineToolCallMarker, traeRunCommandStartMarker, traeFuncCallBeginMarker, traeSeedToolCallStartMarker, traeFunctionStartMarker, traeInvokeStartMarker, traeXMLToolCallStartMarker)
}

func trailingMarkerPrefixLen(s string, markers ...string) int {
	maxLen := 0
	for _, marker := range markers {
		if marker == "" {
			continue
		}
		markerMax := len(marker) - 1
		if len(s) < markerMax {
			markerMax = len(s)
		}
		for n := markerMax; n > 0; n-- {
			if strings.HasSuffix(s, marker[:n]) && n > maxLen {
				maxLen = n
			}
		}
	}
	return maxLen
}

// stripDeepSeekThinkTags removes DeepSeek-R1's <think> and </think> tags
// (including malformed closing tags like </think_never_used_...>) from reasoning content.
func stripDeepSeekThinkTags(s string) string {
	// Remove malformed closing tags: </think_never_used_...> or </think...> with suffix
	s = deepSeekCloseThinkRe.ReplaceAllString(s, "")
	// Remove opening <think> tags (with or without attributes)
	s = deepSeekOpenThinkRe.ReplaceAllString(s, "")
	return s
}

func (p *deepSeekThinkTagStripper) Append(chunk string) string {
	p.buffer += chunk
	return p.consume(false)
}

func (p *deepSeekThinkTagStripper) Flush() string {
	return p.consume(true)
}

func (p *deepSeekThinkTagStripper) consume(final bool) string {
	var out strings.Builder
	for p.buffer != "" {
		idx := strings.IndexByte(p.buffer, '<')
		if idx < 0 {
			keep := 0
			if !final {
				keep = trailingMarkerPrefixLen(p.buffer, "<think", "</think")
			}
			if keep > 0 {
				out.WriteString(p.buffer[:len(p.buffer)-keep])
				p.buffer = p.buffer[len(p.buffer)-keep:]
			} else {
				out.WriteString(p.buffer)
				p.buffer = ""
			}
			break
		}

		if idx > 0 {
			out.WriteString(p.buffer[:idx])
			p.buffer = p.buffer[idx:]
		}

		if strings.HasPrefix(p.buffer, "<think") || strings.HasPrefix(p.buffer, "</think") {
			closeIdx := strings.IndexByte(p.buffer, '>')
			if closeIdx < 0 {
				if final {
					p.buffer = "" // Discard unclosed think tag at the end of stream
				} else if len(p.buffer) > maxDeepSeekThinkTagBuffer {
					out.WriteString(p.buffer)
					p.buffer = ""
				}
				break
			}
			p.buffer = p.buffer[closeIdx+1:]
			continue
		}

		if !final && isPotentialDeepSeekThinkTagPrefix(p.buffer) {
			break
		}

		out.WriteByte('<')
		p.buffer = p.buffer[1:]
	}
	return out.String()
}

func isPotentialDeepSeekThinkTagPrefix(s string) bool {
	if strings.Contains(s, ">") {
		return false
	}
	return strings.HasPrefix("<think", s) || strings.HasPrefix("</think", s)
}

func parseTraeInlineToolCalls(raw string) ([]traeThoughtToolCall, int, bool) {
	decoder := json.NewDecoder(strings.NewReader(raw))
	var items []json.RawMessage
	if err := decoder.Decode(&items); err != nil {
		if errors.Is(err, io.EOF) || strings.Contains(err.Error(), "unexpected EOF") {
			return nil, 0, false
		}
		return nil, 0, true
	}

	toolCalls := make([]traeThoughtToolCall, 0, len(items))
	for _, item := range items {
		toolCall, ok := parseTraeInlineToolCall(item)
		if ok {
			toolCalls = append(toolCalls, toolCall)
		}
	}
	return toolCalls, int(decoder.InputOffset()), true
}

func parseTraeInlineToolCall(raw json.RawMessage) (traeThoughtToolCall, bool) {
	var obj struct {
		Name       string          `json:"name"`
		Arguments  json.RawMessage `json:"arguments"`
		Parameters json.RawMessage `json:"parameters"`
		Function   struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		} `json:"function"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return traeThoughtToolCall{}, false
	}

	name := firstNonEmpty(obj.Name, obj.Function.Name)
	if name == "" {
		return traeThoughtToolCall{}, false
	}
	args := firstNonEmptyRaw(obj.Arguments, obj.Function.Arguments, obj.Parameters)
	arguments := "{}"
	if len(args) > 0 && string(args) != "null" {
		arguments = compactTraeToolArguments(args)
	}
	return traeThoughtToolCall{Name: name, Arguments: arguments}, true
}

func firstNonEmptyRaw(values ...json.RawMessage) json.RawMessage {
	for _, value := range values {
		if len(value) > 0 && string(value) != "null" {
			return value
		}
	}
	return nil
}

// parseSeedToolCall parses <seed:tool_call> inner content containing a function block.
func parseSeedToolCall(inner string) (traeThoughtToolCall, bool) {
	inner = strings.TrimSpace(inner)
	if inner == "" {
		return traeThoughtToolCall{}, false
	}
	start := strings.Index(inner, traeFunctionStartMarker)
	if start < 0 {
		return traeThoughtToolCall{}, false
	}
	inner = inner[start+len(traeFunctionStartMarker):]
	end := strings.Index(inner, traeFunctionEndMarker)
	if end < 0 {
		return traeThoughtToolCall{}, false
	}
	return parseFunctionMarkup(inner[:end])
}

// parseFunctionMarkup parses Name><parameter=key>value</parameter> style calls.
func parseFunctionMarkup(inner string) (traeThoughtToolCall, bool) {
	inner = strings.TrimSpace(inner)
	nameEnd := strings.Index(inner, ">")
	if nameEnd <= 0 {
		return traeThoughtToolCall{}, false
	}
	name := strings.TrimSpace(inner[:nameEnd])
	if name == "" {
		return traeThoughtToolCall{}, false
	}

	params := make(map[string]string)
	rest := inner[nameEnd+1:]
	for {
		paramStart := strings.Index(rest, "<parameter=")
		if paramStart < 0 {
			break
		}
		rest = rest[paramStart+len("<parameter="):]
		keyEnd := strings.Index(rest, ">")
		if keyEnd <= 0 {
			return traeThoughtToolCall{}, false
		}
		key := strings.TrimSpace(rest[:keyEnd])
		rest = rest[keyEnd+1:]
		valueEnd := strings.Index(rest, "</parameter>")
		if valueEnd < 0 {
			return traeThoughtToolCall{}, false
		}
		if key != "" {
			params[key] = html.UnescapeString(rest[:valueEnd])
		}
		rest = rest[valueEnd+len("</parameter>"):]
	}
	if len(params) == 0 {
		return traeThoughtToolCall{}, false
	}

	argsBytes, err := json.Marshal(params)
	if err != nil {
		return traeThoughtToolCall{}, false
	}
	return traeThoughtToolCall{Name: name, Arguments: string(argsBytes)}, true
}

func parseInvokeToolCalls(markup string) []traeThoughtToolCall {
	type invokeParameter struct {
		Name  string `xml:"name,attr"`
		Value string `xml:",chardata"`
	}
	type invokeFunction struct {
		Name       string            `xml:"name,attr"`
		Parameters []invokeParameter `xml:"parameter"`
	}
	type invokeToolCall struct {
		Function invokeFunction `xml:"function"`
	}
	type invokePayload struct {
		ToolCalls []invokeToolCall `xml:"tool_call"`
	}

	var payload invokePayload
	if err := xml.Unmarshal([]byte(markup), &payload); err != nil {
		return nil
	}

	toolCalls := make([]traeThoughtToolCall, 0, len(payload.ToolCalls))
	for _, item := range payload.ToolCalls {
		name := strings.TrimSpace(item.Function.Name)
		if name == "" {
			continue
		}
		args := make(map[string]string)
		for _, param := range item.Function.Parameters {
			paramName := strings.TrimSpace(param.Name)
			if paramName != "" {
				args[paramName] = strings.TrimSpace(param.Value)
			}
		}
		if len(args) == 0 {
			continue
		}
		argsBytes, err := json.Marshal(args)
		if err != nil {
			continue
		}
		toolCalls = append(toolCalls, traeThoughtToolCall{
			Name:      name,
			Arguments: string(argsBytes),
		})
	}
	return toolCalls
}

func parseXMLToolCall(markup string) (traeThoughtToolCall, bool) {
	type toolParameter struct {
		Name  string `xml:"name,attr"`
		Value string `xml:",chardata"`
	}
	type toolFunction struct {
		Name       string          `xml:"name,attr"`
		Parameters []toolParameter `xml:"parameter"`
	}
	type toolCall struct {
		Function toolFunction `xml:"function"`
	}

	var call toolCall
	if err := xml.Unmarshal([]byte(markup), &call); err != nil {
		return traeThoughtToolCall{}, false
	}
	name := strings.TrimSpace(call.Function.Name)
	if name == "" {
		return traeThoughtToolCall{}, false
	}
	args := make(map[string]string)
	for _, param := range call.Function.Parameters {
		paramName := strings.TrimSpace(param.Name)
		if paramName != "" {
			args[paramName] = strings.TrimSpace(param.Value)
		}
	}
	if len(args) == 0 {
		return traeThoughtToolCall{}, false
	}
	argsBytes, err := json.Marshal(args)
	if err != nil {
		return traeThoughtToolCall{}, false
	}
	return traeThoughtToolCall{Name: name, Arguments: string(argsBytes)}, true
}

func compactTraeToolArguments(raw json.RawMessage) string {
	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		if strings.TrimSpace(asString) == "" {
			return "{}"
		}
		return asString
	}
	if compacted, ok := compactJSONBytes(raw); ok {
		return compacted
	}
	return string(raw)
}

func parseTraeRunCommandMarkup(markup string) (traeThoughtToolCall, bool) {
	type runCommand struct {
		Command string `xml:"command"`
	}
	var rc runCommand
	if err := xml.Unmarshal([]byte(markup), &rc); err != nil {
		return traeThoughtToolCall{}, false
	}
	command := strings.TrimSpace(rc.Command)
	if command == "" {
		return traeThoughtToolCall{}, false
	}
	argsBytes, err := json.Marshal(map[string]string{"command": command})
	if err != nil {
		return traeThoughtToolCall{}, false
	}
	return traeThoughtToolCall{
		Name:      "Bash",
		Arguments: string(argsBytes),
	}, true
}

func stripInlineToolLabel(prefix, toolName string) string {
	if toolName == "" {
		return prefix
	}
	trimmed := strings.TrimRight(prefix, " \t\r\n")
	if !strings.HasSuffix(trimmed, toolName) {
		return prefix
	}
	labelStart := len(trimmed) - len(toolName)
	if labelStart > 0 {
		prev := trimmed[labelStart-1]
		if (prev >= 'A' && prev <= 'Z') || (prev >= 'a' && prev <= 'z') || (prev >= '0' && prev <= '9') || prev == '_' || prev == '-' {
			return prefix
		}
	}
	return trimmed[:labelStart]
}

func stripToolMarkupPrefix(prefix, toolName string) string {
	prefix = stripInlineToolLabel(prefix, toolName)
	xmlStart := strings.LastIndex(prefix, "<?xml")
	if xmlStart < 0 {
		return prefix
	}
	xmlEnd := strings.Index(prefix[xmlStart:], "?>")
	if xmlEnd < 0 {
		return prefix
	}
	after := xmlStart + xmlEnd + len("?>")
	if strings.TrimSpace(prefix[after:]) != "" {
		return prefix
	}
	return prefix[:xmlStart]
}

func xmlToolPrefixContent(prefix, toolName string) string {
	prefix = stripToolMarkupPrefix(prefix, toolName)
	answer := extractXMLTagText(prefix, "answer")
	if answer != "" {
		return answer
	}
	if strings.Contains(prefix, "<?xml") || strings.Contains(prefix, "<root") || strings.Contains(prefix, "<invoke") {
		return ""
	}
	return prefix
}

func extractXMLTagText(s, tag string) string {
	startMarker := "<" + tag + ">"
	endMarker := "</" + tag + ">"
	start := strings.Index(s, startMarker)
	if start < 0 {
		return ""
	}
	start += len(startMarker)
	end := strings.Index(s[start:], endMarker)
	if end < 0 {
		return ""
	}
	return strings.TrimSpace(html.UnescapeString(s[start : start+end]))
}

func trimLeadingXMLWrapperClose(s string) string {
	trimmedLeft := strings.TrimLeft(s, " \t\r\n")
	for _, tag := range []string{"</root>", "</invoke>"} {
		if strings.HasPrefix(trimmedLeft, tag) {
			return trimmedLeft[len(tag):]
		}
	}
	return s
}

func stripTrailingToolCallResidue(s string, hasToolCall bool) string {
	if !hasToolCall {
		return s
	}
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return s
	}
	for _, r := range trimmed {
		if r != '}' && r != ']' {
			return s
		}
	}
	return ""
}

func parseTraeThoughtToolMarkup(markup string) (traeThoughtToolCall, bool) {
	inner := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(markup, traeThoughtToolMarker), "/>"))
	if inner == "" || strings.HasPrefix(inner, "<") {
		return traeThoughtToolCall{}, false
	}

	xmlSnippet := "<" + inner + "/>"
	decoder := xml.NewDecoder(strings.NewReader(xmlSnippet))
	for {
		token, err := decoder.Token()
		if err != nil {
			return traeThoughtToolCall{}, false
		}
		start, ok := token.(xml.StartElement)
		if !ok {
			continue
		}
		args := make(map[string]string, len(start.Attr))
		for _, attr := range start.Attr {
			args[attr.Name.Local] = attr.Value
		}
		argBytes, err := json.Marshal(args)
		if err != nil {
			return traeThoughtToolCall{}, false
		}
		return traeThoughtToolCall{
			Name:      start.Name.Local,
			Arguments: string(argBytes),
		}, true
	}
}
