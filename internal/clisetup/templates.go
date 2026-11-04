package clisetup

import (
	"strconv"
	"strings"
)

// templates ports TOOL_TEMPLATES from Axon's
// src/shared/constants/cliToolTemplates.js verbatim: same ids, names,
// formats, per-OS paths, config builders, and verify commands. The JS
// file defines 15 tools; the setup route serves any key in the map, so
// all 15 are ported (deepseek-tui and grok-build included).
var templates = map[string]Template{
	"claude": {
		ID: "claude", Name: "Claude Code", Format: "json_merge",
		Posix:   []string{"~/.claude/settings.json"},
		Windows: []string{`$env:USERPROFILE\.claude\settings.json`},
		Verify:  "claude --version",
		Generate: func(p Params) any {
			env := NewJObj(
				"ANTHROPIC_BASE_URL", p.BaseURL,
				"ANTHROPIC_AUTH_TOKEN", p.apiKeyOr(),
				"ANTHROPIC_DEFAULT_SONNET_MODEL", orStr(p.Models["sonnet"], "cc/claude-sonnet-5"),
				"ANTHROPIC_DEFAULT_OPUS_MODEL", orStr(p.Models["opus"], "cc/claude-opus-5"),
				"ANTHROPIC_DEFAULT_HAIKU_MODEL", orStr(p.Models["haiku"], "cc/claude-haiku-4-5-20251001"),
			)
			if p.Models["fable"] != "" {
				env.Set("ANTHROPIC_DEFAULT_FABLE_MODEL", p.Models["fable"])
			}
			if p.Models["model"] != "" {
				env.Set("ANTHROPIC_MODEL", p.Models["model"])
			}
			if p.HasMaxContext {
				env.Set("CLAUDE_CODE_MAX_CONTEXT_TOKENS", p.MaxContextTokens)
			}
			return NewJObj(
				"hasCompletedOnboarding", true,
				"env", env,
			)
		},
	},

	"codex": {
		ID: "codex", Name: "OpenAI Codex", Format: "toml_block",
		Posix:   []string{"~/.codex/config.toml"},
		Windows: []string{`$env:USERPROFILE\.codex\config.toml`},
		Verify:  "codex --version",
		Generate: func(p Params) any {
			model := p.modelOr("gpt-5")
			subModel := model
			if p.HasSubagentModel {
				subModel = p.SubagentModel
			}
			cleanBase := cleanBaseStripV1(p.BaseURL)
			return "model = \"" + model + "\"\n" +
				"model_provider = \"axonrouter\"\n" +
				"\n" +
				"[model_providers.axonrouter]\n" +
				"name = \"AxonRouter\"\n" +
				"base_url = \"" + cleanBase + "\"\n" +
				"wire_api = \"responses\"\n" +
				"\n" +
				"[model_providers.axonrouter.http_headers]\n" +
				"Authorization = \"Bearer " + p.apiKeyOr() + "\"\n" +
				"\n" +
				"[agents]\n" +
				"default_subagent_model = \"" + subModel + "\"\n"
		},
	},

	"openclaw": {
		ID: "openclaw", Name: "Open Claw", Format: "json_merge",
		Posix:   []string{"~/.openclaw/openclaw.json"},
		Windows: []string{`$env:USERPROFILE\.openclaw\openclaw.json`},
		Verify:  "openclaw --version",
		Generate: func(p Params) any {
			model := p.modelOr("claude-sonnet-5")
			name := model
			if i := strings.LastIndex(model, "/"); i >= 0 {
				name = model[i+1:]
			}
			return NewJObj(
				"agents", NewJObj(
					"defaults", NewJObj(
						"model", NewJObj("primary", "axonrouter/"+model),
					),
				),
				"models", NewJObj(
					"providers", NewJObj(
						"axonrouter", NewJObj(
							"baseUrl", cleanBaseV1(p.BaseURL),
							"apiKey", p.apiKeyOr(),
							"api", "openai-completions",
							"models", []any{NewJObj("id", model, "name", name)},
						),
					),
				),
			)
		},
	},

	"droid": {
		ID: "droid", Name: "Factory Droid", Format: "json_merge",
		Posix:   []string{"~/.factory/settings.json"},
		Windows: []string{`$env:USERPROFILE\.factory\settings.json`},
		Verify:  "droid --version",
		Generate: func(p Params) any {
			list := p.modelsListOr([]string{"claude-sonnet-5", "claude-opus-5", "gpt-5"})
			models := make([]any, 0, len(list))
			for i, m := range list {
				models = append(models, NewJObj(
					"model", m,
					"id", "custom:AxonRouter-"+itoa(i),
					"index", i,
					"baseUrl", cleanBaseV1(p.BaseURL),
					"apiKey", p.apiKeyOr(),
					"displayName", m,
					"maxOutputTokens", 131072,
					"noImageSupport", false,
					"provider", "openai",
				))
			}
			return NewJObj("customModels", models)
		},
	},

	"hermes": {
		ID: "hermes", Name: "Hermes Agent", Format: "multi_file",
		Verify: "hermes --version",
		Files: []FileSpec{
			{
				PathPosix: "~/.hermes/config.yaml",
				PathWin:   `$env:USERPROFILE\.hermes\config.yaml`,
				Content: func(p Params) string {
					return "model:\n" +
						"  default: \"" + p.modelOr("hermes-3-llama-3.1-405b") + "\"\n" +
						"  provider: \"custom\"\n" +
						"  base_url: \"" + cleanBaseV1(p.BaseURL) + "\"\n" +
						"  api_key: ${OPENAI_API_KEY}\n"
				},
			},
			{
				PathPosix: "~/.hermes/.env",
				PathWin:   `$env:USERPROFILE\.hermes\.env`,
				Content: func(p Params) string {
					return "OPENAI_API_KEY=" + p.apiKeyOr() + "\n"
				},
			},
		},
	},

	"opencode": {
		ID: "opencode", Name: "OpenCode", Format: "json_merge",
		Posix:   []string{"~/.config/opencode/opencode.json"},
		Windows: []string{`$env:APPDATA\opencode\opencode.json`},
		Verify:  "opencode --version",
		Generate: func(p Params) any {
			model := p.modelOr("claude-sonnet-5")
			sub := model
			if p.HasSubagentModel {
				sub = p.SubagentModel
			}
			return NewJObj(
				"provider", NewJObj(
					"axonrouter", NewJObj(
						"npm", "@ai-sdk/openai-compatible",
						"options", NewJObj(
							"baseURL", cleanBaseV1(p.BaseURL),
							"apiKey", p.apiKeyOr(),
						),
						"models", NewJObj(
							model, NewJObj(
								"name", model,
								"modalities", NewJObj(
									"input", []string{"text", "image"},
									"output", []string{"text"},
								),
							),
						),
					),
				),
				"model", "axonrouter/"+model,
				"agent", NewJObj(
					"explorer", NewJObj(
						"mode", "subagent",
						"model", "axonrouter/"+sub,
					),
				),
			)
		},
	},

	"cline": {
		ID: "cline", Name: "Cline", Format: "multi_file",
		Files: []FileSpec{
			{
				PathPosix: "~/.cline/data/globalState.json",
				PathWin:   `$env:USERPROFILE\.cline\data\globalState.json`,
				Content: func(p Params) string {
					model := p.modelOr("claude-sonnet-5")
					return StringifyJSON(NewJObj(
						"actModeApiProvider", "openai",
						"planModeApiProvider", "openai",
						"openAiBaseUrl", cleanBaseStripV1(p.BaseURL),
						"openAiModelId", model,
						"planModeOpenAiModelId", model,
					))
				},
			},
			{
				PathPosix: "~/.cline/data/secrets.json",
				PathWin:   `$env:USERPROFILE\.cline\data\secrets.json`,
				Content: func(p Params) string {
					return StringifyJSON(NewJObj("openAiApiKey", p.apiKeyOr()))
				},
			},
		},
	},

	"kilo": {
		ID: "kilo", Name: "Kilo Code", Format: "json_merge",
		Posix:   []string{"~/.local/share/kilo/auth.json"},
		Windows: []string{`$env:LOCALAPPDATA\kilo\auth.json`},
		Generate: func(p Params) any {
			return NewJObj(
				"openai-compatible", NewJObj(
					"type", "api-key",
					"apiKey", p.apiKeyOr(),
					"baseUrl", cleanBaseV1(p.BaseURL),
					"model", p.modelOr("claude-sonnet-5"),
				),
			)
		},
	},

	"deepseek-tui": {
		ID: "deepseek-tui", Name: "DeepSeek TUI", Format: "toml_replace",
		Posix:   []string{"~/.deepseek/config.toml"},
		Windows: []string{`$env:USERPROFILE\.deepseek\config.toml`},
		Generate: func(p Params) any {
			return "[providers.openai]\n" +
				"base_url = \"" + cleanBaseV1(p.BaseURL) + "\"\n" +
				"api_key = \"" + p.apiKeyOr() + "\"\n" +
				"model = \"" + p.modelOr("deepseek-chat") + "\"\n"
		},
	},

	"jcode": {
		ID: "jcode", Name: "jcode", Format: "multi_file",
		Files: []FileSpec{
			{
				PathPosix: "~/.jcode/config.toml",
				PathWin:   `$env:USERPROFILE\.jcode\config.toml`,
				Content: func(p Params) string {
					model := p.modelOr("claude-sonnet-5")
					return "[providers.axonrouter]\n" +
						"type = \"openai-compatible\"\n" +
						"base_url = \"" + cleanBaseV1(p.BaseURL) + "\"\n" +
						"auth = \"bearer\"\n" +
						"api_key_env = \"JCODE_AXONROUTER_API_KEY\"\n" +
						"env_file = \"provider-axonrouter.env\"\n" +
						"default_model = \"" + model + "\"\n" +
						"requires_api_key = true\n" +
						"\n" +
						"[[providers.axonrouter.models]]\n" +
						"id = \"" + model + "\"\n"
				},
			},
			{
				PathPosix: "~/.config/jcode/provider-axonrouter.env",
				PathWin:   `$env:USERPROFILE\.config\jcode\provider-axonrouter.env`,
				Content: func(p Params) string {
					return "JCODE_AXONROUTER_API_KEY=\"" + p.apiKeyOr() + "\"\n"
				},
			},
		},
	},

	"grok-build": {
		ID: "grok-build", Name: "Grok Build", Format: "toml_block",
		Posix:   []string{"~/.grok/config.toml"},
		Windows: []string{`$env:USERPROFILE\.grok\config.toml`},
		Generate: func(p Params) any {
			return "[models]\n" +
				"default = \"axonrouter\"\n" +
				"\n" +
				"[model.axonrouter]\n" +
				"model = \"" + p.modelOr("grok-beta") + "\"\n" +
				"base_url = \"" + cleanBaseV1(p.BaseURL) + "\"\n" +
				"name = \"AxonRouter\"\n" +
				"description = \"Routed via AxonRouter gateway\"\n" +
				"api_backend = \"chat_completions\"\n" +
				"api_key = \"" + p.apiKeyOr() + "\"\n" +
				"context_window = 200000\n"
		},
	},

	"copilot": {
		ID: "copilot", Name: "GitHub Copilot", Format: "copilot_json",
		Posix:   []string{"~/.config/Code/User/chatLanguageModels.json"},
		Darwin:  []string{"~/Library/Application Support/Code/User/chatLanguageModels.json"},
		Windows: []string{`$env:APPDATA\Code\User\chatLanguageModels.json`},
		Generate: func(p Params) any {
			list := p.modelsListOr([]string{"claude-sonnet-5", "gpt-5"})
			cleanBase := cleanBaseStripV1(p.BaseURL)
			models := make([]any, 0, len(list))
			for _, id := range list {
				models = append(models, NewJObj(
					"id", id,
					"name", id,
					"url", cleanBase+"/chat/completions#models.ai.azure.com",
					"toolCalling", true,
					"vision", false,
					"maxInputTokens", 128000,
					"maxOutputTokens", 16000,
				))
			}
			return []any{NewJObj(
				"name", "AxonRouter",
				"vendor", "azure",
				"apiKey", p.apiKeyOr(),
				"models", models,
			)}
		},
	},

	"cowork": {
		ID: "cowork", Name: "Claude Desktop Cowork", Format: "json_replace",
		Darwin:  []string{"~/Library/Application Support/Claude-3p/configLibrary/axonrouter.json"},
		Windows: []string{`$env:APPDATA\Claude-3p\configLibrary\axonrouter.json`},
		Posix:   []string{"~/.config/Claude-3p/configLibrary/axonrouter.json"},
		Generate: func(p Params) any {
			list := p.modelsListOr([]string{"claude-sonnet-5", "claude-opus-5"})
			models := make([]any, 0, len(list))
			for _, name := range list {
				models = append(models, NewJObj("name", name))
			}
			return NewJObj(
				"inferenceProvider", "gateway",
				"inferenceGatewayBaseUrl", cleanBaseV1(p.BaseURL),
				"inferenceGatewayApiKey", p.apiKeyOr(),
				"inferenceModels", models,
			)
		},
	},

	"qwen": {
		ID: "qwen", Name: "Qwen Code", Format: "json_merge",
		Posix:   []string{"~/.qwen/settings.json"},
		Windows: []string{`$env:USERPROFILE\.qwen\settings.json`},
		Generate: func(p Params) any {
			return NewJObj(
				"security", NewJObj(
					"auth", NewJObj(
						"selectedType", "openai",
						"apiKey", p.apiKeyOr(),
						"baseUrl", cleanBaseV1(p.BaseURL),
					),
				),
				"model", NewJObj("name", p.modelOr("qwen-2.5-coder-32b")),
			)
		},
	},

	"amp": {
		ID: "amp", Name: "Amp CLI", Format: "env_export",
		Posix:   []string{"~/.bashrc"},
		Windows: []string{"$PROFILE"},
		Generate: func(p Params) any {
			return "export OPENAI_BASE_URL=\"" + cleanBaseV1(p.BaseURL) + "\"\n" +
				"export OPENAI_API_KEY=\"" + p.apiKeyOr() + "\"\n"
		},
	},
}

// orStr is the JS `value || fallback` idiom for strings.
func orStr(v, fallback string) string {
	if v != "" {
		return v
	}
	return fallback
}

// itoa renders a list index (JS template `${i}`).
func itoa(i int) string {
	return strconv.Itoa(i)
}
