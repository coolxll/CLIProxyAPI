package executor

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestTraeThoughtToolParser(t *testing.T) {
	tests := []struct {
		name      string
		chunks    []string
		wantText  string
		wantTools []traeThoughtToolCall
	}{
		{
			name:     "single chunk",
			chunks:   []string{`<tool_call>LS path="/tmp" />`},
			wantText: "",
			wantTools: []traeThoughtToolCall{{
				Name:      "LS",
				Arguments: `{"path":"/tmp"}`,
			}},
		},
		{
			name:     "split chunk",
			chunks:   []string{`<tool_call>LS path`, `="/tmp" />`},
			wantText: "",
			wantTools: []traeThoughtToolCall{{
				Name:      "LS",
				Arguments: `{"path":"/tmp"}`,
			}},
		},
		{
			name:     "multiple parameters",
			chunks:   []string{`<tool_call>Read path="/tmp/a.go" offset="1" limit="20" />`},
			wantText: "",
			wantTools: []traeThoughtToolCall{{
				Name:      "Read",
				Arguments: `{"limit":"20","offset":"1","path":"/tmp/a.go"}`,
			}},
		},
		{
			name:     "mixed text",
			chunks:   []string{`before <tool_call>SearchCodebase query="main" /> after`},
			wantText: "before  after",
			wantTools: []traeThoughtToolCall{{
				Name:      "SearchCodebase",
				Arguments: `{"query":"main"}`,
			}},
		},
		{
			name:      "no tool call",
			chunks:    []string{"plain thought"},
			wantText:  "plain thought",
			wantTools: nil,
		},
		{
			name:      "incomplete tool call",
			chunks:    []string{`prefix <tool_call>LS path`},
			wantText:  "prefix ",
			wantTools: nil,
		},
		{
			name:      "malformed tool call",
			chunks:    []string{`<tool_call>LS path=/tmp />`},
			wantText:  `<tool_call>LS path=/tmp />`,
			wantTools: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var parser traeThoughtToolParser
			var gotText strings.Builder
			var gotTools []traeThoughtToolCall
			for _, chunk := range tt.chunks {
				got := parser.Append(chunk)
				gotText.WriteString(got.Content)
				gotTools = append(gotTools, got.ToolCalls...)
			}

			if gotText.String() != tt.wantText {
				t.Fatalf("content = %q, want %q", gotText.String(), tt.wantText)
			}
			if len(gotTools) != len(tt.wantTools) {
				t.Fatalf("tool count = %d, want %d: %#v", len(gotTools), len(tt.wantTools), gotTools)
			}
			for i := range tt.wantTools {
				if gotTools[i] != tt.wantTools[i] {
					t.Fatalf("tool[%d] = %#v, want %#v", i, gotTools[i], tt.wantTools[i])
				}
			}
		})
	}
}

func TestTraeThoughtToolParserFlushesIncompleteAtEOF(t *testing.T) {
	var parser traeThoughtToolParser
	got := parser.Append(`prefix <tool_call>LS path`)
	if got.Content != "prefix " {
		t.Fatalf("content = %q, want prefix", got.Content)
	}
	if len(got.ToolCalls) != 0 {
		t.Fatalf("tool calls = %#v, want none", got.ToolCalls)
	}
	if trailing := parser.Flush(); trailing != `<tool_call>LS path` {
		t.Fatalf("flush = %q, want incomplete markup", trailing)
	}
	if trailing := parser.Flush(); trailing != "" {
		t.Fatalf("second flush = %q, want empty", trailing)
	}
}

func TestTraeThoughtToolParserBoundsIncompleteToolBuffer(t *testing.T) {
	var parser traeThoughtToolParser
	got := parser.Append(traeThoughtToolMarker + strings.Repeat("x", maxTraeThoughtToolBuffer+1))
	if got.Content == "" {
		t.Fatal("expected oversized incomplete tool markup to be flushed as content")
	}
	if len(got.ToolCalls) != 0 {
		t.Fatalf("tool calls = %#v, want none", got.ToolCalls)
	}
	if len(parser.buffer) >= maxTraeThoughtToolBuffer {
		t.Fatalf("buffer len = %d, want bounded below %d", len(parser.buffer), maxTraeThoughtToolBuffer)
	}
}

func TestTraeInlineToolCallParser(t *testing.T) {
	tests := []struct {
		name      string
		chunks    []string
		wantText  string
		wantTools []traeThoughtToolCall
	}{
		{
			name:     "single chunk",
			chunks:   []string{`I will inspect the directory. Bash tool_calls=[{"name":"Bash","arguments":{"command":"ls -la"}}]`},
			wantText: "I will inspect the directory. ",
			wantTools: []traeThoughtToolCall{{
				Name:      "Bash",
				Arguments: `{"command":"ls -la"}`,
			}},
		},
		{
			name:     "split chunk",
			chunks:   []string{`Bash tool_calls=[{"name":"Bash","arguments":{"command"`, `:"ls -la"}}]`},
			wantText: "",
			wantTools: []traeThoughtToolCall{{
				Name:      "Bash",
				Arguments: `{"command":"ls -la"}`,
			}},
		},
		{
			name:     "openai shape",
			chunks:   []string{`prefix Read tool_calls=[{"function":{"name":"Read","arguments":"{\"path\":\"/tmp/a.go\"}"}}] suffix`},
			wantText: "prefix  suffix",
			wantTools: []traeThoughtToolCall{{
				Name:      "Read",
				Arguments: `{"path":"/tmp/a.go"}`,
			}},
		},
		{
			name:     "run command markup",
			chunks:   []string{`I will run it. <run_command><command>ls -la</command></run_command>`},
			wantText: "I will run it. ",
			wantTools: []traeThoughtToolCall{{
				Name:      "Bash",
				Arguments: `{"command":"ls -la"}`,
			}},
		},
		{
			name:     "split run command markup",
			chunks:   []string{`<run_command><command>ls`, ` -la</command></run_command>`},
			wantText: "",
			wantTools: []traeThoughtToolCall{{
				Name:      "Bash",
				Arguments: `{"command":"ls -la"}`,
			}},
		},
		{
			name:      "no tool call",
			chunks:    []string{"plain content"},
			wantText:  "plain content",
			wantTools: nil,
		},
		{
			name:      "malformed tool call",
			chunks:    []string{`Bash tool_calls=[not-json]`},
			wantText:  `Bash tool_calls=[not-json]`,
			wantTools: nil,
		},
		{
			name:     "function call begin single chunk",
			chunks:   []string{`I will search. <|FunctionCallBegin|>[{"name":"Glob","parameters":{"path":"/tmp/*"}}]<|FunctionCallEnd|>`},
			wantText: "I will search. ",
			wantTools: []traeThoughtToolCall{{
				Name:      "Glob",
				Arguments: `{"path":"/tmp/*"}`,
			}},
		},
		{
			name:     "function call begin single chunk escaped",
			chunks:   []string{"I will search. <|FunctionCallBegin|>[{\"name\":\"Glob\",\"parameters\":{\"path\":\"/tmp/*\"}}]<|FunctionCallEnd|>"},
			wantText: "I will search. ",
			wantTools: []traeThoughtToolCall{{
				Name:      "Glob",
				Arguments: `{"path":"/tmp/*"}`,
			}},
		},
		{
			name: "function call begin split marker and JSON across chunks",
			chunks: []string{
				`I will search. <|FunctionCallBegin|>[{"name":"Glob","para`,
				`meters":{"path":"/tmp/*"}}]<|FunctionCallEnd|>`,
			},
			wantText: "I will search. ",
			wantTools: []traeThoughtToolCall{{
				Name:      "Glob",
				Arguments: `{"path":"/tmp/*"}`,
			}},
		},
		{
			name:     "function call begin multiple tools",
			chunks:   []string{`<|FunctionCallBegin|>[{"name":"Glob","parameters":{"path":"/tmp/*"}},{"name":"Read","parameters":{"file_path":"/tmp/a.go"}}]<|FunctionCallEnd|>`},
			wantText: "",
			wantTools: []traeThoughtToolCall{
				{Name: "Glob", Arguments: `{"path":"/tmp/*"}`},
				{Name: "Read", Arguments: `{"file_path":"/tmp/a.go"}`},
			},
		},
		{
			name:     "function call begin with label prefix",
			chunks:   []string{`Glob <|FunctionCallBegin|>[{"name":"Glob","parameters":{"path":"/tmp/*"}}]<|FunctionCallEnd|>`},
			wantText: "",
			wantTools: []traeThoughtToolCall{{
				Name:      "Glob",
				Arguments: `{"path":"/tmp/*"}`,
			}},
		},
		{
			name:      "function call begin malformed JSON",
			chunks:    []string{`<|FunctionCallBegin|>[not-json]<|FunctionCallEnd|>`},
			wantText:  `<|FunctionCallBegin|>[not-json]<|FunctionCallEnd|>`,
			wantTools: nil,
		},
		{
			name:      "function call begin unclosed",
			chunks:    []string{`<|FunctionCallBegin|>[{"name":"Glob"`},
			wantText:  `<|FunctionCallBegin|>[{"name":"Glob"`,
			wantTools: nil,
		},
		{
			name:     "mixed function call begin and tool_calls",
			chunks:   []string{`prefix <|FunctionCallBegin|>[{"name":"Glob","parameters":{"path":"/tmp/*"}}]<|FunctionCallEnd|> suffix Bash tool_calls=[{"name":"Bash","arguments":{"command":"ls"}}]`},
			wantText: "prefix  suffix ",
			wantTools: []traeThoughtToolCall{
				{Name: "Glob", Arguments: `{"path":"/tmp/*"}`},
				{Name: "Bash", Arguments: `{"command":"ls"}`},
			},
		},
		{
			name: "function call begin marker split across chunks",
			chunks: []string{
				`I will search. <|FunctionCa`,
				`llBegin|>[{"name":"Glob","parameters":{"path":"/tmp/*"}}]<|FunctionCallEnd|>`,
			},
			wantText: "I will search. ",
			wantTools: []traeThoughtToolCall{{
				Name:      "Glob",
				Arguments: `{"path":"/tmp/*"}`,
			}},
		},
		{
			name:      "function call begin empty array",
			chunks:    []string{`<|FunctionCallBegin|>[]<|FunctionCallEnd|>`},
			wantText:  `<|FunctionCallBegin|>[]<|FunctionCallEnd|>`,
			wantTools: nil,
		},
		{
			name: "function call begin consecutive blocks",
			chunks: []string{
				`<|FunctionCallBegin|>[{"name":"Glob","parameters":{"path":"/tmp/*"}}]<|FunctionCallEnd|> text <|FunctionCallBegin|>[{"name":"Read","parameters":{"file_path":"/tmp/a.go"}}]<|FunctionCallEnd|>`,
			},
			wantText: " text ",
			wantTools: []traeThoughtToolCall{
				{Name: "Glob", Arguments: `{"path":"/tmp/*"}`},
				{Name: "Read", Arguments: `{"file_path":"/tmp/a.go"}`},
			},
		},
		{
			name:     "seed tool call markup",
			chunks:   []string{`<seed:tool_call><function=Bash><parameter=command>ls -la</parameter></function></seed:tool_call>`},
			wantText: "",
			wantTools: []traeThoughtToolCall{{
				Name:      "Bash",
				Arguments: `{"command":"ls -la"}`,
			}},
		},
		{
			name:     "function markup",
			chunks:   []string{`<function=Read><parameter=file_name>/tmp/test.txt</parameter></function>`},
			wantText: "",
			wantTools: []traeThoughtToolCall{{
				Name:      "Read",
				Arguments: `{"file_name":"/tmp/test.txt"}`,
			}},
		},
		{
			name: "seed tool call split across chunks",
			chunks: []string{
				`prefix <seed:tool_call><function=Bash><parameter=command>`,
				`pwd</parameter></function></seed:tool_call> suffix`,
			},
			wantText: "prefix  suffix",
			wantTools: []traeThoughtToolCall{{
				Name:      "Bash",
				Arguments: `{"command":"pwd"}`,
			}},
		},
		{
			name: "invoke tool call xml",
			chunks: []string{
				"<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<invoke>\n<tool_call>\n<function name=\"Bash\">\n<parameter name=\"command\" string=\"true\">ls -la</parameter>\n</function>\n</tool_call>\n</invoke>",
			},
			wantText: "",
			wantTools: []traeThoughtToolCall{{
				Name:      "Bash",
				Arguments: `{"command":"ls -la"}`,
			}},
		},
		{
			name: "root answer and tool call xml",
			chunks: []string{
				"<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<root>\n<answer>\nI will call Bash.\n</answer>\n<tool_call>\n<function name=\"Bash\">\n<parameter name=\"command\" string=\"true\">ls -la</parameter>\n</function>\n</tool_call>\n</root>",
			},
			wantText: "I will call Bash.",
			wantTools: []traeThoughtToolCall{{
				Name:      "Bash",
				Arguments: `{"command":"ls -la"}`,
			}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var parser traeInlineToolCallParser
			var gotText strings.Builder
			var gotTools []traeThoughtToolCall
			for _, chunk := range tt.chunks {
				got := parser.Append(chunk)
				gotText.WriteString(got.Content)
				gotTools = append(gotTools, got.ToolCalls...)
			}
			gotText.WriteString(parser.Flush())

			if gotText.String() != tt.wantText {
				t.Fatalf("content = %q, want %q", gotText.String(), tt.wantText)
			}
			if len(gotTools) != len(tt.wantTools) {
				t.Fatalf("tool count = %d, want %d: %#v", len(gotTools), len(tt.wantTools), gotTools)
			}
			for i := range tt.wantTools {
				if gotTools[i] != tt.wantTools[i] {
					t.Fatalf("tool[%d] = %#v, want %#v", i, gotTools[i], tt.wantTools[i])
				}
			}
		})
	}
}

func TestNextTraeInlineToolMarkerNotFound(t *testing.T) {
	idx, marker := nextTraeInlineToolMarker("plain content")
	if idx != -1 || marker != "" {
		t.Fatalf("marker = (%d, %q), want (-1, \"\")", idx, marker)
	}
}

func TestStripDeepSeekThinkTags(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "opening think tag",
			input: "<think>Some reasoning here",
			want:  "Some reasoning here",
		},
		{
			name:  "closing think tag",
			input: "Some reasoning</think>",
			want:  "Some reasoning",
		},
		{
			name:  "malformed closing tag with UUID",
			input: "Some reasoning</think_never_used_51bce0c785ca2f68081bfa7d91973934>",
			want:  "Some reasoning",
		},
		{
			name:  "both tags",
			input: "<think>Reasoning content</think>",
			want:  "Reasoning content",
		},
		{
			name:  "think tag with attributes",
			input: "<think type=\"deep\">Reasoning</think>",
			want:  "Reasoning",
		},
		{
			name:  "no tags",
			input: "Plain reasoning without tags",
			want:  "Plain reasoning without tags",
		},
		{
			name:  "empty string",
			input: "",
			want:  "",
		},
		{
			name:  "multiple malformed closing tags",
			input: "Part1</think_never_used_abc>Part2</think_never_used_def>Part3",
			want:  "Part1Part2Part3",
		},
		{
			name:  "only closing tag without opening",
			input: "Part1</think>Part2",
			want:  "Part1Part2",
		},
		{
			name:  "non-matching tags preserved",
			input: "<thin>not a think tag",
			want:  "<thin>not a think tag",
		},
		{
			name:  "multiple think blocks",
			input: "<think>first</think>middle<think>second",
			want:  "firstmiddlesecond",
		},
		{
			name:  "empty closing tag",
			input: "Hello</think>World",
			want:  "HelloWorld",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripDeepSeekThinkTags(tt.input)
			if got != tt.want {
				t.Fatalf("stripDeepSeekThinkTags(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestDeepSeekThinkTagStripperAcrossChunks(t *testing.T) {
	tests := []struct {
		name   string
		chunks []string
		want   string
	}{
		{
			name:   "split opening tag",
			chunks: []string{"<thi", "nk>reason"},
			want:   "reason",
		},
		{
			name:   "split malformed closing tag",
			chunks: []string{"reason</think_never", "_used_abc>done"},
			want:   "reasondone",
		},
		{
			name:   "split normal closing tag",
			chunks: []string{"reason</thi", "nk>done"},
			want:   "reasondone",
		},
		{
			name:   "non think tag preserved",
			chunks: []string{"prefix <thi", "n>suffix"},
			want:   "prefix <thin>suffix",
		},
		{
			name:   "opening tag with attributes split",
			chunks: []string{"<think type=\"deep", "\">reason</think>"},
			want:   "reason",
		},
		{
			name:   "malformed closing tag missing bracket at EOF",
			chunks: []string{"reason</think_never_used_123"},
			want:   "reason",
		},
		{
			name:   "normal closing tag missing bracket at EOF",
			chunks: []string{"reason</think"},
			want:   "reason",
		},
		{
			name:   "unclosed opening tag at EOF",
			chunks: []string{"reason<think"},
			want:   "reason",
		},
		{
			name:   "unclosed opening tag with content at EOF",
			chunks: []string{"reason<think some reasoning"},
			want:   "reason",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stripper deepSeekThinkTagStripper
			var got strings.Builder
			for _, chunk := range tt.chunks {
				got.WriteString(stripper.Append(chunk))
			}
			got.WriteString(stripper.Flush())
			if got.String() != tt.want {
				t.Fatalf("stream stripped output = %q, want %q", got.String(), tt.want)
			}
		})
	}
}

func TestBuildTraeToolShimInstructions(t *testing.T) {
	instructions := buildTraeToolShimInstructions([]byte(`{
		"tools": [
			{"type":"function","function":{"name":"Bash","description":"Run shell","parameters":{"type":"object"}}},
			{"type":"function","function":{"name":"mcp__weather__get_current_weather","description":"Get weather","parameters":{"type":"object","properties":{"location":{"type":"string"}}}}}
		]
	}`))

	if !strings.Contains(instructions, "mcp__weather__get_current_weather") {
		t.Fatalf("tool instructions missing weather tool: %q", instructions)
	}
	if !strings.Contains(instructions, "Bash") {
		t.Fatalf("tool instructions missing Bash tool: %q", instructions)
	}
	if !strings.Contains(instructions, "tool_calls=") {
		t.Fatalf("tool instructions missing inline tool call protocol: %q", instructions)
	}
	if !strings.Contains(instructions, "Do not simulate tool execution in prose") {
		t.Fatalf("tool instructions should forbid simulated tool output: %q", instructions)
	}
	if !strings.Contains(instructions, "For shell commands, call Bash") {
		t.Fatalf("tool instructions missing Bash-specific guidance: %q", instructions)
	}
	if strings.Contains(instructions, "summarize the results in natural language") {
		t.Fatalf("tool instructions should not inject post-commit content guidance: %q", instructions)
	}
}

func TestBuildTraeToolShimInstructionsIncludesReadGuidance(t *testing.T) {
	instructions := buildTraeToolShimInstructions([]byte(`{
		"tools": [
			{"type":"function","function":{"name":"Read","description":"Read file","parameters":{"type":"object","properties":{"file_path":{"type":"string"}}}}}
		]
	}`))

	if !strings.Contains(instructions, "For file reads, call Read with an absolute file_path argument") {
		t.Fatalf("tool instructions missing Read file_path guidance: %q", instructions)
	}
	if strings.Contains(instructions, "cat output") && !strings.Contains(instructions, "instead of printing cat output") {
		t.Fatalf("tool instructions should explicitly reject cat-output simulation: %q", instructions)
	}
}

func TestTraeToolNameNormalizerMapsRequestToolAliases(t *testing.T) {
	openaiReq := []byte(`{"tools":[{"type":"function","function":{"name":"Bash"}},{"type":"function","function":{"name":"Read"}}]}`)
	normalize := buildTraeToolNameNormalizer(openaiReq, nil)

	if got := normalize("ExecuteBash"); got != "Bash" {
		t.Fatalf("ExecuteBash normalized to %q, want Bash", got)
	}
	if got := normalize("ReadFile"); got != "Read" {
		t.Fatalf("ReadFile normalized to %q, want Read", got)
	}
	if got := normalize("UnknownTool"); got != "UnknownTool" {
		t.Fatalf("unknown tool normalized to %q, want unchanged", got)
	}
}

func TestNormalizeTraeToolArgumentsReadFileName(t *testing.T) {
	got := normalizeTraeToolArguments("Read", `{"file_name":"README.md","limit":20}`)
	if gjson.Get(got, "file_name").Exists() {
		t.Fatalf("normalized arguments should remove file_name: %s", got)
	}
	filePath := gjson.Get(got, "file_path").String()
	if filePath == "" || !filepath.IsAbs(filePath) {
		t.Fatalf("file_path = %q, want absolute path; args=%s", filePath, got)
	}
	if gotLimit := gjson.Get(got, "limit").Int(); gotLimit != 20 {
		t.Fatalf("limit = %d, want preserved 20; args=%s", gotLimit, got)
	}
}
