package clisetup

import "strings"

// GeneratePowerShellScript ports generatePowerShellScript from Axon's
// src/lib/cliSetup/powershellGenerator.js, byte-for-byte: same header,
// backup behaviour, ConvertFrom-Json merge for json_merge targets and
// here-string overwrite for every other format.
func GeneratePowerShellScript(toolID string, p Params) string {
	tpl, ok := Lookup(toolID)
	if !ok {
		return "# AxonRouter Host Setup Script\n" +
			"$ErrorActionPreference = \"Stop\"\n" +
			"Write-Error \"Unknown tool '" + toolID + "'\"\n"
	}

	var fileOperations string
	if tpl.Format == "multi_file" {
		parts := make([]string, 0, len(tpl.Files))
		for i, f := range tpl.Files {
			parts = append(parts, psSingleFile(f.PathWin, f.Content(p), "replace", i))
		}
		fileOperations = strings.Join(parts, "\n\n")
	} else {
		targetPath := ""
		if len(tpl.Windows) > 0 {
			targetPath = tpl.Windows[0]
		}
		fileOperations = psSingleFile(targetPath, contentString(tpl, p), tpl.Format, 0)
	}

	baseURL := p.BaseURL
	if baseURL == "" {
		baseURL = "http://localhost:3777"
	}

	return "# ==============================================================================\n" +
		"# AxonRouter Host Setup Script (PowerShell): " + tpl.Name + "\n" +
		"# Generated automatically by AxonRouter Gateway\n" +
		"# ==============================================================================\n" +
		"$ErrorActionPreference = \"Stop\"\n" +
		"\n" +
		"Write-Host \"[AxonRouter] Configuring " + tpl.Name + " for AxonRouter host gateway...\" -ForegroundColor Cyan\n" +
		"\n" +
		fileOperations + "\n" +
		"\n" +
		"Write-Host \"[AxonRouter] ✓ Configuration for " + tpl.Name + " applied successfully!\" -ForegroundColor Green\n" +
		"Write-Host \"[AxonRouter] Gateway Target: " + baseURL + "\" -ForegroundColor Green\n"
}

// psPathExpr maps a template windows path onto the PowerShell
// expression the JS generator emits: Join-Path for the three env
// roots, a quoted literal otherwise.
func psPathExpr(rawPath string) string {
	for _, prefix := range []string{`$env:USERPROFILE\`, `$env:APPDATA\`, `$env:LOCALAPPDATA\`} {
		if strings.HasPrefix(rawPath, prefix) {
			env := prefix[:len(prefix)-1] // strip trailing backslash
			return "Join-Path " + env + " \"" + rawPath[len(prefix):] + "\""
		}
	}
	return "\"" + rawPath + "\""
}

// psSingleFile ports generateSingleFilePowerShell.
func psSingleFile(rawPath, contentStr, format string, index int) string {
	i := itoa(index)
	header := "\n# Target " + itoa(index+1) + ": " + rawPath + "\n" +
		"$target_" + i + " = " + psPathExpr(rawPath) + "\n" +
		"$dir_" + i + " = Split-Path -Parent $target_" + i + "\n" +
		"if (-not (Test-Path $dir_" + i + ")) {\n" +
		"    New-Item -ItemType Directory -Force -Path $dir_" + i + " | Out-Null\n" +
		"}\n" +
		"\n" +
		"if (Test-Path $target_" + i + ") {\n" +
		"    $bak_" + i + " = \"$target_" + i + ".bak.$(Get-Date -Format 'yyyyMMddHHmmss')\"\n" +
		"    Copy-Item -Path $target_" + i + " -Destination $bak_" + i + "\n" +
		"    Write-Host \"[AxonRouter] Backed up existing config to $bak_" + i + "\" -ForegroundColor DarkCyan\n" +
		"}\n" +
		"\n"

	if format == "json_merge" {
		return header +
			"$rawJson_" + i + " = @'\n" +
			contentStr + "\n" +
			"'@\n" +
			"\n" +
			"$incoming_" + i + " = $rawJson_" + i + " | ConvertFrom-Json\n" +
			"\n" +
			"if (Test-Path $target_" + i + ") {\n" +
			"    try {\n" +
			"        $existing_" + i + " = Get-Content -Raw -Path $target_" + i + " | ConvertFrom-Json\n" +
			"        # Merge top-level properties\n" +
			"        $incoming_" + i + ".PSObject.Properties | ForEach-Object {\n" +
			"            if ($_.Name -eq \"env\" -and $existing_" + i + ".env) {\n" +
			"                $_.Value.PSObject.Properties | ForEach-Object {\n" +
			"                    $existing_" + i + ".env.$($_.Name) = $_.Value\n" +
			"                }\n" +
			"            } else {\n" +
			"                $existing_" + i + " | Add-Member -MemberType NoteProperty -Name $_.Name -Value $_.Value -Force\n" +
			"            }\n" +
			"        }\n" +
			"        $existing_" + i + " | ConvertTo-Json -Depth 10 | Set-Content -Path $target_" + i + " -Encoding UTF8\n" +
			"    } catch {\n" +
			"        $rawJson_" + i + " | Set-Content -Path $target_" + i + " -Encoding UTF8\n" +
			"    }\n" +
			"} else {\n" +
			"    $rawJson_" + i + " | Set-Content -Path $target_" + i + " -Encoding UTF8\n" +
			"}\n" +
			"Write-Host \"[AxonRouter] ✓ Updated $target_" + i + "\" -ForegroundColor Green\n"
	}

	return header +
		"@'\n" +
		contentStr + "\n" +
		"'@ | Set-Content -Path $target_" + i + " -Encoding UTF8\n" +
		"Write-Host \"[AxonRouter] ✓ Written $target_" + i + "\" -ForegroundColor Green\n"
}
