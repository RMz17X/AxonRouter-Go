package clisetup

import "strings"

// GenerateBashScript ports generateBashScript from Axon's
// src/lib/cliSetup/bashGenerator.js, byte-for-byte: same header,
// backup behaviour, node/python3 merge fallback chain for json_merge
// targets, raw heredoc writes for every other format, and the same
// verification footer.
func GenerateBashScript(toolID string, p Params) string {
	tpl, ok := Lookup(toolID)
	if !ok {
		return "#!/usr/bin/env bash\n" +
			"set -euo pipefail\n" +
			"echo \"Error: Unknown tool '" + toolID + "'\"\n" +
			"exit 1\n"
	}

	var fileOperations string
	if tpl.Format == "multi_file" {
		parts := make([]string, 0, len(tpl.Files))
		for i, f := range tpl.Files {
			parts = append(parts, bashSingleFile(f.PathPosix, f.Content(p), "replace", i))
		}
		fileOperations = strings.Join(parts, "\n\n")
	} else {
		targetPath := ""
		if len(tpl.Posix) > 0 {
			targetPath = tpl.Posix[0]
		} else if len(tpl.Darwin) > 0 {
			targetPath = tpl.Darwin[0]
		}
		fileOperations = bashSingleFile(targetPath, contentString(tpl, p), tpl.Format, 0)
	}

	verifySection := ""
	if tpl.Verify != "" {
		bin := strings.SplitN(tpl.Verify, " ", 2)[0]
		verifySection = "\n# Verification check\n" +
			"if command -v " + bin + " >/dev/null 2>&1; then\n" +
			"  printf \"\\033[0;32m[AxonRouter] Detected binary: %s\\033[0m\\n\" \"$(" + tpl.Verify + " 2>/dev/null || true)\"\n" +
			"else\n" +
			"  printf \"\\033[0;33m[AxonRouter] Note: '" + bin + "' binary not found in PATH on this host.\\033[0m\\n\"\n" +
			"  printf \"\\033[0;33m          Config written. Run after installing the tool.\\033[0m\\n\"\n" +
			"fi"
	}

	baseURL := p.BaseURL
	if baseURL == "" {
		baseURL = "http://localhost:3777"
	}

	return "#!/usr/bin/env bash\n" +
		"# ==============================================================================\n" +
		"# AxonRouter Host Setup Script: " + tpl.Name + "\n" +
		"# Generated automatically by AxonRouter Gateway\n" +
		"# ==============================================================================\n" +
		"set -euo pipefail\n" +
		"\n" +
		"printf \"\\033[0;36m[AxonRouter] Configuring %s for AxonRouter host gateway...\\033[0m\\n\" \"" + tpl.Name + "\"\n" +
		"\n" +
		fileOperations + "\n" +
		"\n" +
		verifySection + "\n" +
		"\n" +
		"printf \"\\033[0;32m[AxonRouter] ✓ Configuration for %s applied successfully!\\033[0m\\n\" \"" + tpl.Name + "\"\n" +
		"printf \"\\033[0;32m[AxonRouter] Gateway Target: %s\\033[0m\\n\" \"" + baseURL + "\"\n"
}

// contentString renders a template's Generate result the way the JS
// generators do: strings pass through, everything else goes through
// JSON.stringify(value, null, 2).
func contentString(tpl Template, p Params) string {
	raw := tpl.Generate(p)
	if s, ok := raw.(string); ok {
		return s
	}
	return StringifyJSON(raw)
}

// bashSingleFile ports generateSingleFileBash: one target file write,
// with the deep-merge-via-node/python3 path for json_merge and a plain
// heredoc overwrite for all other formats.
func bashSingleFile(rawPath, contentStr, format string, index int) string {
	i := itoa(index)
	targetExpr := "\"" + rawPath + "\""
	if strings.HasPrefix(rawPath, "~/") {
		targetExpr = "\"$HOME/" + rawPath[2:] + "\""
	}

	header := "\n# Target " + itoa(index+1) + ": " + rawPath + "\n" +
		"TARGET_" + i + "=" + targetExpr + "\n" +
		"DIR_" + i + "=\"$(dirname \"$TARGET_" + i + "\")\"\n" +
		"mkdir -p \"$DIR_" + i + "\"\n" +
		"\n" +
		"if [ -f \"$TARGET_" + i + "\" ]; then\n" +
		"  BAK_" + i + "=\"$TARGET_" + i + ".bak.$(date +%Y%m%d%H%M%S)\"\n" +
		"  cp \"$TARGET_" + i + "\" \"$BAK_" + i + "\"\n" +
		"  printf \"\\033[0;34m[AxonRouter] Backed up existing config to %s\\033[0m\\n\" \"$BAK_" + i + "\"\n" +
		"fi\n" +
		"\n"

	if format == "json_merge" {
		return header +
			"cat << 'EOF_AXON_JSON_" + i + "' > \"$DIR_" + i + "/.axonrouter_incoming_" + i + ".json\"\n" +
			contentStr + "\n" +
			"EOF_AXON_JSON_" + i + "\n" +
			"\n" +
			"if command -v node >/dev/null 2>&1; then\n" +
			"  node -e '\n" +
			"    const fs = require(\"fs\");\n" +
			"    const target = process.argv[1];\n" +
			"    const incoming = JSON.parse(fs.readFileSync(process.argv[2], \"utf8\"));\n" +
			"    let existing = {};\n" +
			"    if (fs.existsSync(target)) {\n" +
			"      try { existing = JSON.parse(fs.readFileSync(target, \"utf8\")); } catch(e) {}\n" +
			"    }\n" +
			"    const merged = { ...existing, ...incoming };\n" +
			"    if (existing.env && incoming.env) {\n" +
			"      merged.env = { ...existing.env, ...incoming.env };\n" +
			"    }\n" +
			"    if (existing.agents && incoming.agents) {\n" +
			"      merged.agents = { ...existing.agents, ...incoming.agents };\n" +
			"    }\n" +
			"    if (existing.models && incoming.models) {\n" +
			"      merged.models = { ...existing.models, ...incoming.models };\n" +
			"    }\n" +
			"    fs.writeFileSync(target, JSON.stringify(merged, null, 2));\n" +
			"  ' \"$TARGET_" + i + "\" \"$DIR_" + i + "/.axonrouter_incoming_" + i + ".json\"\n" +
			"  rm -f \"$DIR_" + i + "/.axonrouter_incoming_" + i + ".json\"\n" +
			"elif command -v python3 >/dev/null 2>&1; then\n" +
			"  python3 -c '\n" +
			"import json, sys, os\n" +
			"target, incoming_file = sys.argv[1], sys.argv[2]\n" +
			"with open(incoming_file, \"r\") as f:\n" +
			"    incoming = json.load(f)\n" +
			"existing = {}\n" +
			"if os.path.exists(target):\n" +
			"    try:\n" +
			"        with open(target, \"r\") as f:\n" +
			"            existing = json.load(f)\n" +
			"    except Exception:\n" +
			"        pass\n" +
			"merged = {**existing, **incoming}\n" +
			"if \"env\" in existing and \"env\" in incoming:\n" +
			"    merged[\"env\"] = {**existing[\"env\"], **incoming[\"env\"]}\n" +
			"with open(target, \"w\") as f:\n" +
			"    json.dump(merged, f, indent=2)\n" +
			"' \"$TARGET_" + i + "\" \"$DIR_" + i + "/.axonrouter_incoming_" + i + ".json\"\n" +
			"  rm -f \"$DIR_" + i + "/.axonrouter_incoming_" + i + ".json\"\n" +
			"else\n" +
			"  mv \"$DIR_" + i + "/.axonrouter_incoming_" + i + ".json\" \"$TARGET_" + i + "\"\n" +
			"fi\n" +
			"printf \"\\033[0;32m[AxonRouter] ✓ Updated %s\\033[0m\\n\" \"$TARGET_" + i + "\"\n"
	}

	return header +
		"cat << 'EOF_AXON_RAW_" + i + "' > \"$TARGET_" + i + "\"\n" +
		contentStr + "\n" +
		"EOF_AXON_RAW_" + i + "\n" +
		"printf \"\\033[0;32m[AxonRouter] ✓ Written %s\\033[0m\\n\" \"$TARGET_" + i + "\"\n"
}
